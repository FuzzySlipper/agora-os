package agent

import (
	"fmt"
	"os/exec"

	"github.com/patch/agora-os/internal/schema"
)

// agentUnitName returns the deterministic systemd unit name for an agent.
func agentUnitName(uid uint32) string {
	return fmt.Sprintf("agent-%d-cmd.service", uid)
}

// startProcess launches the agent's command as a transient systemd service
// inside its cgroup slice. systemd-run --wait blocks until the service
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
