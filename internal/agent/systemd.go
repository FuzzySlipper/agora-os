package agent

import (
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

// agentUnitName returns the deterministic systemd unit name for an agent.
func agentUnitName(uid uint32) string {
	return fmt.Sprintf("agent-%d-cmd.service", uid)
}

// startProcess launches the agent's command as a transient systemd service
// inside its cgroup slice. We start the unit without --wait so startProcess
// can surface immediate launch failures (for example stale transient units)
// instead of reporting success as soon as the systemd-run helper process
// starts. A background poller updates the cached status when the unit exits.
func (m *Manager) startProcess(uid uint32, username string, slice string, req schema.SpawnAgentRequest) error {
	unit := fmt.Sprintf("agent-%d-cmd", uid)
	// Best-effort cleanup for stale transient units left behind after an
	// isolation-service restart. Reusing the deterministic unit name should
	// not fail just because a prior failed invocation is still loaded.
	_ = exec.Command("systemctl", "stop", unit+".service").Run()
	_ = exec.Command("systemctl", "reset-failed", unit+".service").Run()

	args := []string{
		"--unit", unit,
		"--slice", slice,
		"--uid", username,
		"--gid", username,
		"--property", fmt.Sprintf("MemoryMax=%s", defaultStr(req.MemoryMax, "512M")),
		"--property", fmt.Sprintf("CPUQuota=%s", defaultStr(req.CPUQuota, "50%")),
	}
	if len(req.Env) > 0 {
		keys := make([]string, 0, len(req.Env))
		for key := range req.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			args = append(args, "--setenv", fmt.Sprintf("%s=%s", key, req.Env[key]))
		}
	}
	args = append(args, "--")
	args = append(args, req.Command...)

	if err := exec.Command("systemd-run", args...).Run(); err != nil {
		return err
	}

	m.hasCmd[uid] = true
	go m.waitProcess(uid)
	return nil
}

// waitProcess polls the agent's transient unit until it stops reporting active,
// then updates the cached status.
func (m *Manager) waitProcess(uid uint32) {
	unit := agentUnitName(uid)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		if exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil {
			continue
		}

		m.mu.Lock()
		defer m.mu.Unlock()

		if info, ok := m.agents[uid]; ok {
			info.Status = schema.StatusExited
		}
		delete(m.hasCmd, uid)
		return
	}
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
