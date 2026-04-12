#!/usr/bin/env bash
#
# phase2.sh
# ---------
# Phase 2 end-to-end compositor integration test.
#
# Requires an already-running Wayfire session with the agora bridge plugin
# loaded. This script starts the repo-owned Go services, opens one root-owned
# Wayland surface and one agent-owned Wayland surface, verifies compositor
# attribution, then proves the deny/grant path by switching the plugin's input
# context to the agent uid and sending a real keyboard event.
#
# Run on a disposable host inside the Wayfire session:
#   cd /repo
#   sudo --preserve-env=XDG_RUNTIME_DIR,WAYLAND_DISPLAY test/phase2.sh
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RUNTIME_DIR="/run/agent-os"
LOG_DIR="/var/log/agent-os"
BIN_DIR="$(mktemp -d /tmp/phase2-e2e.XXXXXX)"
BRIDGE_LOG="$LOG_DIR/compositor-grants.jsonl"
BUS_LOG="/tmp/agora-event-bus.log"
ISOLATION_LOG="/tmp/agora-isolation.log"
COMPOSITOR_LOG="/tmp/agora-compositor-bridge.log"
PASS=0
FAIL=0

BUS_PID=""
ISOLATION_PID=""
COMPOSITOR_PID=""
ROOT_WINDOW_PID=""
SPAWNED_UID=""
WATCHER_PID=""
WATCHER_OUT=""

HUMAN_TITLE="AgoraPhase2Human"
AGENT_TITLE="AgoraPhase2Agent"
WINDOW_HOLD_SECONDS=300
WINDOW_CLIENT=""
ROOT_WINDOW_CMD=""
AGENT_WINDOW_CMD=""
WAYLAND_SOCKET=""
ORIG_RUNTIME_MODE=""
ORIG_SOCKET_MODE=""

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
    [[ ${EUID} -eq 0 ]] || { echo "error: run as root in the Wayfire host session" >&2; exit 1; }
}

require_wayland_session() {
    [[ -n ${XDG_RUNTIME_DIR:-} ]] || { echo "error: XDG_RUNTIME_DIR is not set; run inside the Wayfire session and preserve env" >&2; exit 1; }
    [[ -n ${WAYLAND_DISPLAY:-} ]] || { echo "error: WAYLAND_DISPLAY is not set; run inside the Wayfire session and preserve env" >&2; exit 1; }
    WAYLAND_SOCKET="$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY"
    [[ -S "$WAYLAND_SOCKET" ]] || { echo "error: Wayland socket $WAYLAND_SOCKET not found" >&2; exit 1; }

    local compositor_uid
    compositor_uid=$(stat -Lc '%u' "$WAYLAND_SOCKET")
    if [[ "$compositor_uid" != "0" ]]; then
        echo "error: $WAYLAND_SOCKET is owned by uid $compositor_uid, not uid 0" >&2
        echo "Phase 2 acceptance currently targets a root-owned human surface." >&2
        exit 1
    fi
}

require_cmd() {
    command -v "$1" >/dev/null || { echo "error: required command '$1' not found" >&2; exit 1; }
}

pick_window_client() {
    if command -v foot >/dev/null; then
        WINDOW_CLIENT="foot"
        return 0
    fi
    if command -v weston-terminal >/dev/null; then
        WINDOW_CLIENT="weston-terminal"
        return 0
    fi
    if command -v alacritty >/dev/null; then
        WINDOW_CLIENT="alacritty"
        return 0
    fi
    if command -v kitty >/dev/null; then
        WINDOW_CLIENT="kitty"
        return 0
    fi

    echo "error: need one supported native Wayland terminal (foot, weston-terminal, alacritty, or kitty)" >&2
    exit 1
}

window_command_for() {
    local title="$1"
    case "$WINDOW_CLIENT" in
        foot)
            printf "foot --title %s sh -lc 'sleep %d'" "$title" "$WINDOW_HOLD_SECONDS"
            ;;
        weston-terminal)
            printf "weston-terminal --title=%s -e sh -lc 'sleep %d'" "$title" "$WINDOW_HOLD_SECONDS"
            ;;
        alacritty)
            printf "alacritty --title %s -e sh -lc 'sleep %d'" "$title" "$WINDOW_HOLD_SECONDS"
            ;;
        kitty)
            printf "kitty --title %s sh -lc 'sleep %d'" "$title" "$WINDOW_HOLD_SECONDS"
            ;;
        *)
            return 1
            ;;
    esac
}

json_get() {
    python3 -c "import json,sys; print(json.loads(sys.stdin.read())$1)"
}

surface_json_field() {
    local title="$1"
    local owner_uid="$2"
    local field="$3"

    "$BIN_DIR/compositorctl" list-surfaces | python3 -c '
import json, sys

title = sys.argv[1]
owner = int(sys.argv[2])
field = sys.argv[3]
for item in json.load(sys.stdin).get("surfaces", []):
    surface = item.get("surface", {})
    client = item.get("client", {})
    if surface.get("title") == title and client.get("uid") == owner:
        value = item
        for part in field.split("."):
            value = value[part]
        print(value)
        sys.exit(0)
sys.exit(1)
' "$title" "$owner_uid" "$field"
}

wait_for_surface() {
    local title="$1"
    local owner_uid="$2"
    local timeout="${3:-20}"
    local deadline=$((SECONDS + timeout))

    while (( SECONDS < deadline )); do
        if surface_json_field "$title" "$owner_uid" 'surface.id' >/tmp/agora-surface-id.$$ 2>/dev/null; then
            cat /tmp/agora-surface-id.$$
            rm -f /tmp/agora-surface-id.$$
            return 0
        fi
        sleep 0.25
    done
    rm -f /tmp/agora-surface-id.$$ 2>/dev/null || true
    return 1
}

check_access_allowed() {
    local surface_id="$1"
    local agent_uid="$2"
    local action="$3"
    local out
    out=$("$BIN_DIR/compositorctl" check-access --surface "$surface_id" --agent-uid "$agent_uid" --action "$action")
    echo "$out" | json_get "['allowed']"
}

start_bus_watcher() {
    local outfile="$1"
    local topic="$2"
    local surface_id="$3"
    local device="$4"
    local timeout="$5"

    python3 - "$RUNTIME_DIR/bus.sock" "$topic" "$surface_id" "$device" "$outfile" "$timeout" <<'PY' &
import json, socket, sys, time

sock_path, topic, surface_id, device, outfile, timeout_s = sys.argv[1:7]
timeout = float(timeout_s)

sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
sock.connect(sock_path)
sock.sendall((json.dumps({"op": "sub", "topic": topic}) + "\n").encode())
sock.settimeout(timeout)
start = time.time()
buffer = b""

try:
    while time.time() - start < timeout:
        chunk = sock.recv(4096)
        if not chunk:
            break
        buffer += chunk
        while b"\n" in buffer:
            line, buffer = buffer.split(b"\n", 1)
            if not line:
                continue
            event = json.loads(line.decode())
            body = event.get("body", {})
            surface = body.get("surface", {})
            if surface.get("id") != surface_id:
                continue
            if body.get("device") != device:
                continue
            with open(outfile, "w", encoding="utf-8") as fh:
                json.dump(event, fh)
            sys.exit(0)
except socket.timeout:
    pass
finally:
    sock.close()

sys.exit(1)
PY
    echo $!
}

launch_root_window() {
    env XDG_RUNTIME_DIR="$XDG_RUNTIME_DIR" WAYLAND_DISPLAY="$WAYLAND_DISPLAY" \
        sh -lc "$ROOT_WINDOW_CMD" >/tmp/agora-root-window.log 2>&1 &
    ROOT_WINDOW_PID=$!
}

spawn_agent_window() {
    local spawn_json
    spawn_json=$("$BIN_DIR/agentctl" spawn --name phase2-e2e --cpu 50% --mem 256M --net allow -- \
        sh -lc "$AGENT_WINDOW_CMD")
    SPAWNED_UID=$(echo "$spawn_json" | json_get "['agent']['uid']")
}

relax_wayland_socket_perms() {
    ORIG_RUNTIME_MODE=$(stat -Lc '%a' "$XDG_RUNTIME_DIR")
    ORIG_SOCKET_MODE=$(stat -Lc '%a' "$WAYLAND_SOCKET")
    chmod 0711 "$XDG_RUNTIME_DIR"
    chmod 0666 "$WAYLAND_SOCKET"
}

restore_wayland_socket_perms() {
    [[ -n "$ORIG_RUNTIME_MODE" ]] && chmod "$ORIG_RUNTIME_MODE" "$XDG_RUNTIME_DIR" 2>/dev/null || true
    [[ -n "$ORIG_SOCKET_MODE" ]] && chmod "$ORIG_SOCKET_MODE" "$WAYLAND_SOCKET" 2>/dev/null || true
}

dump_logs_on_fail() {
    if (( FAIL > 0 )); then
        note "compositor-bridge log:"
        tail -n 50 "$COMPOSITOR_LOG" 2>/dev/null || true
        note "event-bus log:"
        tail -n 50 "$BUS_LOG" 2>/dev/null || true
        note "isolation log:"
        tail -n 50 "$ISOLATION_LOG" 2>/dev/null || true
        note "wtype denied:"
        cat /tmp/agora-wtype-denied.log 2>/dev/null || true
        note "wtype allowed:"
        cat /tmp/agora-wtype-allowed.log 2>/dev/null || true
    fi
}

cleanup() {
    set +e

    dump_logs_on_fail

    "$BIN_DIR/compositorctl" clear-input-context >/dev/null 2>&1

    if [[ -n "$WATCHER_PID" ]]; then
        kill "$WATCHER_PID" 2>/dev/null
        wait "$WATCHER_PID" 2>/dev/null
    fi

    if [[ -n "$SPAWNED_UID" ]]; then
        "$BIN_DIR/agentctl" terminate "$SPAWNED_UID" >/dev/null 2>&1
        pkill -U "$SPAWNED_UID" 2>/dev/null
        systemctl stop "agent-${SPAWNED_UID}-cmd.service" 2>/dev/null
        systemctl stop "agent-${SPAWNED_UID}.slice" 2>/dev/null
    fi

    [[ -n "$ROOT_WINDOW_PID" ]] && kill "$ROOT_WINDOW_PID" 2>/dev/null && wait "$ROOT_WINDOW_PID" 2>/dev/null
    [[ -n "$COMPOSITOR_PID" ]] && kill "$COMPOSITOR_PID" 2>/dev/null && wait "$COMPOSITOR_PID" 2>/dev/null
    [[ -n "$ISOLATION_PID" ]] && kill "$ISOLATION_PID" 2>/dev/null && wait "$ISOLATION_PID" 2>/dev/null
    [[ -n "$BUS_PID" ]] && kill "$BUS_PID" 2>/dev/null && wait "$BUS_PID" 2>/dev/null

    restore_wayland_socket_perms

    rm -f "$RUNTIME_DIR"/{bus.sock,isolation.sock,compositor-bridge.sock,compositor-control.sock}
    rm -rf "$BIN_DIR"
}

require_root
require_wayland_session
require_cmd python3
require_cmd wtype
pick_window_client

ROOT_WINDOW_CMD=$(window_command_for "$HUMAN_TITLE")
AGENT_WINDOW_CMD="XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR WAYLAND_DISPLAY=$WAYLAND_DISPLAY $(window_command_for "$AGENT_TITLE")"

trap cleanup EXIT

note "building services and CLIs"
(
    cd "$ROOT_DIR"
    go build -o "$BIN_DIR/event-bus" ./cmd/event-bus
    go build -o "$BIN_DIR/isolation-service" ./cmd/isolation-service
    go build -o "$BIN_DIR/compositor-bridge" ./cmd/compositor-bridge
    go build -o "$BIN_DIR/agentctl" ./cmd/agentctl
    go build -o "$BIN_DIR/compositorctl" ./cmd/compositorctl
)

mkdir -p "$RUNTIME_DIR" "$LOG_DIR"
rm -f "$RUNTIME_DIR"/{bus.sock,isolation.sock,compositor-bridge.sock,compositor-control.sock}
rm -f "$BRIDGE_LOG"

note "starting event bus, isolation service, and compositor bridge"
"$BIN_DIR/event-bus" >"$BUS_LOG" 2>&1 &
BUS_PID=$!
"$BIN_DIR/isolation-service" >"$ISOLATION_LOG" 2>&1 &
ISOLATION_PID=$!
AGORA_COMPOSITOR_GRANT_LOG="$BRIDGE_LOG" "$BIN_DIR/compositor-bridge" >"$COMPOSITOR_LOG" 2>&1 &
COMPOSITOR_PID=$!

for _ in $(seq 1 40); do
    [[ -S "$RUNTIME_DIR/bus.sock" && -S "$RUNTIME_DIR/isolation.sock" && -S "$RUNTIME_DIR/compositor-control.sock" ]] && break
    sleep 0.25
done

for sock in bus.sock isolation.sock compositor-control.sock; do
    [[ -S "$RUNTIME_DIR/$sock" ]] || { echo "error: $sock not created" >&2; exit 1; }
done

relax_wayland_socket_perms
sleep 1

note "test 1: launch an agent-owned Wayland surface and verify uid attribution"
spawn_agent_window
if [[ -n "$SPAWNED_UID" ]]; then
    pass "spawned agent uid $SPAWNED_UID"
else
    fail "agent spawn did not return a uid"
    exit 1
fi

AGENT_SURFACE_ID="$(wait_for_surface "$AGENT_TITLE" "$SPAWNED_UID" 20 || true)"
if [[ -n "$AGENT_SURFACE_ID" ]]; then
    pass "bridge tracked agent surface $AGENT_SURFACE_ID for uid $SPAWNED_UID"
else
    fail "agent-owned Wayland surface was not observed; ensure the plugin is loaded and the agent can reach $WAYLAND_SOCKET"
    exit 1
fi

note "test 2: launch a root-owned surface and verify agent access is denied before grant"
launch_root_window
HUMAN_SURFACE_ID="$(wait_for_surface "$HUMAN_TITLE" 0 20 || true)"
if [[ -n "$HUMAN_SURFACE_ID" ]]; then
    pass "bridge tracked human surface $HUMAN_SURFACE_ID for uid 0"
else
    fail "root-owned surface was not observed through the plugin/bridge pipeline"
    exit 1
fi

if [[ $(check_access_allowed "$HUMAN_SURFACE_ID" "$SPAWNED_UID" read_pixels) == "False" ]]; then
    pass "bridge denies read_pixels from agent uid $SPAWNED_UID to uid-0 surface before grant"
else
    fail "bridge unexpectedly allowed read_pixels before grant"
fi

if [[ $(check_access_allowed "$HUMAN_SURFACE_ID" "$SPAWNED_UID" keyboard) == "False" ]]; then
    pass "bridge denies keyboard input from agent uid $SPAWNED_UID to uid-0 surface before grant"
else
    fail "bridge unexpectedly allowed keyboard input before grant"
fi

WATCHER_OUT="$(mktemp /tmp/phase2-input-denied.XXXXXX.json)"
WATCHER_PID="$(start_bus_watcher "$WATCHER_OUT" 'compositor.surface.input' "$HUMAN_SURFACE_ID" 'keyboard' 5)"
"$BIN_DIR/compositorctl" set-input-context --agent-uid "$SPAWNED_UID" >/dev/null
sleep 0.5
wtype denied >/tmp/agora-wtype-denied.log 2>&1 || true

if wait "$WATCHER_PID"; then
    if grep -q '"event": "input_denied"' "$WATCHER_OUT" 2>/dev/null || grep -q '"event":"input_denied"' "$WATCHER_OUT" 2>/dev/null; then
        pass "plugin emitted compositor.surface.input denial for agent-driven keyboard input to uid-0 surface"
    else
        fail "input-denied watcher fired but did not record an input_denied event"
    fi
else
    fail "did not observe compositor.surface.input denial; ensure the human window has keyboard focus and wtype is functioning"
fi
rm -f "$WATCHER_OUT"
WATCHER_OUT=""
WATCHER_PID=""

note "test 3: grant viewport access and verify the denial stops"
if "$BIN_DIR/compositorctl" grant-viewport --surface "$HUMAN_SURFACE_ID" --agent-uid "$SPAWNED_UID" >/dev/null; then
    pass "grant-viewport recorded explicit approval for uid $SPAWNED_UID"
else
    fail "grant-viewport command failed"
fi

if [[ $(check_access_allowed "$HUMAN_SURFACE_ID" "$SPAWNED_UID" read_pixels) == "True" ]]; then
    pass "bridge allows read_pixels after viewport grant"
else
    fail "bridge still denies read_pixels after viewport grant"
fi

if [[ $(check_access_allowed "$HUMAN_SURFACE_ID" "$SPAWNED_UID" keyboard) == "True" ]]; then
    pass "bridge allows keyboard after viewport grant"
else
    fail "bridge still denies keyboard after viewport grant"
fi

WATCHER_OUT="$(mktemp /tmp/phase2-input-allowed.XXXXXX.json)"
WATCHER_PID="$(start_bus_watcher "$WATCHER_OUT" 'compositor.surface.input' "$HUMAN_SURFACE_ID" 'keyboard' 2)"
sleep 0.5
wtype allowed >/tmp/agora-wtype-allowed.log 2>&1 || true

if wait "$WATCHER_PID"; then
    fail "still observed compositor.surface.input denial after viewport grant"
else
    pass "no compositor.surface.input denial observed after viewport grant"
fi
rm -f "$WATCHER_OUT"
WATCHER_OUT=""
WATCHER_PID=""

"$BIN_DIR/compositorctl" clear-input-context >/dev/null

if [[ -f "$BRIDGE_LOG" ]] && grep -q '"kind":"grant"' "$BRIDGE_LOG" && grep -q "$HUMAN_SURFACE_ID" "$BRIDGE_LOG"; then
    pass "append-only grant log recorded the viewport grant"
else
    fail "grant log did not record the viewport grant"
fi

echo
note "phase 2 complete: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
