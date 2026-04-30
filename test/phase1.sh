#!/usr/bin/env bash
#
# phase1.sh
# ---------
# Phase 1 end-to-end integration test.
#
# Exercises the full Phase 1 scenario in the disposable VM:
#   1. Spawn an agent with resource limits and a command
#   2. Verify cgroup limits are enforced
#   3. Verify network access is blocked by nftables
#   4. Verify file I/O is captured in the audit log
#   5. Submit escalation request and verify audit trail
#   6. Terminate agent and verify full cleanup
#
# Requires: root, python3, nft, systemd, go toolchain
#
# Run inside the disposable VM:
#   cd /repo
#   sudo test/phase1.sh
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RUNTIME_DIR="/run/agent-os"
LOG_DIR="/var/log/agent-os"
ADMIN_LOG="$LOG_DIR/admin-agent.log"
AUDIT_LOG="$LOG_DIR/audit.log"
AGENT_HOME_BASE="/var/lib/agents"
BIN_DIR="$(mktemp -d /tmp/phase1-e2e.XXXXXX)"
PASS=0
FAIL=0

# Service PIDs
ISOLATION_PID=""
ADMIN_PID=""
AUDIT_PID=""

# Test state
SPAWNED_UID=""
AGENT_USER=""
RESTART_UID=""
RESTART_USER=""
STALE_UID=""
STALE_USER=""
TERM9_UID=""
TERM9_USER=""
WRITE_PID=""
LISTENER_PID=""
SPINNER_UNIT=""

note() { echo ":: $*"; }

pass() {
    echo "  PASS: $*"
    ((PASS++)) || true
}

fail() {
    echo "  FAIL: $*"
    ((FAIL++)) || true
}

require_root() {
    [[ ${EUID} -eq 0 ]] || { echo "error: run as root inside the VM" >&2; exit 1; }
}

# Send JSON to a Unix socket as the current user.
sock_send() {
    python3 -c "
import socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sys.argv[1])
s.sendall(sys.argv[2].encode() + b'\n')
s.shutdown(socket.SHUT_WR)
data = b''
while True:
    chunk = s.recv(4096)
    if not chunk: break
    data += chunk
s.close()
sys.stdout.write(data.decode())
" "$1" "$2"
}

# Send JSON to a Unix socket as a different user (kernel identity changes via runuser).
sock_send_as() {
    runuser -u "$1" -- python3 -c "
import socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sys.argv[1])
s.sendall(sys.argv[2].encode() + b'\n')
s.shutdown(socket.SHUT_WR)
data = b''
while True:
    chunk = s.recv(4096)
    if not chunk: break
    data += chunk
s.close()
sys.stdout.write(data.decode())
" "$2" "$3"
}

# Parse a value from JSON on stdin.  Argument is a Python expression suffix
# applied to the parsed object, e.g. "['ok']" or "['body']['agents']".
json_get() {
    python3 -c "import json,sys; print(json.loads(sys.stdin.read())$1)"
}

# Parse the response body, handling both raw object and string-encoded JSON.
resp_body_get() {
    python3 -c "
import json, sys
r = json.loads(sys.stdin.read())
body = json.loads(r['body']) if isinstance(r['body'], str) else r['body']
print(body$1)
"
}

cleanup() {
    set +e

    # Kill background helpers.
    [[ -n "$WRITE_PID" ]]    && kill "$WRITE_PID" 2>/dev/null && wait "$WRITE_PID" 2>/dev/null
    [[ -n "$LISTENER_PID" ]] && kill "$LISTENER_PID" 2>/dev/null && wait "$LISTENER_PID" 2>/dev/null
    [[ -n "$SPINNER_UNIT" ]] && systemctl stop "${SPINNER_UNIT}.service" 2>/dev/null

    # Clean up spawned agents.
    for _uidvar in SPAWNED_UID RESTART_UID STALE_UID TERM9_UID; do
        eval _uid="\$$_uidvar"
        case "$_uidvar" in
            SPAWNED_UID)  _user="$AGENT_USER" ;;
            RESTART_UID)  _user="$RESTART_USER" ;;
            STALE_UID)    _user="$STALE_USER" ;;
            TERM9_UID)    _user="$TERM9_USER" ;;
            *)            _user="" ;;
        esac
        if [[ -n "$_uid" ]]; then
            if [[ -S "$RUNTIME_DIR/isolation.sock" ]]; then
                sock_send "$RUNTIME_DIR/isolation.sock" \
                    "{\"method\":\"terminate_agent\",\"body\":{\"uid\":$_uid}}" \
                    >/dev/null 2>&1
            fi
            pkill -U "$_uid" 2>/dev/null
            systemctl stop "agent-${_uid}-cmd.service" 2>/dev/null
            systemctl stop "agent-${_uid}.slice" 2>/dev/null
            [[ -n "$_user" ]] && userdel -r "$_user" 2>/dev/null
        fi
    done

    # Stop services.
    [[ -n "$AUDIT_PID" ]]     && kill "$AUDIT_PID" 2>/dev/null
    [[ -n "$ISOLATION_PID" ]] && kill "$ISOLATION_PID" 2>/dev/null
    [[ -n "$ADMIN_PID" ]]     && kill "$ADMIN_PID" 2>/dev/null
    [[ -n "$AUDIT_PID" ]]     && wait "$AUDIT_PID" 2>/dev/null
    [[ -n "$ISOLATION_PID" ]] && wait "$ISOLATION_PID" 2>/dev/null
    [[ -n "$ADMIN_PID" ]]     && wait "$ADMIN_PID" 2>/dev/null

    rm -f "$RUNTIME_DIR"/{isolation.sock,admin-agent.sock,audit.sock}
    rm -rf "$BIN_DIR"
}


# ---- Prerequisites ----

require_root
command -v python3 >/dev/null || { echo "error: python3 required" >&2; exit 1; }
command -v nft     >/dev/null || { echo "error: nft required"     >&2; exit 1; }

trap cleanup EXIT


# ---- Setup ----

note "building services"
(
    cd "$ROOT_DIR"
    go build -o "$BIN_DIR/isolation-service" ./cmd/isolation-service
    go build -o "$BIN_DIR/admin-agent"       ./cmd/admin-agent
    go build -o "$BIN_DIR/audit-service"     ./cmd/audit-service
)

mkdir -p "$RUNTIME_DIR" "$LOG_DIR" "$AGENT_HOME_BASE"
rm -f "$RUNTIME_DIR"/{isolation.sock,admin-agent.sock,audit.sock}
: > "$ADMIN_LOG"
: > "$AUDIT_LOG"

note "starting services"

# Audit service — watches agent home directories for file events.
"$BIN_DIR/audit-service" "$AGENT_HOME_BASE" >/tmp/audit-service.log 2>&1 &
AUDIT_PID=$!

# Admin agent — LLM endpoint is unreachable so evaluate() always returns
# decision=escalate.  This proves the audit trail without needing a real API key.
ADMIN_AGENT_API_URL="http://127.0.0.1:1/v1/messages" \
ADMIN_AGENT_PROMPT="$ROOT_DIR/config/admin-agent-system-prompt.md" \
    "$BIN_DIR/admin-agent" >/tmp/admin-agent.log 2>&1 &
ADMIN_PID=$!

# Isolation service — bootstraps nftables and manages agents.
"$BIN_DIR/isolation-service" >/tmp/isolation-service.log 2>&1 &
ISOLATION_PID=$!

# Wait for all sockets to appear.
for _ in $(seq 1 40); do
    [[ -S "$RUNTIME_DIR/isolation.sock" && \
       -S "$RUNTIME_DIR/admin-agent.sock" && \
       -S "$RUNTIME_DIR/audit.sock" ]] && break
    sleep 0.25
done

for sock in isolation.sock admin-agent.sock audit.sock; do
    [[ -S "$RUNTIME_DIR/$sock" ]] || { echo "error: $sock not created" >&2; exit 1; }
done

note "all services ready"


# ==== Test 1: Spawn agent with resource limits ====

echo
note "test 1: spawn agent (cpu=50%, mem=256M, net=deny, cmd=sleep)"

SPAWN_REQ='{"method":"spawn_agent","body":{"name":"e2e","cpu_quota":"50%","memory_max":"256M","net_access":"deny","command":["sleep","120"]}}'
SPAWN_RESP=$(sock_send "$RUNTIME_DIR/isolation.sock" "$SPAWN_REQ")

SPAWN_OK=$(echo "$SPAWN_RESP" | json_get "['ok']")
if [[ "$SPAWN_OK" != "True" ]]; then
    SPAWN_ERR=$(echo "$SPAWN_RESP" | json_get "['body']" 2>/dev/null || echo "unknown")
    fail "spawn_agent failed: $SPAWN_ERR"
    echo "cannot continue without a spawned agent"
    exit 1
fi

SPAWNED_UID=$(echo "$SPAWN_RESP" | resp_body_get "['agent']['uid']")
AGENT_USER="agent-e2e-${SPAWNED_UID}"
AGENT_HOME="$AGENT_HOME_BASE/$AGENT_USER"
note "spawned agent: uid=$SPAWNED_UID user=$AGENT_USER"

# Verify user exists.
if id "$AGENT_USER" &>/dev/null; then
    pass "agent user $AGENT_USER created"
else
    fail "agent user $AGENT_USER not found"
fi

# Wait for systemd unit to become active.
UNIT="agent-${SPAWNED_UID}-cmd.service"
for _ in $(seq 1 20); do
    systemctl is-active --quiet "$UNIT" 2>/dev/null && break
    sleep 0.25
done

if systemctl is-active --quiet "$UNIT" 2>/dev/null; then
    pass "systemd unit $UNIT is active"
else
    fail "systemd unit $UNIT not active"
fi


# ==== Test 2: Cgroup limits enforced ====

echo
note "test 2: cgroup resource limits — configuration and enforcement"

CGROUP=$(systemctl show "$UNIT" -p ControlGroup --value 2>/dev/null)

if [[ -n "$CGROUP" && -d "/sys/fs/cgroup${CGROUP}" ]]; then
    # 2a. Configuration: verify cgroupfs values match what was requested.

    # CPU: 50% = 50000 usec per 100000 usec period.
    CPU_MAX=$(cat "/sys/fs/cgroup${CGROUP}/cpu.max" 2>/dev/null || echo "")
    CPU_QUOTA=$(echo "$CPU_MAX" | awk '{print $1}')
    if [[ "$CPU_QUOTA" == "50000" ]]; then
        pass "cpu.max quota = 50000 usec (50%)"
    else
        fail "expected cpu.max quota 50000, got '$CPU_QUOTA' (raw: $CPU_MAX)"
    fi

    # Memory: 256M = 256 * 1024 * 1024 = 268435456 bytes.
    MEM_MAX=$(cat "/sys/fs/cgroup${CGROUP}/memory.max" 2>/dev/null || echo "")
    if [[ "$MEM_MAX" == "268435456" ]]; then
        pass "memory.max = 268435456 (256M)"
    else
        fail "expected memory.max 268435456, got '$MEM_MAX'"
    fi
else
    fail "cgroup path not found for $UNIT (ControlGroup='$CGROUP')"
    fail "(skipping memory.max check)"
fi

# 2b. Enforcement — CPU: run a busy loop under the same limits and verify
#     the kernel actually throttled it (nr_throttled > 0 in cpu.stat).

SPINNER_UNIT="agent-${SPAWNED_UID}-spin"
systemd-run --unit="$SPINNER_UNIT" \
    --slice="agent-${SPAWNED_UID}.slice" \
    --uid="$SPAWNED_UID" --gid="$SPAWNED_UID" \
    --property=CPUQuota=50% \
    -- sh -c 'while true; do :; done' 2>/dev/null

for _ in $(seq 1 10); do
    systemctl is-active --quiet "${SPINNER_UNIT}.service" 2>/dev/null && break
    sleep 0.25
done
# Let the spinner run long enough to accumulate measurable throttling.
sleep 2

SPIN_CGROUP=$(systemctl show "${SPINNER_UNIT}.service" -p ControlGroup --value 2>/dev/null)
if [[ -n "$SPIN_CGROUP" && -f "/sys/fs/cgroup${SPIN_CGROUP}/cpu.stat" ]]; then
    NR_THROTTLED=$(awk '/^nr_throttled/ {print $2}' "/sys/fs/cgroup${SPIN_CGROUP}/cpu.stat")
    if [[ "$NR_THROTTLED" -gt 0 ]] 2>/dev/null; then
        pass "cpu enforcement: kernel throttled spinner (nr_throttled=$NR_THROTTLED)"
    else
        fail "cpu spinner was not throttled (nr_throttled=$NR_THROTTLED)"
    fi
else
    fail "cpu.stat not readable for spinner (cgroup='$SPIN_CGROUP')"
fi
systemctl stop "${SPINNER_UNIT}.service" 2>/dev/null
SPINNER_UNIT=""

# 2c. Enforcement — memory: attempt to allocate 300M with a 256M limit.
#     The cgroup OOM killer should terminate the process.

MEM_UNIT="agent-${SPAWNED_UID}-memproof"
if systemd-run --quiet --unit="$MEM_UNIT" \
    --slice="agent-${SPAWNED_UID}.slice" \
    --uid="$SPAWNED_UID" --gid="$SPAWNED_UID" \
    --property=MemoryMax=256M \
    --wait \
    -- python3 -c "x = bytearray(300 * 1024 * 1024)" 2>/dev/null; then
    fail "process survived allocating 300M with 256M limit"
else
    pass "memory enforcement: allocation beyond 256M was killed"
fi


# ==== Test 3: Network access blocked ====

echo
note "test 3: outbound network blocked by nftables"

# Use a local TCP listener so the proof is deterministic — independent of
# external network availability.  Root proves the listener is reachable,
# then the agent uid proves the nft "meta skuid <uid> drop" rule blocks it.

NET_PORT=19876
python3 -c "
import socket, signal, sys
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', int(sys.argv[1])))
s.listen(5)
while True:
    try:
        conn, _ = s.accept()
        conn.close()
    except:
        break
" "$NET_PORT" &
LISTENER_PID=$!
sleep 0.3

# Control: root can reach the listener (proves it's up).
if python3 -c "
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(3)
s.connect(('127.0.0.1', int(sys.argv[1])))
s.close()
" "$NET_PORT" 2>/dev/null; then
    pass "control: root connects to local listener"
else
    fail "control: root cannot connect to local listener (test is invalid)"
fi

# Agent uid should be blocked by the nft drop rule.
if runuser -u "$AGENT_USER" -- python3 -c "
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(3)
s.connect(('127.0.0.1', int(sys.argv[1])))
s.close()
sys.exit(0)
" "$NET_PORT" 2>/dev/null; then
    fail "agent uid connected to local listener (nft rule not enforced)"
else
    pass "agent uid blocked from local listener by nftables"
fi

kill "$LISTENER_PID" 2>/dev/null && wait "$LISTENER_PID" 2>/dev/null
LISTENER_PID=""

# Verify the nft rule references this uid.
NFT_RULES=$(nft list chain inet filter agent-os-output 2>/dev/null || echo "")
if echo "$NFT_RULES" | grep -q "skuid $SPAWNED_UID"; then
    pass "nft drop rule present for uid $SPAWNED_UID"
else
    fail "nft drop rule for uid $SPAWNED_UID not found in agent-os-output"
fi


# ==== Test 4: File I/O captured in audit log ====

echo
note "test 4: file write attributed to agent uid in audit log"

PROOF_FILE="$AGENT_HOME/audit-proof.txt"

# Write a file as the agent uid.  The trailing sleep keeps the process alive
# long enough for the audit service to look up the UID from /proc/<pid>/status
# before the process exits and the procfs entry disappears.
runuser -u "$AGENT_USER" -- sh -c "echo 'phase1 e2e proof' > '$PROOF_FILE'; sleep 3" &
WRITE_PID=$!

# Poll for the audit event.
AUDIT_FOUND=false
for _ in $(seq 1 20); do
    if grep -q "\"agent_uid\":$SPAWNED_UID" "$AUDIT_LOG" 2>/dev/null; then
        AUDIT_FOUND=true
        break
    fi
    sleep 0.25
done
wait "$WRITE_PID" 2>/dev/null || true
WRITE_PID=""

if $AUDIT_FOUND; then
    pass "audit log contains event for agent uid=$SPAWNED_UID"
else
    fail "no audit event for uid=$SPAWNED_UID in $AUDIT_LOG"
    # Dump service log for debugging.
    echo "    audit service stderr:" >&2
    tail -5 /tmp/audit-service.log 2>/dev/null | sed 's/^/      /' >&2
fi


# ==== Test 5: Escalation request and audit trail ====

echo
note "test 5: escalation request logged with kernel-verified uid"

ESC_REQ="{\"method\":\"escalate\",\"body\":{\"agent_uid\":$SPAWNED_UID,\"task_context\":\"e2e-test\",\"requested_action\":\"write\",\"requested_resource\":\"/etc/hosts\",\"justification\":\"testing escalation audit trail\"}}"

# Send from the agent's uid so SO_PEERCRED attributes correctly.
sock_send_as "$AGENT_USER" "$RUNTIME_DIR/admin-agent.sock" "$ESC_REQ" >/dev/null

sleep 0.5

if [[ -s "$ADMIN_LOG" ]]; then
    # Check the last log entry has the correct agent uid and decision.
    LOGGED_UID=$(tail -1 "$ADMIN_LOG" | json_get "['request']['agent_uid']")
    LOGGED_DECISION=$(tail -1 "$ADMIN_LOG" | json_get "['response']['decision']")

    if [[ "$LOGGED_UID" == "$SPAWNED_UID" ]]; then
        pass "escalation log records kernel-verified uid=$SPAWNED_UID"
    else
        fail "escalation log has agent_uid=$LOGGED_UID, expected $SPAWNED_UID"
    fi

    if [[ "$LOGGED_DECISION" == "escalate" ]]; then
        pass "decision=escalate (safe default on unreachable LLM)"
    else
        fail "expected decision=escalate, got '$LOGGED_DECISION'"
    fi
else
    fail "admin-agent.log is empty after escalation request"
    fail "(skipping decision check)"
fi


# ==== Test 6: Terminate and verify cleanup ====

echo
note "test 6: terminate agent and verify full cleanup"

TERM_REQ="{\"method\":\"terminate_agent\",\"body\":{\"uid\":$SPAWNED_UID}}"
TERM_RESP=$(sock_send "$RUNTIME_DIR/isolation.sock" "$TERM_REQ")
TERM_OK=$(echo "$TERM_RESP" | json_get "['ok']")

if [[ "$TERM_OK" == "True" ]]; then
    pass "terminate_agent returned ok"
else
    TERM_ERR=$(echo "$TERM_RESP" | json_get "['body']" 2>/dev/null || echo "unknown")
    fail "terminate_agent failed: $TERM_ERR"
fi

# Give cleanup a moment to complete.
sleep 0.5

# User should be removed.
if id "$AGENT_USER" &>/dev/null; then
    fail "agent user $AGENT_USER still exists after terminate"
else
    pass "agent user $AGENT_USER removed"
fi

# Systemd unit should be gone.
if systemctl is-active --quiet "$UNIT" 2>/dev/null; then
    fail "systemd unit $UNIT still active after terminate"
else
    pass "systemd unit $UNIT stopped"
fi

# Agent slice should be gone/stopped too.
SLICE="agent-${SPAWNED_UID}.slice"
if systemctl is-active --quiet "$SLICE" 2>/dev/null; then
    fail "agent slice $SLICE still active after terminate"
else
    pass "agent slice $SLICE stopped"
fi

# Nftables rules for this uid should be gone.
NFT_AFTER=$(nft list chain inet filter agent-os-output 2>/dev/null || echo "")
if echo "$NFT_AFTER" | grep -q "skuid $SPAWNED_UID"; then
    fail "nft rules for uid $SPAWNED_UID still present after terminate"
else
    pass "nft rules for uid $SPAWNED_UID removed"
fi

# Agent should not appear in list_agents.
LIST_RESP=$(sock_send "$RUNTIME_DIR/isolation.sock" '{"method":"list_agents","body":null}')
LIST_HAS=$(echo "$LIST_RESP" | python3 -c "
import json, sys
r = json.loads(sys.stdin.read())
body = json.loads(r['body']) if isinstance(r['body'], str) else r['body']
uids = [a['uid'] for a in body.get('agents', [])]
print('yes' if $SPAWNED_UID in uids else 'no')
")

if [[ "$LIST_HAS" == "no" ]]; then
    pass "agent uid=$SPAWNED_UID absent from list_agents"
else
    fail "agent uid=$SPAWNED_UID still in list_agents after terminate"
fi

# Home directory should be removed by userdel --remove.
if [[ -d "$AGENT_HOME" ]]; then
    fail "agent home $AGENT_HOME still exists after terminate"
else
    pass "agent home directory removed"
fi

# Mark as cleaned up so the trap doesn't retry.
SPAWNED_UID=""


# ==== Test 7: Service restart preserves agent network isolation ====

echo
note "test 7: service restart preserves agent network isolation"

# Spawn a fresh agent with a long-running command.
SPAWN_REQ7='{"method":"spawn_agent","body":{"name":"restart","cpu_quota":"50%","memory_max":"256M","net_access":"deny","command":["sleep","120"]}}'
SPAWN_RESP7=$(sock_send "$RUNTIME_DIR/isolation.sock" "$SPAWN_REQ7")
SPAWN_OK7=$(echo "$SPAWN_RESP7" | json_get "['ok']")
if [[ "$SPAWN_OK7" != "True" ]]; then
    fail "test 7: spawn_agent failed"
else
    RESTART_UID=$(echo "$SPAWN_RESP7" | resp_body_get "['agent']['uid']")
    RESTART_USER="agent-restart-${RESTART_UID}"
    note "test 7: spawned agent uid=$RESTART_UID"

    # Verify nft rule exists.
    NFT_BEFORE=$(nft list chain inet filter agent-os-output 2>/dev/null || echo "")
    if echo "$NFT_BEFORE" | grep -q "skuid $RESTART_UID"; then
        pass "test 7: nft rule present before restart"
    else
        fail "test 7: nft rule missing before restart"
    fi

    # Kill the isolation service.
    kill "$ISOLATION_PID" 2>/dev/null
    wait "$ISOLATION_PID" 2>/dev/null || true
    ISOLATION_PID=""
    sleep 0.5

    # Restart the isolation service.
    "$BIN_DIR/isolation-service" >/tmp/isolation-service-restart.log 2>&1 &
    ISOLATION_PID=$!

    # Wait for socket to reappear.
    for _ in $(seq 1 20); do
        [[ -S "$RUNTIME_DIR/isolation.sock" ]] && break
        sleep 0.25
    done

    if [[ ! -S "$RUNTIME_DIR/isolation.sock" ]]; then
        fail "test 7: isolation socket not created after restart"
    else
        # Verify agent is still tracked.
        LIST_RESP7=$(sock_send "$RUNTIME_DIR/isolation.sock" '{"method":"list_agents","body":null}')
        LIST_HAS7=$(echo "$LIST_RESP7" | python3 -c "
import json, sys
r = json.loads(sys.stdin.read())
body = json.loads(r['body']) if isinstance(r['body'], str) else r['body']
uids = [a['uid'] for a in body.get('agents', [])]
print('yes' if $RESTART_UID in uids else 'no')
")

        if [[ "$LIST_HAS7" == "yes" ]]; then
            pass "test 7: agent uid=$RESTART_UID re-adopted after restart"
        else
            fail "test 7: agent uid=$RESTART_UID missing from list_agents after restart"
        fi

        # Verify nft rules are rebuilt.
        NFT_AFTER_RESTART=$(nft list chain inet filter agent-os-output 2>/dev/null || echo "")
        if echo "$NFT_AFTER_RESTART" | grep -q "skuid $RESTART_UID"; then
            pass "test 7: nft rule rebuilt after restart"
        else
            fail "test 7: nft rule missing after restart (security gap!)"
        fi

        # Verify agent cannot reach network.
        NET_PORT2=19877
        python3 -c "
import socket, signal, sys
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', int(sys.argv[1])))
s.listen(5)
while True:
    try:
        conn, _ = s.accept()
        conn.close()
    except:
        break
" "$NET_PORT2" &
        LISTENER_PID=$!
        sleep 0.3

        if runuser -u "$RESTART_USER" -- python3 -c "
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(3)
s.connect(('127.0.0.1', int(sys.argv[1])))
s.close()
sys.exit(0)
" "$NET_PORT2" 2>/dev/null; then
            fail "test 7: agent uid connected after restart (nft rule not enforced)"
        else
            pass "test 7: agent uid blocked from network after restart"
        fi

        kill "$LISTENER_PID" 2>/dev/null && wait "$LISTENER_PID" 2>/dev/null
        LISTENER_PID=""
    fi

    # Clean up the restart-test agent.
    TERM_REQ7="{\"method\":\"terminate_agent\",\"body\":{\"uid\":$RESTART_UID}}"
    sock_send "$RUNTIME_DIR/isolation.sock" "$TERM_REQ7" >/dev/null 2>&1
    pkill -U "$RESTART_UID" 2>/dev/null
    userdel -r "$RESTART_USER" 2>/dev/null
    # Clear so cleanup() trap doesn't re-clean.
    RESTART_UID=""
    RESTART_USER=""
fi


# ==== Test 8: Stale agent user (exited process) gets NetDeny after restart ====

echo
note "test 8: stale agent user with no running process gets NetDeny after restart"

# Spawn an agent with a short-lived command so the process exits on its own.
SPAWN_REQ8='{"method":"spawn_agent","body":{"name":"stale","cpu_quota":"50%","memory_max":"256M","net_access":"deny","command":["true"]}}'
SPAWN_RESP8=$(sock_send "$RUNTIME_DIR/isolation.sock" "$SPAWN_REQ8")
SPAWN_OK8=$(echo "$SPAWN_RESP8" | json_get "['ok']")
if [[ "$SPAWN_OK8" != "True" ]]; then
    fail "test 8: spawn_agent failed"
else
    STALE_UID=$(echo "$SPAWN_RESP8" | resp_body_get "['agent']['uid']")
    STALE_USER="agent-stale-${STALE_UID}"
    note "test 8: spawned short-lived agent uid=$STALE_UID"

    # Wait for the command to finish (true exits immediately).
    sleep 1

    # The slice should still exist but the command unit should be inactive.
    if systemctl is-active --quiet "agent-${STALE_UID}.slice" 2>/dev/null; then
        note "test 8: slice still active (expected)"
    else
        note "test 8: slice already inactive"
    fi

    # Kill the isolation service.
    kill "$ISOLATION_PID" 2>/dev/null
    wait "$ISOLATION_PID" 2>/dev/null || true
    ISOLATION_PID=""
    sleep 0.5

    # Restart the isolation service.
    "$BIN_DIR/isolation-service" >/tmp/isolation-service-restart8.log 2>&1 &
    ISOLATION_PID=$!

    # Wait for socket to reappear.
    for _ in $(seq 1 20); do
        [[ -S "$RUNTIME_DIR/isolation.sock" ]] && break
        sleep 0.25
    done

    if [[ ! -S "$RUNTIME_DIR/isolation.sock" ]]; then
        fail "test 8: isolation socket not created after restart"
    else
        # Verify agent is re-adopted (even though process exited).
        LIST_RESP8=$(sock_send "$RUNTIME_DIR/isolation.sock" '{"method":"list_agents","body":null}')
        LIST_HAS8=$(echo "$LIST_RESP8" | python3 -c "
import json, sys
r = json.loads(sys.stdin.read())
body = json.loads(r['body']) if isinstance(r['body'], str) else r['body']
uids = [a['uid'] for a in body.get('agents', [])]
print('yes' if $STALE_UID in uids else 'no')
")

        if [[ "$LIST_HAS8" == "yes" ]]; then
            pass "test 8: stale agent uid=$STALE_UID re-adopted after restart"
        else
            fail "test 8: stale agent uid=$STALE_UID missing from list_agents after restart"
        fi

        # Verify nft rules are present even for the stale user.
        NFT_STALE=$(nft list chain inet filter agent-os-output 2>/dev/null || echo "")
        if echo "$NFT_STALE" | grep -q "skuid $STALE_UID"; then
            pass "test 8: nft NetDeny rule present for stale uid=$STALE_UID"
        else
            fail "test 8: nft rule missing for stale uid=$STALE_UID (security gap!)"
        fi
    fi

    # Clean up the stale agent.
    TERM_REQ8="{\"method\":\"terminate_agent\",\"body\":{\"uid\":$STALE_UID}}"
    sock_send "$RUNTIME_DIR/isolation.sock" "$TERM_REQ8" >/dev/null 2>&1
    pkill -U "$STALE_UID" 2>/dev/null
    userdel -r "$STALE_USER" 2>/dev/null
    STALE_UID=""
    STALE_USER=""
fi


# ==== Test 9: Terminate a recovered agent after restart ====

echo
note "test 9: terminate recovered agent after restart"

# Spawn a fresh agent with a long-running command.
SPAWN_REQ9='{"method":"spawn_agent","body":{"name":"termtest","cpu_quota":"50%","memory_max":"256M","net_access":"deny","command":["sleep","120"]}}'
SPAWN_RESP9=$(sock_send "$RUNTIME_DIR/isolation.sock" "$SPAWN_REQ9")
SPAWN_OK9=$(echo "$SPAWN_RESP9" | json_get "['ok']")
if [[ "$SPAWN_OK9" != "True" ]]; then
    fail "test 9: spawn_agent failed"
else
    TERM9_UID=$(echo "$SPAWN_RESP9" | resp_body_get "['agent']['uid']")
    TERM9_USER="agent-termtest-${TERM9_UID}"
    TERM9_HOME="${AGENT_HOME_BASE}/${TERM9_USER}"
    note "test 9: spawned agent uid=$TERM9_UID"

    # Kill and restart isolation service.
    kill "$ISOLATION_PID" 2>/dev/null
    wait "$ISOLATION_PID" 2>/dev/null || true
    ISOLATION_PID=""
    sleep 0.5

    "$BIN_DIR/isolation-service" >/tmp/isolation-service-restart9.log 2>&1 &
    ISOLATION_PID=$!
    for _ in $(seq 1 20); do
        [[ -S "$RUNTIME_DIR/isolation.sock" ]] && break
        sleep 0.25
    done

    if [[ ! -S "$RUNTIME_DIR/isolation.sock" ]]; then
        fail "test 9: isolation socket not created after restart"
    else
        # Terminate the recovered agent via the API.
        TERM9_REQ="{\"method\":\"terminate_agent\",\"body\":{\"uid\":$TERM9_UID}}"
        TERM9_RESP=$(sock_send "$RUNTIME_DIR/isolation.sock" "$TERM9_REQ")
        TERM9_OK=$(echo "$TERM9_RESP" | json_get "['ok']")

        if [[ "$TERM9_OK" == "True" ]]; then
            pass "test 9: terminate_agent for recovered uid=$TERM9_UID returned ok"
        else
            TERM9_ERR=$(echo "$TERM9_RESP" | json_get "['body']" 2>/dev/null || echo "unknown")
            fail "test 9: terminate_agent for recovered uid=$TERM9_UID failed: $TERM9_ERR"
        fi

        sleep 0.5

        # Verify user removed.
        if id "$TERM9_USER" &>/dev/null; then
            fail "test 9: user $TERM9_USER still exists after terminate"
        else
            pass "test 9: user $TERM9_USER removed after terminate"
        fi

        # Verify nft rules removed.
        NFT_T9=$(nft list chain inet filter agent-os-output 2>/dev/null || echo "")
        if echo "$NFT_T9" | grep -q "skuid $TERM9_UID"; then
            fail "test 9: nft rules for uid $TERM9_UID still present after terminate"
        else
            pass "test 9: nft rules for uid $TERM9_UID removed after terminate"
        fi

        # Verify absent from list_agents.
        LIST_RESP9=$(sock_send "$RUNTIME_DIR/isolation.sock" '{"method":"list_agents","body":null}')
        LIST_HAS9=$(echo "$LIST_RESP9" | python3 -c "
import json, sys
r = json.loads(sys.stdin.read())
body = json.loads(r['body']) if isinstance(r['body'], str) else r['body']
uids = [a['uid'] for a in body.get('agents', [])]
print('yes' if $TERM9_UID in uids else 'no')
")

        if [[ "$LIST_HAS9" == "no" ]]; then
            pass "test 9: agent uid=$TERM9_UID absent from list_agents after terminate"
        else
            fail "test 9: agent uid=$TERM9_UID still in list_agents after terminate"
        fi
    fi

    # Clean up.
    pkill -U "$TERM9_UID" 2>/dev/null
    userdel -r "$TERM9_USER" 2>/dev/null
    TERM9_UID=""
    TERM9_USER=""
fi


# ---- Summary ----

echo
echo "================================"
printf "  %d passed, %d failed\n" "$PASS" "$FAIL"
echo "================================"

[[ $FAIL -eq 0 ]]
