#!/usr/bin/env bash
# Bootstrap the 3PO ambassador agent.
#
# 3PO is intentionally bootstrapped through agentctl/isolation-service rather
# than through the supervisor. The supervisor is for R2 workers only; 3PO is the
# long-lived human-facing agent that later calls the supervisor as a client.
#
# Must be run as root on a host with isolation-service/event-bus/supervisor up.

set -euo pipefail

AGENT_UID="${AGENT_UID:-60010}"
AGENT_NAME="${AGENT_NAME:-3po}"
AGENT_CPU="${AGENT_CPU:-200%}"
AGENT_MEM="${AGENT_MEM:-8G}"
AGENT_NET="${AGENT_NET:-local_only}"

BIN_DIR="${BIN_DIR:-/usr/local/bin}"
AGENTCTL="${AGENTCTL:-${BIN_DIR}/agentctl}"
AMBASSADOR_BIN="${AMBASSADOR_BIN:-${BIN_DIR}/ambassador}"
WEBVIEW_LAUNCHER="${WEBVIEW_LAUNCHER:-${BIN_DIR}/webview-launcher}"

CONFIG_DIR="${CONFIG_DIR:-/etc/agent-os}"
RUNTIME_DIR="${RUNTIME_DIR:-/run/agent-os}"
PROMPT_FILE="${AMBASSADOR_PROMPT:-${CONFIG_DIR}/ambassador-system-prompt.md}"
LLM_CONFIG_FILE="${AGORA_LLM_CONFIG:-${CONFIG_DIR}/llm-defaults.json}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_LLM_DEFAULTS="${SCRIPT_DIR}/../internal/llm/defaults.json"

if [[ ${EUID} -ne 0 ]]; then
    echo "error: scripts/bootstrap-3po.sh must be run as root" >&2
    exit 1
fi

if [[ ! -x "${AGENTCTL}" ]]; then
    echo "error: agentctl not found or not executable at ${AGENTCTL}" >&2
    exit 1
fi

if [[ ! -x "${AMBASSADOR_BIN}" ]]; then
    echo "error: ambassador binary not found or not executable at ${AMBASSADOR_BIN}" >&2
    exit 1
fi

mkdir -p "${CONFIG_DIR}" "${RUNTIME_DIR}"
chmod 755 "${CONFIG_DIR}" "${RUNTIME_DIR}"

if [[ ! -f "${LLM_CONFIG_FILE}" ]]; then
    if [[ -f "${REPO_LLM_DEFAULTS}" ]]; then
        install -m 644 -o root -g root "${REPO_LLM_DEFAULTS}" "${LLM_CONFIG_FILE}"
    else
        echo "error: LLM defaults config not found at ${LLM_CONFIG_FILE}; set AGORA_LLM_CONFIG or install internal/llm/defaults.json" >&2
        exit 1
    fi
fi

read_llm_config_value() {
    local key="$1"
    python3 - "$LLM_CONFIG_FILE" "$key" <<'PY'
import json
import sys

path, key = sys.argv[1:]
with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)
value = data.get(key, "")
if not isinstance(value, str) or not value:
    raise SystemExit(f"missing required string key {key!r} in {path}")
print(value)
PY
}

LLM_ENDPOINT="${AGORA_LLM_ENDPOINT:-$(read_llm_config_value endpoint)}"
LLM_MODEL="${AGORA_LLM_MODEL:-$(read_llm_config_value model)}"

if [[ ! -f "${PROMPT_FILE}" ]]; then
    cat >"${PROMPT_FILE}" <<'EOF'
You are the 3PO ambassador, the human-facing agent in the Agora OS agent framework.

Your responsibilities:
1. Translate human requests into structured work orders for R2 workers.
2. Decide whether a request should be answered directly, delegated to R2 workers, or escalated to admin.
3. Synthesize R2 worker results into coherent human-readable responses.
4. Handle ambiguity by asking follow-up questions.

You are not privileged. You do not create Linux users, do not talk to systemd directly, and do not bypass the admin agent.
EOF
    chown root:root "${PROMPT_FILE}"
    chmod 644 "${PROMPT_FILE}"
fi

echo "Spawning 3PO ambassador through isolation-service via agentctl..."
spawn_json=$("${AGENTCTL}" spawn \
    --name "${AGENT_NAME}" \
    --uid "${AGENT_UID}" \
    --cpu "${AGENT_CPU}" \
    --mem "${AGENT_MEM}" \
    --net "${AGENT_NET}" \
    -- \
    /usr/bin/env \
        "AMBASSADOR_PROMPT=${PROMPT_FILE}" \
        "AGORA_LLM_ENDPOINT=${LLM_ENDPOINT}" \
        "AGORA_LLM_MODEL=${LLM_MODEL}" \
        "${AMBASSADOR_BIN}")

printf '%s\n' "${spawn_json}"

actual_uid=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["agent"]["uid"])' <<<"${spawn_json}")
if [[ "${actual_uid}" != "${AGENT_UID}" ]]; then
    echo "error: isolation-service returned uid ${actual_uid}, expected ${AGENT_UID}" >&2
    exit 1
fi

# The Phase 4 shell/webview stack is allowed to be deployed independently, but
# when webview-launcher is present on this host the bootstrap owns 3PO's surface
# startup as requested by the architecture notes.
if [[ -x "${WEBVIEW_LAUNCHER}" ]]; then
    echo "Starting 3PO webview surface via ${WEBVIEW_LAUNCHER}..."
    "${WEBVIEW_LAUNCHER}" --agent "${AGENT_NAME}" --uid "${AGENT_UID}" --runtime-dir "${RUNTIME_DIR}" || \
        echo "warning: webview-launcher exited non-zero; ambassador remains running" >&2
else
    echo "warning: webview-launcher not found at ${WEBVIEW_LAUNCHER}; skipping 3PO surface startup" >&2
fi

echo "3PO ambassador bootstrap complete (uid ${AGENT_UID})."
