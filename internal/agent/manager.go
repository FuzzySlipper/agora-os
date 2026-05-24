// Package agent manages the lifecycle of agent Linux users.
//
// Responsibilities are split across files:
//   - manager.go: Manager state, Spawn/Terminate/List orchestration
//   - nftables.go: network isolation via nft
//   - systemd.go:  cgroup slices and process lifecycle via systemd-run
package agent

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

// Manager tracks active agents and orchestrates their lifecycle. Each agent
// gets a dedicated Linux user, systemd slice, and nftables rules.
type Manager struct {
	mu          sync.Mutex
	agents      map[uint32]*schema.AgentInfo
	hasCmd      map[uint32]bool     // agents with a running systemd unit
	ruleHandles map[uint32][]uint64 // nft rule handles per agent uid
	nextUID     uint32
}

func NewManager() *Manager {
	return &Manager{
		agents:      make(map[uint32]*schema.AgentInfo),
		hasCmd:      make(map[uint32]bool),
		ruleHandles: make(map[uint32][]uint64),
		nextUID:     schema.AgentUIDBase,
	}
}

// RecoverAgents discovers agent users and systemd units left by a prior run
// and rebuilds in-memory state. It is called after BootstrapNftables so the
// nft chain has been flushed and is empty.
//
// Recovery design: flush-then-reapply. BootstrapNftables flushes the entire
// owned chain indiscriminately, removing all prior rules. RecoverAgents then
// discovers surviving agent users and re-applies NetDeny rules for each one.
// This ensures kernel state matches Manager state for all known agent UIDs.
//
// Known limitation: recovered agents always get NetDeny regardless of their
// original NetAccess policy (NetAllow, NetLocalOnly, NetDeny). The original
// policy is not persisted to disk, so we default to the safest option. This
// can be improved in a follow-up with disk-persisted policy storage.
func (m *Manager) RecoverAgents() {
	m.mu.Lock()
	defer m.mu.Unlock()

	agentUsers := discoverAgentUsers()
	activeSlices := discoverActiveSlices()
	activeCmds := discoverActiveCmds()

	for uid, username := range agentUsers {
		_, isRunning := activeSlices[uid]
		hasCmd := activeCmds[uid]

		// Apply NetDeny for ALL discovered agent users, not just running ones.
		// After chain flush, UIDs without nft rules have unrestricted network
		// access. Applying NetDeny for all ensures kernel state matches Manager
		// state. For truly stale users (no slice), NetDeny is harmless and
		// prevents misuse of the UID until it is cleaned up.
		policy := schema.NetDeny
		if err := m.applyNetRules(uid, policy); err != nil {
			log.Printf("recovery: failed to apply net rules for uid %d: %v", uid, err)
		}

		sliceName := fmt.Sprintf("agent-%d.slice", uid)
		status := schema.StatusExited
		if isRunning {
			status = schema.StatusRunning
		}

		m.agents[uid] = &schema.AgentInfo{
			Name:      username,
			UID:       uid,
			Status:    status,
			Slice:     sliceName,
			NetAccess: policy,
			CreatedAt: time.Now(), // best-effort; not recoverable
		}

		if hasCmd {
			hasCmd = exec.Command("systemctl", "is-active", "--quiet", agentUnitName(uid)).Run() == nil
		}
		m.hasCmd[uid] = hasCmd

		log.Printf("recovery: adopted agent uid=%d user=%s policy=%s running=%v", uid, username, policy, isRunning)
	}

	// Set nextUID above all discovered UIDs.
	for uid := range agentUsers {
		if uid >= m.nextUID {
			m.nextUID = uid + 1
		}
	}
}

func (m *Manager) Spawn(req schema.SpawnAgentRequest) (*schema.AgentInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	uid := req.UID
	if uid == 0 {
		uid = m.nextUID
	} else if uid < schema.AgentUIDBase || uid >= schema.AgentUIDMax {
		return nil, fmt.Errorf("requested uid %d outside agent range [%d,%d)", uid, schema.AgentUIDBase, schema.AgentUIDMax)
	} else if _, exists := m.agents[uid]; exists {
		return nil, fmt.Errorf("requested uid %d already active", uid)
	}
	if uid >= schema.AgentUIDMax {
		return nil, fmt.Errorf("agent uid pool exhausted")
	}
	if uid >= m.nextUID {
		m.nextUID = uid + 1
	}

	username := fmt.Sprintf("agent-%s-%d", req.Name, uid)

	// Create system user with no login shell, home under /var/lib/agents/
	home := fmt.Sprintf("/var/lib/agents/%s", username)
	err := exec.Command("useradd",
		"--system",
		"--uid", fmt.Sprintf("%d", uid),
		"--home-dir", home,
		"--create-home",
		"--shell", "/usr/sbin/nologin",
		username,
	).Run()
	if err != nil {
		return nil, fmt.Errorf("useradd: %w", err)
	}

	// Set up systemd transient slice with resource limits
	sliceName := fmt.Sprintf("agent-%d.slice", uid)
	if err := m.createSlice(sliceName, req); err != nil {
		// Best-effort cleanup
		_ = exec.Command("userdel", "--remove", username).Run()
		return nil, fmt.Errorf("cgroup slice: %w", err)
	}

	// Apply nftables rules for network access control
	if err := m.applyNetRules(uid, req.NetAccess); err != nil {
		_ = exec.Command("userdel", "--remove", username).Run()
		return nil, fmt.Errorf("nftables: %w", err)
	}

	info := &schema.AgentInfo{
		Name:      req.Name,
		UID:       uid,
		Status:    schema.StatusRunning,
		Slice:     sliceName,
		CPUQuota:  req.CPUQuota,
		MemoryMax: req.MemoryMax,
		NetAccess: req.NetAccess,
		CreatedAt: time.Now(),
	}
	m.agents[uid] = info

	if len(req.Command) > 0 {
		if err := m.startProcess(uid, username, sliceName, req); err != nil {
			delete(m.agents, uid)
			_ = exec.Command("pkill", "-U", fmt.Sprintf("%d", uid)).Run()
			_ = m.removeNetRules(uid)
			_ = exec.Command("systemctl", "stop", sliceName).Run()
			_ = exec.Command("userdel", "--remove", username).Run()
			return nil, fmt.Errorf("start process: %w", err)
		}
	}

	return info, nil
}

func (m *Manager) Terminate(uid uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, ok := m.agents[uid]
	if !ok {
		return fmt.Errorf("agent uid %d not found", uid)
	}

	username := fmt.Sprintf("agent-%s-%d", info.Name, uid)

	// Best-effort cleanup of the transient command unit even if the process
	// has already exited. Otherwise failed units linger in systemd and can
	// block reuse of the deterministic unit name after service restarts.
	_ = exec.Command("systemctl", "stop", agentUnitName(uid)).Run()
	_ = exec.Command("systemctl", "reset-failed", agentUnitName(uid)).Run()
	delete(m.hasCmd, uid)

	// Kill any remaining processes owned by this uid
	_ = exec.Command("pkill", "-U", fmt.Sprintf("%d", uid)).Run()

	// Remove nftables rules
	_ = m.removeNetRules(uid)

	// Stop and remove the systemd slice
	_ = exec.Command("systemctl", "stop", info.Slice).Run()

	// Remove the system user and home directory
	if err := exec.Command("userdel", "--remove", username).Run(); err != nil {
		return fmt.Errorf("userdel: %w", err)
	}

	delete(m.agents, uid)
	return nil
}

func (m *Manager) List() []schema.AgentInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]schema.AgentInfo, 0, len(m.agents))
	for _, a := range m.agents {
		info := *a
		// Authoritative status: query systemd for agents with a command unit.
		if m.hasCmd[info.UID] {
			if exec.Command("systemctl", "is-active", "--quiet", agentUnitName(info.UID)).Run() != nil {
				info.Status = schema.StatusExited
				a.Status = schema.StatusExited
				delete(m.hasCmd, info.UID)
			}
		}
		out = append(out, info)
	}
	return out
}

func defaultStr(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}

// discoverAgentUsers reads /etc/passwd for users matching the agent naming
// convention: agent-<name>-<uid>. Returns a map of uid → username.
func discoverAgentUsers() map[uint32]string {
	users := make(map[uint32]string)
	out, err := exec.Command("getent", "passwd").Output()
	if err != nil {
		return users
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.SplitN(line, ":", 7)
		if len(fields) < 3 {
			continue
		}
		username := fields[0]
		if !strings.HasPrefix(username, "agent-") {
			continue
		}
		var uid uint32
		if _, err := fmt.Sscanf(fields[2], "%d", &uid); err != nil {
			continue
		}
		users[uid] = username
	}
	return users
}

// discoverActiveSlices finds running agent slices (agent-*.slice).
func discoverActiveSlices() map[uint32]bool {
	active := make(map[uint32]bool)
	out, err := exec.Command("systemctl", "list-units",
		"--type=slice", "--state=active", "--no-legend", "--no-pager").Output()
	if err != nil {
		return active
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		sliceName := fields[0]
		var uid uint32
		if _, err := fmt.Sscanf(sliceName, "agent-%d.slice", &uid); err == nil {
			active[uid] = true
		}
	}
	return active
}

// discoverActiveCmds finds loaded agent command units (agent-*-cmd.service).
func discoverActiveCmds() map[uint32]bool {
	cmds := make(map[uint32]bool)
	out, err := exec.Command("systemctl", "list-units",
		"--type=service", "--state=active", "--no-legend", "--no-pager").Output()
	if err != nil {
		return cmds
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		svcName := fields[0]
		var uid uint32
		if _, err := fmt.Sscanf(svcName, "agent-%d-cmd.service", &uid); err == nil {
			cmds[uid] = true
		}
	}
	return cmds
}
