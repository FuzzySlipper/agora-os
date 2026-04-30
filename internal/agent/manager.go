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
// nft chain exists but is empty. UIDs with prior nft rules get those rules
// re-applied; UIDs without prior rules get the safe default (NetDeny).
// Stale nft rules for UIDs with no active user or process are cleaned up.
func (m *Manager) RecoverAgents(priorRuleUIDs map[uint32]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	agentUsers := discoverAgentUsers()
	activeSlices := discoverActiveSlices()
	activeCmds := discoverActiveCmds()

	// Clean up stale nft rules for UIDs that no longer have an agent user.
	for uid := range priorRuleUIDs {
		if _, exists := agentUsers[uid]; !exists {
			log.Printf("recovery: cleaning stale nft rules for uid %d", uid)
			removeStaleRules(uid)
			delete(priorRuleUIDs, uid)
		}
	}

	for uid, username := range agentUsers {
		_, isRunning := activeSlices[uid]
		hasCmd := activeCmds[uid]

		// Default to NetDeny — we cannot reliably recover the original policy.
		policy := schema.NetDeny

		if isRunning {
			if err := m.applyNetRules(uid, policy); err != nil {
				log.Printf("recovery: failed to apply net rules for uid %d: %v", uid, err)
			}
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

		if isRunning {
			log.Printf("recovery: adopted agent uid=%d user=%s policy=%s", uid, username, policy)
		}
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

	uid := m.nextUID
	if uid >= schema.AgentUIDMax {
		return nil, fmt.Errorf("agent uid pool exhausted")
	}
	m.nextUID++

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

// removeStaleRules removes nft rules for a UID that no longer has an agent user.
func removeStaleRules(uid uint32) {
	uidStr := fmt.Sprintf("%d", uid)
	out, err := exec.Command("nft", "list", "chain", "inet", "filter", nftChain).CombinedOutput()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "skuid "+uidStr) || !strings.Contains(line, "handle") {
			continue
		}
		idx := strings.LastIndex(line, "handle ")
		if idx < 0 {
			continue
		}
		handleStr := strings.TrimSpace(line[idx+7:])
		if _, err := fmt.Sscanf(handleStr, "%d", new(uint64)); err != nil {
			continue
		}
		exec.Command("nft", "delete", "rule", "inet", "filter", nftChain, "handle", handleStr).Run()
	}
}
