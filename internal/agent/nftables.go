package agent

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

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
