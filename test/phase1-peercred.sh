#!/usr/bin/env bash
#
# phase1-peercred.sh
# ------------------
# VM integration test for Phase 1 peer-credential checks.
#
# Proves that SO_PEERCRED enforcement works end-to-end:
#   - admin-agent overrides self-reported uid with kernel peer uid
#   - isolation-service rejects unauthorized operations based on peer uid
#
# Run inside the disposable VM as root:
#   cd /repo
#   sudo test/phase1-peercred.sh
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RUNTIME_DIR="/run/agent-os"
LOG_DIR="/var/log/agent-os"
ADMIN_LOG="$LOG_DIR/admin-agent.log"
TEST_USER="agora-test-peer"
PROOF_AGENT_NAME="list-proof"
BIN_DIR="$(mktemp -d /tmp/phase1-peercred.XXXXXX)"
ADMIN_BIN="$BIN_DIR/admin-agent"
ISOLATION_BIN="$BIN_DIR/isolation-service"
PASS=0
FAIL=0
SPAWNED_UID=""
ADMIN_PID=""
ISOLATION_PID=""

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

cleanup() {
    set +e
    if [[ -n "$SPAWNED_UID" ]]; then
        if [[ -S "$RUNTIME_DIR/isolation.sock" ]]; then
            sock_send "$RUNTIME_DIR/isolation.sock" \
                "{\"method\":\"terminate_agent\",\"body\":{\"uid\":$SPAWNED_UID}}" \
                >/dev/null 2>&1
        fi
        pkill -U "$SPAWNED_UID" 2>/dev/null
        systemctl stop "agent-$SPAWNED_UID-cmd.service" 2>/dev/null
        systemctl stop "agent-$SPAWNED_UID.slice" 2>/dev/null
        userdel -r "agent-$PROOF_AGENT_NAME-$SPAWNED_UID" 2>/dev/null
    fi
    [[ -n "$ISOLATION_PID" ]] && kill "$ISOLATION_PID" 2>/dev/null
    [[ -n "$ADMIN_PID" ]] && kill "$ADMIN_PID" 2>/dev/null
    [[ -n "$ISOLATION_PID" ]] && wait "$ISOLATION_PID" 2>/dev/null
    [[ -n "$ADMIN_PID" ]] && wait "$ADMIN_PID" 2>/dev/null
    userdel -r "$TEST_USER" 2>/dev/null
    rm -f "$RUNTIME_DIR"/{isolation.sock,admin-agent.sock}
    rm -rf "$BIN_DIR"
}

# ---- Prerequisites ----

require_root
command -v python3 >/dev/null || { echo "error: python3 required" >&2; exit 1; }

trap cleanup EXIT

# ---- Setup ----

note "creating test user"
id "$TEST_USER" &>/dev/null || useradd -M -s /usr/sbin/nologin "$TEST_USER"
TEST_UID=$(id -u "$TEST_USER")
note "test user $TEST_USER uid=$TEST_UID"

note "building services"
(
    cd "$ROOT_DIR"
    go build -o "$ISOLATION_BIN" ./cmd/isolation-service
    go build -o "$ADMIN_BIN" ./cmd/admin-agent
)

mkdir -p "$RUNTIME_DIR" "$LOG_DIR"
rm -f "$RUNTIME_DIR"/{isolation.sock,admin-agent.sock}
: > "$ADMIN_LOG"

note "starting services"

# Point admin-agent LLM endpoint to a guaranteed-unreachable address so the
# HTTP call fails instantly with "connection refused" instead of hanging on
# DNS or network.  evaluate() handles the error and returns decision=escalate,
# which is all the uid-override proof needs.
ADMIN_AGENT_API_URL="http://127.0.0.1:1/v1/messages" \
ADMIN_AGENT_PROMPT="$ROOT_DIR/config/admin-agent-system-prompt.md" \
    "$ADMIN_BIN" >/tmp/admin-agent.log 2>&1 &
ADMIN_PID=$!

"$ISOLATION_BIN" >/tmp/isolation-service.log 2>&1 &
ISOLATION_PID=$!

for _ in $(seq 1 20); do
    [[ -S "$RUNTIME_DIR/isolation.sock" && -S "$RUNTIME_DIR/admin-agent.sock" ]] && break
    sleep 0.25
done
[[ -S "$RUNTIME_DIR/isolation.sock" ]]   || { echo "error: isolation.sock not created" >&2; exit 1; }
[[ -S "$RUNTIME_DIR/admin-agent.sock" ]] || { echo "error: admin-agent.sock not created" >&2; exit 1; }

note "services ready"

# ---- Test 1: admin-agent overrides spoofed uid ----
#
# Connect as the test user and send an escalate request that lies about
# agent_uid (claims to be uid 0).  The admin-agent must:
#   a) log the kernel-verified uid in the append-only log, not the spoofed one
#   b) emit a mismatch warning on stderr
echo
note "test 1: admin-agent overrides spoofed uid with kernel peer uid"

ESCALATE='{"method":"escalate","body":{"agent_uid":0,"task_context":"peercred-test","requested_action":"test","requested_resource":"/test","justification":"testing uid override"}}'

sock_send_as "$TEST_USER" "$RUNTIME_DIR/admin-agent.sock" "$ESCALATE" >/dev/null

# Wait for the log write to land.
sleep 0.3

if [[ -s "$ADMIN_LOG" ]]; then
    LOGGED_UID=$(tail -1 "$ADMIN_LOG" | json_get "['request']['agent_uid']")
    if [[ "$LOGGED_UID" == "$TEST_UID" ]]; then
        pass "audit log recorded kernel uid=$TEST_UID, not spoofed uid=0"
    else
        fail "audit log has agent_uid=$LOGGED_UID, expected $TEST_UID"
    fi
else
    fail "admin-agent.log is empty after escalate request"
fi

if grep -q "uid mismatch: peer=$TEST_UID self-reported=0 (overridden)" /tmp/admin-agent.log; then
    pass "service stderr shows uid mismatch override warning"
else
    fail "uid mismatch warning not found in /tmp/admin-agent.log"
fi


# ---- Test 2: isolation-service rejects spawn_agent from non-root ----
#
# Only root (uid 0) may create new agent users.  A non-root peer must be
# rejected before the spawn logic runs.
echo
note "test 2: spawn_agent rejected for non-root peer"

SPAWN='{"method":"spawn_agent","body":{"name":"evil"}}'
SPAWN_RESP=$(sock_send_as "$TEST_USER" "$RUNTIME_DIR/isolation.sock" "$SPAWN")

SPAWN_OK=$(echo "$SPAWN_RESP" | json_get "['ok']")
SPAWN_BODY=$(echo "$SPAWN_RESP" | json_get "['body']" 2>/dev/null || echo "")

if [[ "$SPAWN_OK" == "False" ]] && echo "$SPAWN_BODY" | grep -q "root"; then
    pass "spawn_agent denied: $SPAWN_BODY"
else
    fail "spawn_agent not denied (ok=$SPAWN_OK body=$SPAWN_BODY)"
fi


# ---- Test 3: isolation-service rejects cross-uid terminate ----
#
# A non-root peer can only terminate its own uid.  Attempting to terminate
# a different uid (here uid 0) must be rejected by the authorization check.
echo
note "test 3: terminate_agent rejected for cross-uid peer"

TERM='{"method":"terminate_agent","body":{"uid":0}}'
TERM_RESP=$(sock_send_as "$TEST_USER" "$RUNTIME_DIR/isolation.sock" "$TERM")

TERM_OK=$(echo "$TERM_RESP" | json_get "['ok']")
TERM_BODY=$(echo "$TERM_RESP" | json_get "['body']" 2>/dev/null || echo "")

if [[ "$TERM_OK" == "False" ]] && echo "$TERM_BODY" | grep -q "another agent"; then
    pass "cross-uid terminate denied: $TERM_BODY"
else
    fail "cross-uid terminate not denied (ok=$TERM_OK body=$TERM_BODY)"
fi


# ---- Test 4: isolation-service filters list_agents by peer uid ----
#
# Spawn an agent as root so the manager has a non-empty list, then prove
# that root sees it and the non-root test user does not.
echo
note "test 4: list_agents filtered for non-root peer"

LIST='{"method":"list_agents","body":null}'

# First, create an agent so there is something to filter.
SPAWN_FOR_LIST="{\"method\":\"spawn_agent\",\"body\":{\"name\":\"$PROOF_AGENT_NAME\"}}"
SPAWN_FOR_LIST_RESP=$(sock_send "$RUNTIME_DIR/isolation.sock" "$SPAWN_FOR_LIST")
SPAWN_FOR_LIST_OK=$(echo "$SPAWN_FOR_LIST_RESP" | json_get "['ok']")

if [[ "$SPAWN_FOR_LIST_OK" != "True" ]]; then
    SPAWN_ERR=$(echo "$SPAWN_FOR_LIST_RESP" | json_get "['body']" 2>/dev/null || echo "unknown")
    fail "spawn_agent failed for list proof ($SPAWN_ERR)"
else
    SPAWNED_UID=$(echo "$SPAWN_FOR_LIST_RESP" | python3 -c "
import json, sys
r = json.loads(sys.stdin.read())
body = json.loads(r['body']) if isinstance(r['body'], str) else r['body']
print(body['agent']['uid'])
")
    note "spawned agent uid=$SPAWNED_UID for list proof"

    # Root should see the spawned agent.
    ROOT_RESP=$(sock_send "$RUNTIME_DIR/isolation.sock" "$LIST")
    ROOT_HAS=$(echo "$ROOT_RESP" | python3 -c "
import json, sys
r = json.loads(sys.stdin.read())
body = json.loads(r['body']) if isinstance(r['body'], str) else r['body']
uids = [a['uid'] for a in body.get('agents', [])]
print('yes' if $SPAWNED_UID in uids else 'no')
")

    if [[ "$ROOT_HAS" == "yes" ]]; then
        pass "root list_agents includes spawned agent uid=$SPAWNED_UID"
    else
        fail "root list_agents missing spawned agent uid=$SPAWNED_UID"
    fi

    # Non-root must NOT see it — its uid doesn't match the spawned agent's uid.
    USER_RESP=$(sock_send_as "$TEST_USER" "$RUNTIME_DIR/isolation.sock" "$LIST")
    USER_HAS=$(echo "$USER_RESP" | python3 -c "
import json, sys
r = json.loads(sys.stdin.read())
body = json.loads(r['body']) if isinstance(r['body'], str) else r['body']
uids = [a['uid'] for a in body.get('agents', [])]
print('yes' if $SPAWNED_UID in uids else 'no')
")

    if [[ "$USER_HAS" == "no" ]]; then
        pass "non-root list_agents excludes spawned agent uid=$SPAWNED_UID"
    else
        fail "non-root list_agents contains spawned agent uid=$SPAWNED_UID (should be filtered)"
    fi
fi


# ---- Summary ----
echo
echo "================================"
printf "  %d passed, %d failed\n" "$PASS" "$FAIL"
echo "================================"

[[ $FAIL -eq 0 ]]
