#!/usr/bin/env bash
#
# phase1-peercred.sh
# ------------------
# VM-first integration outline for Phase 1 peer-credential checks.
#
# This is intentionally a scaffold rather than a fully automated test:
# the point is to make the required proof shape explicit for future tasks
# involving SO_PEERCRED, cross-uid authorization, and admin-agent logging.
#
# Run inside the disposable VM:
#   cd /repo
#   sudo test/phase1-peercred.sh
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RUNTIME_DIR="/run/agent-os"
LOG_DIR="/var/log/agent-os"

note() {
    echo ":: $*"
}

fail() {
    echo "error: $*" >&2
    exit 1
}

require_root() {
    [[ ${EUID} -eq 0 ]] || fail "run this script as root inside the VM"
}

cleanup() {
    set +e
    pkill -f '/repo/cmd/isolation-service' 2>/dev/null || true
    pkill -f '/repo/cmd/admin-agent' 2>/dev/null || true
}

require_root
trap cleanup EXIT

note "building services inside the VM"
(
    cd "$ROOT_DIR"
    go build ./cmd/isolation-service ./cmd/admin-agent
)

mkdir -p "$RUNTIME_DIR" "$LOG_DIR"
rm -f "$RUNTIME_DIR"/isolation.sock "$RUNTIME_DIR"/admin-agent.sock

note "starting isolation-service and admin-agent"
"$ROOT_DIR/isolation-service" >/tmp/isolation-service.log 2>&1 &
"$ROOT_DIR/admin-agent" >/tmp/admin-agent.log 2>&1 &

sleep 1

[[ -S "$RUNTIME_DIR/isolation.sock" ]] || fail "missing $RUNTIME_DIR/isolation.sock"
[[ -S "$RUNTIME_DIR/admin-agent.sock" ]] || fail "missing $RUNTIME_DIR/admin-agent.sock"

cat <<'EOF'
Next proof steps to automate or execute manually:

1. Admin-agent anti-spoofing proof
   - Connect from a non-root uid.
   - Send an `escalate` request whose JSON body lies about `agent_uid`.
   - Assert `/var/log/agent-os/admin-agent.log` records the kernel peer uid,
     not the self-reported one.
   - Also assert the service logs the mismatch/override path if that behavior
     remains part of the implementation.

2. Isolation-service authorization proof
   - As non-root uid `dev`, attempt `spawn_agent`.
   - Expect rejection: `spawn_agent requires root`.
   - Create or identify two distinct agent uids.
   - From one non-root uid, attempt `terminate_agent` on another uid.
   - Expect rejection: `cannot terminate another agent`.
   - From non-root, call `list_agents`.
   - Expect the response to contain only the caller's own uid.

3. Suggested transport harness
   - Use `python3` or `socat` to write JSON to the Unix sockets.
   - Prefer `sudo -u <user> ...` or `runuser -u <user> -- ...` so peer identity
     is actually different at the kernel level.

4. Review bar
   - Do not mark SO_PEERCRED / authorization tasks review-ready until this
     script has been completed into a reproducible VM proof or replaced by an
     equivalent VM integration test.
EOF
