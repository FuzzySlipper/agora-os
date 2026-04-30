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
AUDIT_EBPF_PID=

info()  { echo -e ":: $*"; }
pass()  { echo -e "${GREEN}PASS${NC} $*"; pass_count=$((pass_count + 1)); }
fail()  { echo -e "${RED}FAIL${NC} $*"; fail_count=$((fail_count + 1)); }
warn()  { echo -e "${YELLOW}WARN${NC} $*"; }

# ── Setup ───────────────────────────────────────────────────────────────────

setup() {
    mkdir -p "$ARTIFACT_DIR" /run/agent-os
    > "$SUMMARY"
    echo "Phase 4 eBPF Audit Validation" | tee "$SUMMARY"
    echo "=============================" | tee -a "$SUMMARY"
    date -u +"Started: %Y-%m-%dT%H:%M:%SZ" | tee -a "$SUMMARY"
    echo | tee -a "$SUMMARY"

    # Kernel version check.
    local kver
    kver=$(uname -r)
    echo "Kernel: $kver" >> "$SUMMARY"
    local k_major k_minor
    k_major=$(echo "$kver" | cut -d. -f1)
    k_minor=$(echo "$kver" | cut -d. -f2)
    if [[ "$k_major" -gt 5 ]] || { [[ "$k_major" -eq 5 ]] && [[ "$k_minor" -ge 6 ]]; }; then
        pass "kernel >= 5.6 ($kver)"
    else
        fail "kernel too old ($kver < 5.6)"
    fi

    if [[ -f /sys/kernel/btf/vmlinux ]]; then
        pass "BTF support: present"
    else
        fail "BTF support: missing (CONFIG_DEBUG_INFO_BTF)"
    fi

    # Check required tooling.
    if command -v nc &>/dev/null; then
        pass "nc: available"
    else
        fail "nc: not installed (infrastructure: install gnu-netcat)"
    fi
}

# ── Build ───────────────────────────────────────────────────────────────────

test_build() {
    info "Building audit-ebpf..."
    local build_log="$ARTIFACT_DIR/build.log"
    if su - dev -c "export PATH=\$HOME/.cargo/bin:\$PATH && cd $REPO/cmd/audit-ebpf && make build" > "$build_log" 2>&1; then
        pass "make build: ok"
    else
        fail "make build: failed (infrastructure: see $build_log)"
        return 1
    fi

    if [[ -x "$AUDIT_EBPF" ]]; then
        pass "binary: exists"
    else
        fail "binary missing: $AUDIT_EBPF (infrastructure)"
        return 1
    fi

    local obj_type
    if obj_type=$(file -b "$BPF_OBJ" 2>/dev/null); then
        if echo "$obj_type" | grep -q "ELF.*eBPF"; then
            pass "BPF object: ELF eBPF"
        else
            fail "BPF object: wrong format ($obj_type)"
            return 1
        fi
    else
        fail "BPF object: missing ($BPF_OBJ)"
        return 1
    fi
}

# ── Bus Connectivity ────────────────────────────────────────────────────────

test_bus() {
    info "Checking event bus..."
    if [[ -S "$BUS_SOCKET" ]]; then
        pass "bus socket: $BUS_SOCKET"
    else
        fail "bus socket missing: $BUS_SOCKET (infrastructure)"
        return 1
    fi

    # Publish a test event and verify echo-back via separate subscriber.
    # The bus broker suppresses echo to the publishing connection, so we
    # use two connections: one subscriber, one publisher.
    local bus_pub_out="$ARTIFACT_DIR/bus-pub-out.log"
    local bus_sub_out="$ARTIFACT_DIR/bus-sub-out.log"
    local rc=0

    # Start subscriber on a background nc that stays open.
    # Using tail -f /dev/null keeps stdin open so nc does not exit.
    (printf '{"op":"sub","topic":"test.phase4.echo"}\n'; tail -f /dev/null) \
        | timeout 3 nc -U "$BUS_SOCKET" \
        > "$bus_sub_out" 2>/dev/null &
    local sub_pid=$!
    sleep 0.3

    # Publish from a separate connection.
    printf '{"op":"pub","topic":"test.phase4.echo","body":{"msg":"ping"}}\n' \
        | timeout 1 nc -U "$BUS_SOCKET" > /dev/null 2>/dev/null || true

    sleep 0.5
    kill "$sub_pid" 2>/dev/null || true
    wait "$sub_pid" 2>/dev/null || true

    if grep -q '"msg":"ping"' "$bus_sub_out" 2>/dev/null; then
        pass "bus pub/sub: echo received"
    else
        fail "bus pub/sub: echo not received (daemon: bus may be down)"
        return 1
    fi
}

# ── Daemon Attachment ──────────────────────────────────────────────────────

test_attachment() {
    info "Starting audit-ebpf daemon..."

    local pid_file="$ARTIFACT_DIR/audit-ebpf.pid"
    local log_file="$ARTIFACT_DIR/audit-ebpf.log"

    "$AUDIT_EBPF" "$BUS_SOCKET" "libssl.so.3" "$BPF_OBJ" \
        1> "$ARTIFACT_DIR/audit-ebpf-stdout.log" \
        2> "$log_file" &
    AUDIT_EBPF_PID=$!
    echo "$AUDIT_EBPF_PID" > "$pid_file"

    sleep 2

    if kill -0 "$AUDIT_EBPF_PID" 2>/dev/null; then
        pass "daemon: running (pid $AUDIT_EBPF_PID)"
    else
        fail "daemon: exited (infrastructure: see $log_file)"
        grep "Error\|error\|FAIL" "$log_file" 2>/dev/null | head -5 >> "$SUMMARY" || true
        return 1
    fi

    local probe_count
    probe_count=$(grep -c "uprobe\|tracepoint" "$log_file" 2>/dev/null || echo 0)
    if [[ "$probe_count" -ge 4 ]]; then
        pass "probes: $probe_count attached"
    else
        fail "probes: only $probe_count (expected >= 4; daemon bug)"
        grep "uprobe\|tracepoint\|Error" "$log_file" 2>/dev/null | head -5 >> "$SUMMARY" || true
    fi

    if grep -q "polling for eBPF events" "$log_file" 2>/dev/null; then
        pass "ring buffer: polling active"
    else
        fail "ring buffer: poll not confirmed (daemon bug)"
    fi
}

# ── Agent Event Capture ─────────────────────────────────────────────────────

test_event_capture() {
    info "Generating agent events and capturing audit output..."

    local events_file="$ARTIFACT_DIR/events.jsonl"

    # Subscribe to the three-segment audit topics the daemon publishes.
    # Bus pattern "*" matches exactly one dot-separated segment.
    #   audit.file.open      ->  audit.*.* catches it
    #   audit.process.exec   ->  audit.*.* catches it
    #   audit.net.connect    ->  audit.*.* catches it
    #   audit.net.ssl_read   ->  audit.*.* catches it
    #   audit.net.ssl_write  ->  audit.*.* catches it
    local sub_pattern="audit.*.*"
    info "Subscribing to $sub_pattern"

    # Start subscriber in background.
    # Using tail -f /dev/null keeps stdin open so nc does not exit before events arrive.
    (printf '{"op":"sub","topic":"%s"}\n' "$sub_pattern"; tail -f /dev/null) \
        | timeout 4 nc -U "$BUS_SOCKET" \
        > "$events_file" 2>/dev/null &
    local sub_pid=$!
    sleep 0.5

    # Trigger a connect() syscall from an agent UID to generate audit.net.connect.
    local test_uid=60100
    local test_name="ebpf-test-agent"

    if ! id "$test_uid" &>/dev/null; then
        useradd -u "$test_uid" -M -s /usr/bin/nologin "$test_name" 2>/dev/null || true
    fi

    # Run a quick TCP connect from the agent UID. The eBPF connect tracepoint
    # fires on any connect() syscall. Destination doesn't need to be reachable.
    timeout 2 runuser -u "$test_name" -- curl -s --max-time 1 http://127.0.0.1:1/ 2>/dev/null || true
    sleep 1.5

    wait "$sub_pid" 2>/dev/null || true

    local event_lines
    event_lines=$(wc -l < "$events_file" 2>/dev/null || echo 0)

    if [[ "$event_lines" -gt 0 ]]; then
        pass "bus events: $event_lines received"

        # Check for audit.*.* events specifically.
        if grep -q '"audit\.' "$events_file" 2>/dev/null; then
            local audit_count
            audit_count=$(grep -c '"audit\.' "$events_file" 2>/dev/null || echo 0)
            pass "audit events: $audit_count captured"
            grep '"audit\.' "$events_file" | head -5 >> "$SUMMARY"
        else
            fail "audit events: none detected (daemon bug or no agent activity)"
        fi
    else
        fail "bus events: none received (daemon bug or bus down)"
    fi

    # Clean up test user.
    userdel -f "$test_name" 2>/dev/null || true
}

# ── Cleanup ─────────────────────────────────────────────────────────────────

cleanup() {
    info "Cleaning up..."
    if [[ -n "${AUDIT_EBPF_PID:-}" ]]; then
        kill "$AUDIT_EBPF_PID" 2>/dev/null || true
        wait "$AUDIT_EBPF_PID" 2>/dev/null || true
    fi
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
test_build || true
test_bus || true
test_attachment || true
test_event_capture || true
summary
