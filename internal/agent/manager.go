// Package agent manages the lifecycle of agent Linux users.
//
// Responsibilities are split across files:
//   - manager.go: Manager state, Spawn/Terminate/List orchestration
//   - nftables.go: network isolation via nft
//   - systemd.go:  cgroup slices and process lifecycle via systemd-run
package agent

import (
	"fmt"
	"os/exec"
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
		CreatedAt: time.Now(),
	}
	m.agents[uid] = info

	if len(req.Command) > 0 {
		if err := m.startProcess(uid, sliceName, req); err != nil {
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

	// Stop the agent's systemd unit if one was started.
	if m.hasCmd[uid] {
		_ = exec.Command("systemctl", "stop", agentUnitName(uid)).Run()
		delete(m.hasCmd, uid)
	}

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
