#!/usr/bin/env bash
#
# phase3.sh
# ---------
# Phase 3 end-to-end webview shell proof.
#
# Requires an already-running Wayfire session with the Agora bridge plugin
# loaded, just like phase2.sh. This script starts the repo-owned services,
# launches two agent-owned webviews plus the human shell webview, and verifies
# that agent-to-agent messaging, shell state, surface tracking, and recent
# audit activity all hang together in the live guest.
#
# Run on a disposable host inside the Wayfire session:
#   cd /repo
#   sudo --preserve-env=XDG_RUNTIME_DIR,WAYLAND_DISPLAY test/phase3.sh
#
# Optional:
#   AGORA_PHASE3_HOLD=1 keeps the stack running after the automated probe
#   passes so a human can inspect the shell and agent webviews before cleanup.
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RUNTIME_DIR="/run/agent-os"
LOG_DIR="/var/log/agent-os"
BIN_DIR="$(mktemp -d /tmp/phase3-e2e.XXXXXX)"
WEB_DIR="$(mktemp -d /tmp/phase3-web.XXXXXX)"
chmod 0755 "$BIN_DIR" "$WEB_DIR"
SECRET_FILE="$RUNTIME_DIR/event-bus-web.secret"
BRIDGE_LOG="$LOG_DIR/compositor-grants.jsonl"
BUS_LOG="/tmp/agora-event-bus.log"
ISOLATION_LOG="/tmp/agora-isolation.log"
AUDIT_LOG="/tmp/agora-audit-service.log"
COMPOSITOR_LOG="/tmp/agora-compositor-bridge.log"
WEBBUS_LOG="/tmp/agora-event-bus-web.log"
FIXTURE_LOG="/tmp/agora-phase3-fixtures.log"
PROBE_LOG="/tmp/agora-phase3-probe.log"
SHELL_LOG="/tmp/agora-phase3-shell.log"
PASS=0
FAIL=0

HTTP_ADDR="127.0.0.1:7780"
FIXTURE_ADDR="127.0.0.1:7800"
SHELL_TITLE="Agora Shell"
SENDER_TITLE="Agora Phase3 Sender"
RECEIVER_TITLE="Agora Phase3 Receiver"
SENDER_UID_EXPECTED=60000
RECEIVER_UID_EXPECTED=60001
HOLD_ON_SUCCESS="${AGORA_PHASE3_HOLD:-0}"

BUS_PID=""
ISOLATION_PID=""
AUDIT_PID=""
COMPOSITOR_PID=""
WEBBUS_PID=""
FIXTURE_PID=""
PROBE_PID=""
SHELL_WINDOW_PID=""
WAYLAND_SOCKET=""
COMPOSITOR_UID=""
COMPOSITOR_GID=""
ORIG_RUNTIME_MODE=""
ORIG_SOCKET_MODE=""
HUMAN_TOKEN=""
SENDER_TOKEN=""
RECEIVER_TOKEN=""

declare -a SPAWNED_UIDS=()
declare -a AGENT_LAUNCH_SCRIPTS=()

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

    COMPOSITOR_UID="${AGORA_COMPOSITOR_UID:-}"
    COMPOSITOR_GID="${AGORA_COMPOSITOR_GID:-}"
    [[ -n "$COMPOSITOR_UID" ]] || COMPOSITOR_UID="$(stat -Lc '%u' "$WAYLAND_SOCKET")"
    [[ -n "$COMPOSITOR_GID" ]] || COMPOSITOR_GID="$(stat -Lc '%g' "$WAYLAND_SOCKET")"
    note "using compositor plugin peer uid=$COMPOSITOR_UID gid=$COMPOSITOR_GID from $WAYLAND_SOCKET"
}

require_cmd() {
    command -v "$1" >/dev/null || { echo "error: required command '$1' not found" >&2; exit 1; }
}

json_get() {
    python3 -c "import json,sys; print(json.loads(sys.stdin.read())$1)"
}

wait_for_file() {
    local path="$1"
    local timeout="${2:-10}"
    local deadline=$((SECONDS + timeout))
    while (( SECONDS < deadline )); do
        [[ -S "$path" ]] && return 0
        sleep 0.25
    done
    return 1
}

wait_for_http() {
    local url="$1"
    local timeout="${2:-10}"
    local deadline=$((SECONDS + timeout))
    while (( SECONDS < deadline )); do
        if curl -fsS "$url" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.25
    done
    return 1
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

render_agent_page() {
    local outfile="$1"
    local title="$2"
    local role="$3"
    local uid="$4"
    local peer_uid="$5"
    local token="$6"

    cat >"$outfile" <<HTML
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>${title}</title>
    <style>
      :root {
        color-scheme: dark;
        font-family: "Iosevka Aile", "IBM Plex Sans", sans-serif;
      }
      body {
        margin: 0;
        min-height: 100vh;
        background:
          radial-gradient(circle at top left, rgba(255, 196, 110, 0.28), transparent 40%),
          linear-gradient(160deg, #10232f, #09131c 58%, #04090d);
        color: #f4f7fb;
      }
      main {
        max-width: 56rem;
        margin: 0 auto;
        padding: 2rem;
      }
      .badge {
        display: inline-flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.4rem 0.8rem;
        border-radius: 999px;
        background: rgba(255, 255, 255, 0.08);
        font-size: 0.85rem;
        letter-spacing: 0.04em;
        text-transform: uppercase;
      }
      h1 {
        margin: 1rem 0 0.5rem;
        font-size: clamp(2rem, 5vw, 3.2rem);
      }
      p {
        max-width: 48rem;
        color: rgba(244, 247, 251, 0.78);
        line-height: 1.6;
      }
      .panel {
        margin-top: 1.5rem;
        padding: 1rem 1.2rem;
        border-radius: 1rem;
        background: rgba(6, 12, 18, 0.65);
        border: 1px solid rgba(255, 255, 255, 0.1);
        box-shadow: 0 18px 38px rgba(0, 0, 0, 0.28);
      }
      pre {
        margin: 0;
        white-space: pre-wrap;
        font-family: "Iosevka", "IBM Plex Mono", monospace;
        font-size: 0.92rem;
        line-height: 1.5;
      }
    </style>
  </head>
  <body>
    <main>
      <div class="badge">Agora Phase 3 · ${role}</div>
      <h1>${title}</h1>
      <p>
        This fixture connects to <code>event-bus-web</code> with the agent token for
        uid ${uid}, waits for its peer, and then exercises the structured
        <code>agent.message</code> path.
      </p>
      <section class="panel">
        <pre id="log">booting...</pre>
      </section>
    </main>
    <script>
      const config = {
        title: "${title}",
        role: "${role}",
        uid: ${uid},
        peerUID: ${peer_uid},
        token: "${token}",
        wsURL: "ws://127.0.0.1:7780/ws",
      };

      const state = {
        socket: null,
        readyTimer: null,
        sendTimer: null,
        attempts: 0,
        acked: false,
      };

      const logNode = document.getElementById("log");

      function log(line) {
        logNode.textContent += "\\n" + line;
      }

      function publish(topic, body) {
        state.socket.send(JSON.stringify({ op: "pub", topic, body }));
      }

      function subscribe(topic) {
        state.socket.send(JSON.stringify({ op: "sub", topic }));
      }

      function readyBody() {
        return {
          uid: config.uid,
          peer_uid: config.peerUID,
          role: config.role,
          title: config.title,
        };
      }

      function chatTopic() {
        return "agent.message." + config.uid + "." + config.peerUID + ".chat";
      }

      function inboundChatTopic() {
        return "agent.message." + config.peerUID + "." + config.uid + ".chat";
      }

      function publishReady() {
        publish("webview.broadcast.phase3.ready", readyBody());
      }

      function clearSendTimer() {
        if (state.sendTimer) {
          window.clearInterval(state.sendTimer);
          state.sendTimer = null;
        }
      }

      function sendChat() {
        if (config.role !== "sender" || state.acked || state.attempts >= 5) {
          clearSendTimer();
          return;
        }
        state.attempts += 1;
        const text = "hello from " + config.title;
        publish(chatTopic(), {
          message_id: "phase3-" + config.uid + "-" + state.attempts,
          from_uid: config.uid,
          to_uid: config.peerUID,
          kind: "chat",
          body: {
            text,
            title: config.title,
          },
        });
        log("sent chat attempt #" + state.attempts + ": " + text);
      }

      function startSenderLoop() {
        if (config.role !== "sender" || state.sendTimer || state.acked) {
          return;
        }
        sendChat();
        state.sendTimer = window.setInterval(sendChat, 1000);
      }

      function handleMessage(payload) {
        if (payload.topic === "webview.broadcast.phase3.ready") {
          const body = payload.body || {};
          if (body.uid === config.peerUID) {
            log("peer announced ready: " + body.title);
            startSenderLoop();
          }
          return;
        }

        if (payload.topic === "webview.broadcast.phase3.ack") {
          const body = payload.body || {};
          if (body.from_uid === config.peerUID && body.to_uid === config.uid) {
            state.acked = true;
            clearSendTimer();
            log("peer acknowledgement received");
          }
          return;
        }

        if (payload.topic === inboundChatTopic()) {
          const envelope = payload.body || {};
          const body = envelope.body || {};
          const text = body.text || "received chat";
          log("received chat: " + text);
          if (config.role === "receiver" && !state.acked) {
            state.acked = true;
            publish("webview.broadcast.phase3.ack", {
              from_uid: config.uid,
              to_uid: config.peerUID,
              title: config.title,
              text,
            });
            log("published acknowledgement");
          }
        }
      }

      const socket = new WebSocket(config.wsURL, ["agora.token." + config.token]);
      state.socket = socket;
      logNode.textContent = "connecting to " + config.wsURL + " as uid " + config.uid;

      socket.addEventListener("open", () => {
        subscribe("webview.broadcast.phase3.ready");
        subscribe("webview.broadcast.phase3.ack");
        subscribe("agent.message.*." + config.uid + ".chat");
        publishReady();
        state.readyTimer = window.setInterval(publishReady, 500);
        window.setTimeout(() => {
          if (state.readyTimer) {
            window.clearInterval(state.readyTimer);
            state.readyTimer = null;
          }
        }, 5000);
        if (config.role === "sender") {
          window.setTimeout(startSenderLoop, 3000);
        }
        log("websocket open");
      });

      socket.addEventListener("message", (event) => {
        try {
          handleMessage(JSON.parse(event.data));
        } catch (error) {
          log("decode error: " + error.message);
        }
      });

      socket.addEventListener("close", (event) => {
        log("websocket closed: " + event.code);
      });

      socket.addEventListener("error", () => {
        log("websocket error");
      });
    </script>
  </body>
</html>
HTML
}

create_agent_launch_script() {
    local script_path="$1"
    local agent_name="$2"
    local url="$3"
    local title="$4"
    local audit_file="$5"

    cat >"$script_path" <<EOF
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' '${agent_name}' > "\$HOME/${audit_file}"
exec env XDG_RUNTIME_DIR='${XDG_RUNTIME_DIR}' WAYLAND_DISPLAY='${WAYLAND_DISPLAY}' '${BIN_DIR}/webview-launcher' --url='${url}' --title='${title}' --width=720 --height=540
EOF
    chmod 0755 "$script_path"
    AGENT_LAUNCH_SCRIPTS+=("$script_path")
}

spawn_agent_webview() {
    local name="$1"
    local launch_script="$2"
    local expected_uid="$3"
    local spawn_json
    spawn_json=$("$BIN_DIR/agentctl" spawn --name "$name" --cpu 50% --mem 512M --net local_only -- "$launch_script")
    local actual_uid
    actual_uid=$(echo "$spawn_json" | json_get "['agent']['uid']")
    SPAWNED_UIDS+=("$actual_uid")
    if [[ "$actual_uid" == "$expected_uid" ]]; then
        pass "spawned ${name} as uid ${actual_uid}"
    else
        fail "expected ${name} uid ${expected_uid}, got ${actual_uid}"
        exit 1
    fi
}

dump_logs_on_fail() {
    if (( FAIL > 0 )); then
        note "phase3 probe log:"
        tail -n 80 "$PROBE_LOG" 2>/dev/null || true
        note "event-bus-web log:"
        tail -n 80 "$WEBBUS_LOG" 2>/dev/null || true
        note "compositor-bridge log:"
        tail -n 80 "$COMPOSITOR_LOG" 2>/dev/null || true
        note "audit-service log:"
        tail -n 80 "$AUDIT_LOG" 2>/dev/null || true
        note "isolation-service log:"
        tail -n 80 "$ISOLATION_LOG" 2>/dev/null || true
        note "event-bus log:"
        tail -n 80 "$BUS_LOG" 2>/dev/null || true
        note "fixture server log:"
        tail -n 80 "$FIXTURE_LOG" 2>/dev/null || true
        note "shell webview log:"
        tail -n 80 "$SHELL_LOG" 2>/dev/null || true
    fi
}

hold_for_manual_testing() {
    note "manual inspection mode enabled (AGORA_PHASE3_HOLD=1)"
    note "shell URL: http://${HTTP_ADDR}/shell/#token=${HUMAN_TOKEN}"
    note "fixture pages: http://${FIXTURE_ADDR}/sender.html and http://${FIXTURE_ADDR}/receiver.html"
    note "logs: $PROBE_LOG $WEBBUS_LOG $COMPOSITOR_LOG $AUDIT_LOG $ISOLATION_LOG $BUS_LOG $FIXTURE_LOG $SHELL_LOG"
    if [[ -t 0 ]]; then
        read -r -p "Press Enter to clean up the Phase 3 session..."
        return
    fi

    note "no interactive tty detected; press Ctrl-C in the caller when you are done inspecting"
    while true; do
        sleep 3600 &
        wait $!
    done
}

cleanup() {
    set +e

    dump_logs_on_fail

    if [[ -n "$PROBE_PID" ]]; then
        kill "$PROBE_PID" 2>/dev/null
        wait "$PROBE_PID" 2>/dev/null
    fi

    for uid in "${SPAWNED_UIDS[@]}"; do
        "$BIN_DIR/agentctl" terminate "$uid" >/dev/null 2>&1
        pkill -U "$uid" 2>/dev/null
        systemctl stop "agent-${uid}-cmd.service" 2>/dev/null
        systemctl stop "agent-${uid}.slice" 2>/dev/null
    done

    [[ -n "$SHELL_WINDOW_PID" ]] && kill "$SHELL_WINDOW_PID" 2>/dev/null && wait "$SHELL_WINDOW_PID" 2>/dev/null
    [[ -n "$FIXTURE_PID" ]] && kill "$FIXTURE_PID" 2>/dev/null && wait "$FIXTURE_PID" 2>/dev/null
    [[ -n "$WEBBUS_PID" ]] && kill "$WEBBUS_PID" 2>/dev/null && wait "$WEBBUS_PID" 2>/dev/null
    [[ -n "$COMPOSITOR_PID" ]] && kill "$COMPOSITOR_PID" 2>/dev/null && wait "$COMPOSITOR_PID" 2>/dev/null
    [[ -n "$AUDIT_PID" ]] && kill "$AUDIT_PID" 2>/dev/null && wait "$AUDIT_PID" 2>/dev/null
    [[ -n "$ISOLATION_PID" ]] && kill "$ISOLATION_PID" 2>/dev/null && wait "$ISOLATION_PID" 2>/dev/null
    [[ -n "$BUS_PID" ]] && kill "$BUS_PID" 2>/dev/null && wait "$BUS_PID" 2>/dev/null

    restore_wayland_socket_perms

    rm -f "$RUNTIME_DIR"/{bus.sock,isolation.sock,audit.sock,compositor-bridge.sock,compositor-control.sock,event-bus-web.secret}
    rm -rf "$BIN_DIR" "$WEB_DIR"
}

require_root
require_wayland_session
require_cmd curl
require_cmd python3

trap cleanup EXIT

note "building services, CLIs, and the phase3 probe"
(
    cd "$ROOT_DIR"
    go build -o "$BIN_DIR/event-bus" ./cmd/event-bus
    go build -o "$BIN_DIR/isolation-service" ./cmd/isolation-service
    go build -o "$BIN_DIR/audit-service" ./cmd/audit-service
    go build -o "$BIN_DIR/compositor-bridge" ./cmd/compositor-bridge
    go build -o "$BIN_DIR/event-bus-web" ./cmd/event-bus-web
    go build -o "$BIN_DIR/agentctl" ./cmd/agentctl
    go build -o "$BIN_DIR/webview-launcher" ./cmd/webview-launcher
    go build -o "$BIN_DIR/phase3probe" ./test/phase3probe
)

mkdir -p "$RUNTIME_DIR" "$LOG_DIR" /var/lib/agents
rm -f "$RUNTIME_DIR"/{bus.sock,isolation.sock,audit.sock,compositor-bridge.sock,compositor-control.sock,event-bus-web.secret}
rm -f "$BRIDGE_LOG"

note "starting event bus, audit service, isolation service, and compositor bridge"
"$BIN_DIR/event-bus" >"$BUS_LOG" 2>&1 &
BUS_PID=$!
wait_for_file "$RUNTIME_DIR/bus.sock" 10 || { echo "error: bus.sock not created" >&2; exit 1; }
"$BIN_DIR/audit-service" >"$AUDIT_LOG" 2>&1 &
AUDIT_PID=$!
"$BIN_DIR/isolation-service" >"$ISOLATION_LOG" 2>&1 &
ISOLATION_PID=$!
AGORA_COMPOSITOR_UID="$COMPOSITOR_UID" \
    AGORA_COMPOSITOR_GID="$COMPOSITOR_GID" \
    AGORA_COMPOSITOR_GRANT_LOG="$BRIDGE_LOG" \
    "$BIN_DIR/compositor-bridge" >"$COMPOSITOR_LOG" 2>&1 &
COMPOSITOR_PID=$!

for sock in audit.sock isolation.sock compositor-control.sock; do
	if wait_for_file "$RUNTIME_DIR/$sock" 10; then
		:
	else
        echo "error: $sock not created" >&2
        exit 1
    fi
done
pass "core services created their sockets under $RUNTIME_DIR"

note "starting event-bus-web and the static Phase 3 fixture server"
AGORA_WEBBUS_ALLOWED_ORIGINS="http://${HTTP_ADDR},http://${FIXTURE_ADDR}" \
    "$BIN_DIR/event-bus-web" --listen "$HTTP_ADDR" --secret-file "$SECRET_FILE" >"$WEBBUS_LOG" 2>&1 &
WEBBUS_PID=$!
python3 -m http.server "${FIXTURE_ADDR##*:}" --bind 127.0.0.1 --directory "$WEB_DIR" >"$FIXTURE_LOG" 2>&1 &
FIXTURE_PID=$!

wait_for_http "http://${HTTP_ADDR}/" 10 || { echo "error: event-bus-web did not become reachable" >&2; exit 1; }
wait_for_http "http://${FIXTURE_ADDR}/" 10 || { echo "error: fixture server did not become reachable" >&2; exit 1; }
pass "HTTP endpoints for shell UI and fixtures are reachable"

note "minting the human and agent tokens"
HUMAN_TOKEN=$("$BIN_DIR/event-bus-web" mint-token --secret-file "$SECRET_FILE" --human)
SENDER_TOKEN=$("$BIN_DIR/event-bus-web" mint-token --secret-file "$SECRET_FILE" --uid "$SENDER_UID_EXPECTED")
RECEIVER_TOKEN=$("$BIN_DIR/event-bus-web" mint-token --secret-file "$SECRET_FILE" --uid "$RECEIVER_UID_EXPECTED")

render_agent_page "$WEB_DIR/sender.html" "$SENDER_TITLE" "sender" "$SENDER_UID_EXPECTED" "$RECEIVER_UID_EXPECTED" "$SENDER_TOKEN"
render_agent_page "$WEB_DIR/receiver.html" "$RECEIVER_TITLE" "receiver" "$RECEIVER_UID_EXPECTED" "$SENDER_UID_EXPECTED" "$RECEIVER_TOKEN"

create_agent_launch_script "$WEB_DIR/launch-sender.sh" "phase3-sender" "http://${FIXTURE_ADDR}/sender.html" "$SENDER_TITLE" "phase3-sender-audit.txt"
create_agent_launch_script "$WEB_DIR/launch-receiver.sh" "phase3-receiver" "http://${FIXTURE_ADDR}/receiver.html" "$RECEIVER_TITLE" "phase3-receiver-audit.txt"

note "starting the phase3 probe before any webviews connect"
"$BIN_DIR/phase3probe" \
    --bus-socket "$RUNTIME_DIR/bus.sock" \
    --shell-base "http://${HTTP_ADDR}" \
    --human-token "$HUMAN_TOKEN" \
    --sender-uid "$SENDER_UID_EXPECTED" \
    --receiver-uid "$RECEIVER_UID_EXPECTED" \
    --sender-title "$SENDER_TITLE" \
    --receiver-title "$RECEIVER_TITLE" \
    --shell-title "$SHELL_TITLE" \
    >"$PROBE_LOG" 2>&1 &
PROBE_PID=$!

relax_wayland_socket_perms
sleep 1

note "launching the human shell webview"
env XDG_RUNTIME_DIR="$XDG_RUNTIME_DIR" WAYLAND_DISPLAY="$WAYLAND_DISPLAY" \
    "$BIN_DIR/webview-launcher" --url "http://${HTTP_ADDR}/shell/#token=${HUMAN_TOKEN}" --title "$SHELL_TITLE" \
    >"$SHELL_LOG" 2>&1 &
SHELL_WINDOW_PID=$!

note "spawning the two agent-owned webviews"
spawn_agent_webview "phase3-sender" "$WEB_DIR/launch-sender.sh" "$SENDER_UID_EXPECTED"
spawn_agent_webview "phase3-receiver" "$WEB_DIR/launch-receiver.sh" "$RECEIVER_UID_EXPECTED"

if wait "$PROBE_PID"; then
    PROBE_PID=""
    pass "Phase 3 probe observed chat delivery, shell state, and audit activity"
else
    fail "Phase 3 probe did not complete successfully; inspect the logs above"
fi

if [[ "$HOLD_ON_SUCCESS" == "1" && $FAIL -eq 0 ]]; then
    hold_for_manual_testing
fi

echo
note "phase 3 complete: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
