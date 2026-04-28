#!/usr/bin/env bash
# Phase 4 empirical smoke script
#
# Runs an LLM-backed agent scenario N times and evaluates success by
# threshold. Deterministic tests must pass first (or --skip-prereqs).
#
# Usage:
#   smoke.sh --scenario <path> [--script <path>] [opts]
#
# Options:
#   --scenario PATH       Scenario JSON (required)
#   --script PATH         Deterministic script JSON (default: none; uses Ollama brain)
#   --runs N              Number of runs (default: 10)
#   --threshold PCT       Minimum pass percentage (default: 70)
#   --ollama-url URL      Ollama base URL (default: http://127.0.0.1:11434)
#   --ollama-model NAME   Ollama model name (default: qwen3:8b)
#   --bus SOCKET          Event bus socket (default: /run/agent-os/bus.sock)
#   --artifact-dir DIR    Output directory (default: test/phase4/artifacts/<timestamp>)
#   --skip-prereqs        Skip deterministic prerequisite check
#   --var KEY=VALUE       Template variable for script expansion (repeatable)
#   --agent-sim PATH      Path to agent-sim binary (default: ./agent-sim)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Defaults
SCENARIO=""
SCRIPT=""
RUNS=10
THRESHOLD=70
BRAIN_TYPE="ollama"
OLLAMA_URL="http://127.0.0.1:11434"
OLLAMA_MODEL="qwen3:8b"
BUS_SOCKET="/run/agent-os/bus.sock"
ARTIFACT_DIR=""
SKIP_PREREQS=false
AGENT_SIM="agent-sim"
declare -a VAR_ARGS=()

usage() {
    grep '^#' "$0" | grep -v '^#!/' | sed 's/^# \?//'
    exit 2
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --scenario) SCENARIO="$2"; shift 2 ;;
        --script) SCRIPT="$2"; shift 2 ;;
        --runs) RUNS="$2"; shift 2 ;;
        --threshold) THRESHOLD="$2"; shift 2 ;;
        --brain-type) BRAIN_TYPE="$2"; shift 2 ;;
        --ollama-url) OLLAMA_URL="$2"; shift 2 ;;
        --ollama-model) OLLAMA_MODEL="$2"; shift 2 ;;
        --bus) BUS_SOCKET="$2"; shift 2 ;;
        --artifact-dir) ARTIFACT_DIR="$2"; shift 2 ;;
        --skip-prereqs) SKIP_PREREQS=true; shift ;;
        --var) VAR_ARGS+=("$2"); shift 2 ;;
        --agent-sim) AGENT_SIM="$2"; shift 2 ;;
        -h|--help) usage ;;
        *) echo "unknown option: $1"; usage ;;
    esac
done

if [[ -z "$SCENARIO" ]]; then
    echo "smoke.sh: --scenario is required" >&2
    usage
fi

# Resolve artifact dir.
if [[ -z "$ARTIFACT_DIR" ]]; then
    TIMESTAMP=$(date -u +%Y%m%d-%H%M%S)
    ARTIFACT_DIR="$SCRIPT_DIR/artifacts/$TIMESTAMP"
fi
mkdir -p "$ARTIFACT_DIR"

SUMMARY_FILE="$ARTIFACT_DIR/summary.txt"
RESULTS_FILE="$ARTIFACT_DIR/results.jsonl"

log()  { echo "[smoke] $*"; }
logln() { echo "[smoke] $*" | tee -a "$SUMMARY_FILE"; }

# ── Prerequisites ───────────────────────────────────────────────────────────

if [[ "$SKIP_PREREQS" != true ]]; then
    log "checking deterministic prerequisites..."
    cd "$REPO_ROOT"
    if ! go test ./... > /dev/null 2>&1; then
        logln "FAIL: deterministic tests do not pass. Run 'go test ./...' and fix before running empirical smoke."
        logln "Or use --skip-prereqs to bypass this check."
        exit 2
    fi
    log "deterministic tests pass."
fi

# Check that agent-sim is available.
if ! command -v "$AGENT_SIM" > /dev/null 2>&1; then
    log "building agent-sim..."
    cd "$REPO_ROOT"
    go build -o "$ARTIFACT_DIR/agent-sim" ./cmd/agent-sim/
    AGENT_SIM="$ARTIFACT_DIR/agent-sim"
fi

# Check Ollama reachability (only for ollama brain type).
if [[ "$BRAIN_TYPE" == "ollama" ]]; then
    log "checking Ollama at $OLLAMA_URL..."
    if ! curl -sf "$OLLAMA_URL/api/tags" > /dev/null 2>&1; then
        logln "FAIL: Ollama not reachable at $OLLAMA_URL"
        logln "Start Ollama or use --ollama-url to specify the correct endpoint."
        exit 2
    fi
    log "Ollama reachable."
fi

# ── Run ─────────────────────────────────────────────────────────────────────

logln "Phase 4 empirical smoke"
logln "  scenario:    $SCENARIO"
logln "  brain:       $BRAIN_TYPE"
logln "  script:      ${SCRIPT:-<none>}"
logln "  runs:        $RUNS"
logln "  threshold:   ${THRESHOLD}%"
if [[ "$BRAIN_TYPE" == "ollama" ]]; then
    logln "  model:       $OLLAMA_MODEL"
fi
logln "  artifacts:   $ARTIFACT_DIR"
logln ""

PASSES=0
FAILURES=0
AMBIGUOUS=0
ENV_FAILURES=0

for ((i=1; i<=RUNS; i++)); do
    RUN_ID="run-$(printf '%03d' $i)"
    log "run $i/$RUNS ($RUN_ID)..."

    RUN_DIR="$ARTIFACT_DIR/$RUN_ID"
    mkdir -p "$RUN_DIR"

    set +e
    "$AGENT_SIM" \
        --scenario "$SCENARIO" \
        ${SCRIPT:+--script "$SCRIPT"} \
        --brain-type "$BRAIN_TYPE" \
        --ollama-url "$OLLAMA_URL" \
        --ollama-model "$OLLAMA_MODEL" \
        --bus "$BUS_SOCKET" \
        --run-id "$RUN_ID" \
        --attempt "$i" \
        --artifact-dir "$RUN_DIR" \
        --timeout 300 \
        --compact \
        "${VAR_ARGS[@]/#/--var }" \
        > "$RUN_DIR/result.json" 2>"$RUN_DIR/stderr.log"
    EXIT_CODE=$?
    set -e

    # Append result to JSONL (one JSON object per line, no blank lines).
    if [[ -s "$RUN_DIR/result.json" ]]; then
        cat "$RUN_DIR/result.json" >> "$RESULTS_FILE"
    fi

    # Tally.
    if [[ $EXIT_CODE -eq 0 ]]; then
        PASSES=$((PASSES + 1))
        log "  -> PASS"
    elif [[ $EXIT_CODE -eq 1 ]]; then
        FAILURES=$((FAILURES + 1))
        log "  -> FAIL"
    elif [[ $EXIT_CODE -eq 2 ]]; then
        ENV_FAILURES=$((ENV_FAILURES + 1))
        log "  -> ENV_FAILURE"
    else
        FAILURES=$((FAILURES + 1))
        log "  -> FAIL (exit=$EXIT_CODE)"
    fi
done

# ── Report ──────────────────────────────────────────────────────────────────

TOTAL=$((PASSES + FAILURES + AMBIGUOUS + ENV_FAILURES))
# Count env_failures as unsuccessful for the threshold.
ALL_FAILS=$((FAILURES + ENV_FAILURES))
CONCLUSIVE=$((PASSES + ALL_FAILS))
if [[ $CONCLUSIVE -gt 0 ]]; then
    # Integer arithmetic: pass_rate * 100 = (passes * 10000) / conclusive, then round.
    PASS_RATE_INT=$(( (PASSES * 10000) / CONCLUSIVE ))
    PASS_RATE_DEC=$(( PASS_RATE_INT % 100 ))
    PASS_RATE_WHOLE=$(( PASS_RATE_INT / 100 ))
    PASS_RATE="${PASS_RATE_WHOLE}.${PASS_RATE_DEC}"
else
    PASS_RATE="0.0"
fi

# Integer comparison: pass_rate_int >= threshold * 100
THRESHOLD_INT=$(( THRESHOLD * 100 ))
ABOVE_THRESHOLD=false
if [[ $PASS_RATE_INT -ge $THRESHOLD_INT ]]; then
    ABOVE_THRESHOLD=true
fi

logln ""
logln "──────────────────────────────────────────"
logln "Results"
logln "  total runs:   $TOTAL"
logln "  passes:       $PASSES"
logln "  failures:     $FAILURES"
logln "  env_failures: $ENV_FAILURES"
logln "  pass rate:    ${PASS_RATE}%  (threshold: ${THRESHOLD}%)"
logln "  above threshold: $ABOVE_THRESHOLD"
logln "──────────────────────────────────────────"

# Write machine-readable summary.
cat > "$ARTIFACT_DIR/report.json" <<EOF
{
  "scenario": "$(basename "$SCENARIO")",
  "total_runs": $TOTAL,
  "passes": $PASSES,
  "failures": $FAILURES,
  "env_failures": $ENV_FAILURES,
  "pass_rate_percent": $PASS_RATE,
  "threshold_percent": $THRESHOLD,
  "above_threshold": $ABOVE_THRESHOLD,
  "artifact_dir": "$ARTIFACT_DIR"
}
EOF

logln "artifacts: $ARTIFACT_DIR"
logln "results JSONL: $RESULTS_FILE"

# Exit code.
if [[ $ENV_FAILURES -gt 0 ]]; then
    logln "WARNING: $ENV_FAILURES env_failure(s) — environment issue."
    exit 2
fi

if [[ "$ABOVE_THRESHOLD" == "true" ]]; then
    logln "PASS: pass rate ${PASS_RATE}% meets threshold ${THRESHOLD}%."
    exit 0
else
    logln "FAIL: pass rate ${PASS_RATE}% below threshold ${THRESHOLD}%."
    exit 1
fi
