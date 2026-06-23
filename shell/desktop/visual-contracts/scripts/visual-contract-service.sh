#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: visual-contract-service.sh <command> [args]

Commands:
  health
  from-web-evidence <web-evidence.json> <contract.json>
  validate <contract.json> <validate-response.json>
  promote <promote-payload.json> <promoted-contract.json>
  compare <reference.contract.json> <candidate.contract.json> <compare-response.json>
  fetch-artifacts <compare-response.json> <out-dir>

Security:
  Run this on den-srv, or through an SSH session/tunnel that reaches den-srv
  loopback. The script sources /etc/den-services/visual-contract.env only when
  needed and never prints DEN_VISUAL_CONTRACT_SERVICE_TOKEN.
EOF
}

BASE_URL=${VISUAL_CONTRACT_BASE_URL:-http://127.0.0.1:8086/visual-contracts}
HEALTH_URL=${VISUAL_CONTRACT_HEALTH_URL:-http://127.0.0.1:8086/health}
ENV_FILE=${VISUAL_CONTRACT_ENV_FILE:-/etc/den-services/visual-contract.env}

load_token() {
  if [[ -z "${DEN_VISUAL_CONTRACT_SERVICE_TOKEN:-}" ]]; then
    if [[ ! -r "$ENV_FILE" ]]; then
      echo "visual-contract token env file is not readable: $ENV_FILE" >&2
      return 1
    fi
    set -a
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    set +a
  fi
  if [[ -z "${DEN_VISUAL_CONTRACT_SERVICE_TOKEN:-}" ]]; then
    echo "DEN_VISUAL_CONTRACT_SERVICE_TOKEN is missing after sourcing $ENV_FILE" >&2
    return 1
  fi
}

api_post() {
  local endpoint=$1
  local payload=$2
  local out=$3
  load_token
  mkdir -p "$(dirname "$out")"
  curl -fsS \
    -H "Authorization: Bearer ${DEN_VISUAL_CONTRACT_SERVICE_TOKEN}" \
    -H "Content-Type: application/json" \
    --data @"$payload" \
    "$BASE_URL/$endpoint" \
    -o "$out"
}

cmd=${1:-}
case "$cmd" in
  health)
    curl -fsS "$HEALTH_URL"
    ;;
  from-web-evidence)
    [[ $# -eq 3 ]] || { usage; exit 2; }
    api_post from-web-evidence "$2" "$3"
    ;;
  validate)
    [[ $# -eq 3 ]] || { usage; exit 2; }
    tmp=$(mktemp)
    trap 'rm -f "$tmp"' EXIT
    python3 - "$2" "$tmp" <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    contract = json.load(handle)
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    json.dump({"contract": contract}, handle, indent=2)
    handle.write("\n")
PY
    api_post validate "$tmp" "$3"
    ;;
  promote)
    [[ $# -eq 3 ]] || { usage; exit 2; }
    api_post promote-contract "$2" "$3.tmp"
    python3 - "$3.tmp" "$3" <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    response = json.load(handle)
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    json.dump(response.get("contract", response), handle, indent=2)
    handle.write("\n")
PY
    rm -f "$3.tmp"
    ;;
  compare)
    [[ $# -eq 4 ]] || { usage; exit 2; }
    tmp=$(mktemp)
    trap 'rm -f "$tmp"' EXIT
    python3 - "$2" "$3" "$tmp" <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    reference = json.load(handle)
with open(sys.argv[2], "r", encoding="utf-8") as handle:
    candidate = json.load(handle)
with open(sys.argv[3], "w", encoding="utf-8") as handle:
    json.dump({"reference": reference, "candidate": candidate}, handle, indent=2)
    handle.write("\n")
PY
    api_post compare "$tmp" "$4"
    ;;
  fetch-artifacts)
    [[ $# -eq 3 ]] || { usage; exit 2; }
    load_token
    response=$2
    out_dir=$3
    mkdir -p "$out_dir"
    run_id=$(python3 - "$response" <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    print(json.load(handle).get("run_id", ""))
PY
)
    if [[ -z "$run_id" ]]; then
      echo "compare response has no run_id: $response" >&2
      exit 1
    fi
    for artifact in report.json reference.overlay.svg candidate.overlay.svg diff.overlay.svg reference.contract.json candidate.contract.json; do
      curl -fsS \
        -H "Authorization: Bearer ${DEN_VISUAL_CONTRACT_SERVICE_TOKEN}" \
        "$BASE_URL/$run_id/artifacts/$artifact" \
        -o "$out_dir/$artifact"
    done
    (cd "$out_dir" && sha256sum *) > "$out_dir/SHA256SUMS"
    ;;
  *)
    usage
    exit 2
    ;;
esac
