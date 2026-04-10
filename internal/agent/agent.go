// Package agent manages the lifecycle of agent Linux users.
package agent

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/patch/agent-os/internal/schema"
)

const (
	agentUIDBase = 60000 // agent uids start here to avoid collisions
	agentUIDMax  = 61000
	nftChain     = "agent-os-output" // dedicated chain so we can flush without touching host rules
)

// BootstrapNftables ensures the inet filter table, base output chain, and
// our dedicated agent-os-output chain exist. Idempotent — safe to call on
// every startup. Per-agent rules go in agent-os-output so we can flush it
// without disturbing the host's existing nft state.
func BootstrapNftables() error {
	script := strings.Join([]string{
		"add table inet filter",
		"add chain inet filter output { type filter hook output priority 0 ; policy accept ; }",
		"add chain inet filter " + nftChain,
		"flush chain inet filter " + nftChain,
	}, "\n") + "\n"

	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft bootstrap: %s: %w", string(out), err)
	}

	// Add jump from output → agent-os-output, unless it already exists.
	out, err := exec.Command("nft", "list", "chain", "inet", "filter", "output").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft list output: %s: %w", string(out), err)
	}
	if !strings.Contains(string(out), "jump "+nftChain) {
		if out, err := exec.Command("nft", "add", "rule", "inet", "filter", "output",
			"jump", nftChain,
		).CombinedOutput(); err != nil {
			return fmt.Errorf("nft add jump: %s: %w", string(out), err)
		}
	}

	return nil
}

type Manager struct {
	mu      sync.Mutex
	agents  map[uint32]*schema.AgentInfo
	procs   map[uint32]*exec.Cmd // systemd-run scope processes, keyed by uid
	nextUID uint32
}

func NewManager() *Manager {
	return &Manager{
		agents:  make(map[uint32]*schema.AgentInfo),
		procs:   make(map[uint32]*exec.Cmd),
		nextUID: agentUIDBase,
	}
}

func (m *Manager) Spawn(req schema.SpawnAgentRequest) (*schema.AgentInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	uid := m.nextUID
	if uid >= agentUIDMax {
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
		Status:    "running",
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

	// Remove proc tracking — pkill below handles the actual kill,
	// and the waitProcess goroutine will no-op when it can't find the agent.
	delete(m.procs, uid)

	// Kill all processes owned by this uid
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
		out = append(out, *a)
	}
	return out
}

// startProcess launches the agent's command inside its cgroup slice via
// systemd-run --scope.  The scope inherits the slice's cgroup tree and gets
// its own resource limits so the command is properly contained.
func (m *Manager) startProcess(uid uint32, slice string, req schema.SpawnAgentRequest) error {
	args := []string{
		"--scope",
		"--unit", fmt.Sprintf("agent-%d-cmd", uid),
		"--slice", slice,
		"--uid", fmt.Sprintf("%d", uid),
		"--gid", fmt.Sprintf("%d", uid),
		"--property", fmt.Sprintf("MemoryMax=%s", defaultStr(req.MemoryMax, "512M")),
		"--property", fmt.Sprintf("CPUQuota=%s", defaultStr(req.CPUQuota, "50%")),
		"--",
	}
	args = append(args, req.Command...)

	cmd := exec.Command("systemd-run", args...)
	if err := cmd.Start(); err != nil {
		return err
	}

	m.procs[uid] = cmd
	go m.waitProcess(uid, cmd)
	return nil
}

// waitProcess blocks until the agent's command exits, then updates status.
func (m *Manager) waitProcess(uid uint32, cmd *exec.Cmd) {
	cmd.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	if info, ok := m.agents[uid]; ok {
		info.Status = "exited"
	}
	delete(m.procs, uid)
}

func (m *Manager) createSlice(name string, req schema.SpawnAgentRequest) error {
	// systemd-run creates a transient slice with cgroup v2 resource limits.
	// This is the simplest path — the containerd/cgroups library is the
	// upgrade when you need programmatic control beyond what systemd exposes.
	args := []string{
		"systemd-run",
		"--slice", name,
		"--property", fmt.Sprintf("MemoryMax=%s", defaultStr(req.MemoryMax, "512M")),
		"--property", fmt.Sprintf("CPUQuota=%s", defaultStr(req.CPUQuota, "50%")),
		"--remain-after-exit",
		"true", // no-op command; slice stays open for processes to join
	}
	return exec.Command(args[0], args[1:]...).Run()
}

func (m *Manager) applyNetRules(uid uint32, policy schema.NetPolicy) error {
	if policy == "" {
		policy = schema.NetDeny
	}

	switch policy {
	case schema.NetAllow:
		return nil // no rules needed
	case schema.NetLocalOnly:
		// Allow loopback only — drop everything else for this uid
		return exec.Command("nft", "add", "rule", "inet", "filter", nftChain,
			"meta", "skuid", fmt.Sprintf("%d", uid),
			"oif", "!=", "lo",
			"drop",
		).Run()
	case schema.NetDeny:
		// Drop all outbound for this uid
		return exec.Command("nft", "add", "rule", "inet", "filter", nftChain,
			"meta", "skuid", fmt.Sprintf("%d", uid),
			"drop",
		).Run()
	default:
		return fmt.Errorf("unknown net policy: %s", policy)
	}
}

func (m *Manager) removeNetRules(uid uint32) error {
	// In production you'd track rule handles and delete by handle.
	// For v1, flushing and rebuilding the agent chain is simpler.
	// TODO: track nft rule handles per agent uid
	return nil
}

func defaultStr(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}
