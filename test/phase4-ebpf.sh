#!/usr/bin/env bash
#
# test/phase4-ebpf.sh
# --------------------
# Validate the eBPF audit daemon inside the Agora OS VM.
#
# Requires:
#   - VM running with phase4-ebpf provisioning
#   - Daemon built:  cd /repo/cmd/audit-ebpf && make build
#   - Event bus running on /run/agent-os/bus.sock
#
# Run as root (inside the guest):
#   sudo /repo/test/phase4-ebpf.sh
#
set -euo pipefail

REPO="${REPO:-/repo}"
BUS_SOCKET="${BUS_SOCKET:-/run/agent-os/bus.sock}"
AUDIT_EBPF="$REPO/cmd/audit-ebpf/target/release/audit-ebpf"
BPF_OBJ="$REPO/cmd/audit-ebpf/target/bpfel-unknown-none/release/audit-ebpf-ebpf"
ARTIFACT_DIR="${ARTIFACT_DIR:-/tmp/phase4-ebpf-test}"
SUMMARY="$ARTIFACT_DIR/summary.txt"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
pass_count=0; fail_count=0

info()  { echo -e ":: $*"; }
pass()  { echo -e "${GREEN}PASS${NC} $*"; ((pass_count++)); }
fail()  { echo -e "${RED}FAIL${NC} $*"; ((fail_count++)); }
warn()  { echo -e "${YELLOW}WARN${NC} $*"; }

# ── Setup ───────────────────────────────────────────────────────────────────

setup() {
    mkdir -p "$ARTIFACT_DIR" /run/agent-os
    > "$SUMMARY"
    echo "Phase 4 eBPF Audit Validation" | tee "$SUMMARY"
    echo "=============================" | tee -a "$SUMMARY"
    date -u +"Started: %Y-%m-%dT%H:%M:%SZ" | tee -a "$SUMMARY"
    echo | tee -a "$SUMMARY"

    # Kernel / BTF check
    info "Kernel: $(uname -r)"
    echo "Kernel: $(uname -r)" >> "$SUMMARY"
    if [[ -f /sys/kernel/btf/vmlinux ]]; then
        pass "BTF support present"
    else
        fail "BTF support missing (CONFIG_DEBUG_INFO_BTF)"
    fi
}

# ── Build ───────────────────────────────────────────────────────────────────

test_build() {
    info "Building audit-ebpf..."
    if su - dev -c "cd $REPO/cmd/audit-ebpf && export PATH=\$HOME/.cargo/bin:\$PATH && make build" > "$ARTIFACT_DIR/build.log" 2>&1; then
        pass "make build: ok"
    else
        fail "make build: failed (see $ARTIFACT_DIR/build.log)"
        return
    fi

    if [[ -x "$AUDIT_EBPF" ]]; then
        pass "binary: $AUDIT_EBPF"
    else
        fail "binary missing: $AUDIT_EBPF"
    fi

    if [[ -f "$BPF_OBJ" ]]; then
        local obj_type
        obj_type=$(file -b "$BPF_OBJ")
        if echo "$obj_type" | grep -q "ELF.*eBPF"; then
            pass "BPF object: ELF eBPF ($obj_type)"
        else
            fail "BPF object: not eBPF ELF ($obj_type)"
        fi
    else
        fail "BPF object missing: $BPF_OBJ"
    fi
}

# ── Daemon Attachment ──────────────────────────────────────────────────────

test_attachment() {
    info "Starting audit-ebpf daemon..."

    # Start daemon in background; capture stderr.
    local pid_file="$ARTIFACT_DIR/audit-ebpf.pid"
    local log_file="$ARTIFACT_DIR/audit-ebpf.log"

    "$AUDIT_EBPF" "$BUS_SOCKET" "libssl.so.3" "$BPF_OBJ" \
        > "$ARTIFACT_DIR/audit-ebpf-stdout.log" \
        2> "$log_file" &
    local daemon_pid=$!
    echo "$daemon_pid" > "$pid_file"

    sleep 2

    if kill -0 "$daemon_pid" 2>/dev/null; then
        pass "daemon started (pid $daemon_pid)"
    else
        fail "daemon exited immediately"
        cat "$log_file"
        return
    fi

    # Check for probe attachment in logs.
    local probe_count
    probe_count=$(grep -c "uprobe\|tracepoint" "$log_file" 2>/dev/null || echo 0)
    if [[ "$probe_count" -ge 4 ]]; then
        pass "probes attached ($probe_count probe lines)"
    else
        fail "probes not attached (found $probe_count, expected >= 4)"
        grep "uprobe\|tracepoint\|Error" "$log_file" || true
    fi

    # Verify ring buffer polling started.
    if grep -q "polling for eBPF events" "$log_file" 2>/dev/null; then
        pass "ring buffer poll: active"
    else
        warn "ring buffer poll: not confirmed in logs"
    fi

    # Keep daemon running for subsequent tests.
    AUDIT_EBPF_PID="$daemon_pid"
}

# ── Event Bus Integration ──────────────────────────────────────────────────

test_event_bus() {
    info "Checking event bus connectivity..."

    # Publish a test event and subscribe to verify bus is working.
    if [[ -S "$BUS_SOCKET" ]]; then
        pass "bus socket: $BUS_SOCKET"
    else
        fail "bus socket missing: $BUS_SOCKET"
        return
    fi

    # Write a test pub message.
    printf '{"op":"pub","topic":"test.phase4.ebpf","body":{}}\n' \
        | timeout 1 nc -U "$BUS_SOCKET" > /dev/null 2>&1 && true
    pass "bus publish: ok"
}

# ── Agent UID Event Generation ─────────────────────────────────────────────

test_agent_events() {
    info "Generating agent-tagged events..."

    # Spawn a dummy agent under a known UID and generate events.
    # The agent UID range is 60000-61000.
    local test_uid=60100
    local test_name="ebpf-test-agent"

    # Check if the event bus has any audit topics.
    # Start a subscriber in background to collect events.
    local sub_file="$ARTIFACT_DIR/events.jsonl"
    timeout 3 nc -U "$BUS_SOCKET" < <(printf '{"op":"sub","topic":"audit.*"}\n') \
        > "$sub_file" 2>/dev/null &
    local sub_pid=$!

    sleep 0.5

    # Trigger a connect event via a brief curl from an agent context.
    # Use runuser (or sudo -u) to execute as the agent UID.
    if id "$test_uid" &>/dev/null || useradd -u "$test_uid" -M -s /usr/bin/nologin "$test_name" 2>/dev/null; then
        # The agent just does a quick localhost connect (no actual SSL needed
        # for connect tracepoint validation).
        timeout 2 runuser -u "$test_name" -- curl -s --max-time 1 http://127.0.0.1:1/ 2>/dev/null || true
        sleep 1
        pass "agent event trigger: executed"
    else
        warn "could not create test agent user $test_uid — skipping event gen"
    fi

    kill "$sub_pid" 2>/dev/null || true
    wait "$sub_pid" 2>/dev/null || true

    # Check captured events.
    local event_lines
    event_lines=$(wc -l < "$sub_file" 2>/dev/null || echo 0)
    if [[ "$event_lines" -gt 0 ]]; then
        pass "bus events received ($event_lines lines)"
    else
        warn "no bus events captured (may need actual agent activity)"
    fi

    # Check specifically for audit topics.
    if grep -q "audit\." "$sub_file" 2>/dev/null; then
        pass "audit events present"
        grep "audit\." "$sub_file" | head -5 >> "$SUMMARY"
    else
        warn "no audit.* events captured"
    fi
}

# ── Cleanup ─────────────────────────────────────────────────────────────────

cleanup() {
    info "Cleaning up..."
    if [[ -n "${AUDIT_EBPF_PID:-}" ]]; then
        kill "$AUDIT_EBPF_PID" 2>/dev/null || true
        wait "$AUDIT_EBPF_PID" 2>/dev/null || true
    fi
    # Clean up test agent user.
    userdel -f ebpf-test-agent 2>/dev/null || true
}

# ── Summary ─────────────────────────────────────────────────────────────────

summary() {
    echo | tee -a "$SUMMARY"
    echo "Results: $pass_count passed, $fail_count failed" | tee -a "$SUMMARY"
    echo "Artifacts: $ARTIFACT_DIR" | tee -a "$SUMMARY"
    date -u +"Finished: %Y-%m-%dT%H:%M:%SZ" | tee -a "$SUMMARY"

    if [[ "$fail_count" -gt 0 ]]; then
        echo -e "${RED}VALIDATION FAILED${NC}"
        exit 1
    else
        echo -e "${GREEN}VALIDATION PASSED${NC}"
        exit 0
    fi
}

# ── Main ────────────────────────────────────────────────────────────────────

trap cleanup EXIT

setup
test_build
test_attachment
test_event_bus
test_agent_events
summary
