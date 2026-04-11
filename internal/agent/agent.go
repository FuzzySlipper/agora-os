// Package agent manages the lifecycle of agent Linux users.
package agent

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

const (
	nftChain = "agent-os-output" // dedicated chain so we can flush without touching host rules
)

// BootstrapNftables ensures the inet filter table, base output chain, and
// our dedicated agent-os-output chain exist. Each step probes before creating
// so the function is safe to call on every startup regardless of prior state.
func BootstrapNftables() error {
	// 1. Table — nft add table is a no-op if it already exists.
	if out, err := exec.Command("nft", "add", "table", "inet", "filter").CombinedOutput(); err != nil {
		return fmt.Errorf("nft add table: %s: %w", string(out), err)
	}

	// 2. Base output chain — only create if missing (don't clobber an
	//    existing chain that may have different priority/policy).
	if exec.Command("nft", "list", "chain", "inet", "filter", "output").Run() != nil {
		script := "add chain inet filter output { type filter hook output priority 0 ; policy accept ; }\n"
		cmd := exec.Command("nft", "-f", "-")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("nft create output chain: %s: %w", string(out), err)
		}
	}

	// 3. Our dedicated chain — nft add chain is a no-op for regular chains.
	if out, err := exec.Command("nft", "add", "chain", "inet", "filter", nftChain).CombinedOutput(); err != nil {
		return fmt.Errorf("nft add %s: %s: %w", nftChain, string(out), err)
	}

	// 4. Flush our chain — we own it, safe to clear on startup.
	if out, err := exec.Command("nft", "flush", "chain", "inet", "filter", nftChain).CombinedOutput(); err != nil {
		return fmt.Errorf("nft flush %s: %s: %w", nftChain, string(out), err)
	}

	// 5. Jump rule — add only if not already present.
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
	mu          sync.Mutex
	agents      map[uint32]*schema.AgentInfo
	hasCmd      map[uint32]bool         // agents with a running systemd unit
	ruleHandles map[uint32][]uint64     // nft rule handles per agent uid
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

// agentUnitName returns the deterministic systemd unit name for an agent.
func agentUnitName(uid uint32) string {
	return fmt.Sprintf("agent-%d-cmd.service", uid)
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
				a.Status = "exited"
				delete(m.hasCmd, info.UID)
			}
		}
		out = append(out, info)
	}
	return out
}

// startProcess launches the agent's command as a transient systemd service
// inside its cgroup slice.  systemd-run --wait blocks until the service
// exits, giving us a reliable lifecycle signal; List() also cross-checks
// the unit state via systemctl so status is authoritative even if the
// wait goroutine hasn't run yet.
func (m *Manager) startProcess(uid uint32, slice string, req schema.SpawnAgentRequest) error {
	unit := fmt.Sprintf("agent-%d-cmd", uid)
	args := []string{
		"--wait",
		"--unit", unit,
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

	m.hasCmd[uid] = true
	go m.waitProcess(uid, cmd)
	return nil
}

// waitProcess blocks until the agent's systemd unit exits, then updates status.
func (m *Manager) waitProcess(uid uint32, cmd *exec.Cmd) {
	cmd.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	if info, ok := m.agents[uid]; ok {
		info.Status = schema.StatusExited
	}
	delete(m.hasCmd, uid)
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

	var handle uint64
	var err error
	uidStr := fmt.Sprintf("%d", uid)

	switch policy {
	case schema.NetAllow:
		return nil // no rules needed
	case schema.NetLocalOnly:
		handle, err = addNftRule("inet", "filter", nftChain,
			"meta", "skuid", uidStr, "oif", "!=", "lo", "drop")
	case schema.NetDeny:
		handle, err = addNftRule("inet", "filter", nftChain,
			"meta", "skuid", uidStr, "drop")
	default:
		return fmt.Errorf("unknown net policy: %s", policy)
	}
	if err != nil {
		return err
	}

	m.ruleHandles[uid] = append(m.ruleHandles[uid], handle)
	return nil
}

func (m *Manager) removeNetRules(uid uint32) error {
	handles := m.ruleHandles[uid]
	delete(m.ruleHandles, uid)

	var firstErr error
	for _, h := range handles {
		if out, err := exec.Command("nft", "delete", "rule", "inet", "filter", nftChain,
			"handle", fmt.Sprintf("%d", h),
		).CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("nft delete handle %d: %s: %w", h, string(out), err)
		}
	}
	return firstErr
}

// addNftRule inserts a rule and returns the kernel-assigned handle.
func addNftRule(args ...string) (uint64, error) {
	cmdArgs := append([]string{"--echo", "--handle", "add", "rule"}, args...)
	out, err := exec.Command("nft", cmdArgs...).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("%s: %w", string(out), err)
	}
	return parseNftHandle(string(out))
}

// parseNftHandle extracts the handle number from nft --echo --handle output.
// Expected format: "... # handle 42\n"
func parseNftHandle(output string) (uint64, error) {
	const prefix = "# handle "
	idx := strings.LastIndex(output, prefix)
	if idx < 0 {
		return 0, fmt.Errorf("no handle in nft output: %q", output)
	}
	s := strings.TrimSpace(output[idx+len(prefix):])
	return strconv.ParseUint(s, 10, 64)
}

func defaultStr(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}
