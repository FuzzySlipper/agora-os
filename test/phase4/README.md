# Phase 4: Empirical Agent Validation

Phase 4 tests use real LLM-backed agents to validate whether Agora OS
feature intentions hold under actual agent usage. They complement the
deterministic Phase 1/2/3 tests.

**Phase 4 is optional.** Deterministic tests remain the authoritative
first gate. If scripted tests fail, empirical tests are not meaningful.

## Testing Philosophy

| | Phase 1–3 (Deterministic) | Phase 4 (Empirical) |
|---|---|---|
| **What it tests** | Protocol contracts, wire formats, kernel isolation | Agent intent, UX paths, workflow completion |
| **Success criteria** | Exact assertions on output/state | Pass rate across repeated runs |
| **Failure means** | A code bug or contract violation | May be a code bug, protocol ambiguity, or model limitation |
| **CI gating** | Mandatory | Advisory / informational |

## Quick Start

### Prerequisites

1. **Deterministic tests pass:**
   ```sh
   go test ./...
   ```
   The smoke script gates on this by default.

2. **Ollama installed and running** (for LLM-backed runs):
   ```sh
   ollama pull qwen3:8b   # or your preferred model
   ollama serve
   ```

3. **agent-sim built:**
   ```sh
   go build -o agent-sim ./cmd/agent-sim/
   ```

### Running the Smoke Script

```sh
# Single scenario, 10 runs, 70% threshold
./test/phase4/smoke.sh \
  --scenario test/phase4/scenarios/worker_lifecycle.json \
  --ollama-model qwen3:8b \
  --runs 10 \
  --threshold 70

# With a deterministic script (no LLM)
./test/phase4/smoke.sh \
  --scenario test/phase4/scenarios/worker_lifecycle.json \
  --script test/phase4/scripts/worker_lifecycle_script.json \
  --runs 1 \
  --threshold 100

# Skip prerequisite check (for iterative development)
./test/phase4/smoke.sh \
  --scenario test/phase4/scenarios/surface_awareness.json \
  --skip-prereqs \
  --runs 5 \
  --threshold 60
```

### Running Individual Scenarios

```sh
# Deterministic single run
agent-sim \
  --scenario test/phase4/scenarios/admin_escalation.json \
  --script test/phase4/scripts/admin_escalation_script.json

# LLM-backed single run
agent-sim \
  --brain-type ollama \
  --ollama-model qwen3:8b \
  --scenario test/phase4/scenarios/surface_awareness.json
```

## Scenario Corpus

Eight scenarios covering four categories, each with a negative/adversarial
variant:

### 1. Two-Agent Coordination
Agent receives a coordination request via event-bus-web (`agent.message`
topics), processes it, and publishes a response.

- **Primary:** `two_agent_coordination.json`
- **Adversarial:** prompt injection attempt in the received message

### 2. Surface Awareness
Agent identifies its own Wayland surface via `compositor.surface.*`
events and avoids touching human-owned surfaces.

- **Primary:** `surface_awareness.json`
- **Adversarial:** impersonated surface with mismatched owner UID

### 3. Viewport Grant Workflow
Agent requests a viewport grant via the supervisor protocol, receives
the assignment, and uses it.

- **Primary:** `grant_workflow.json`
- **Negative:** grant denied — agent must stop

### 4. Admin Escalation
Agent submits a structured escalation request and handles
approve/deny/escalate outcomes.

- **Primary:** `admin_escalation.json` (approve)
- **Negative:** `admin_escalation_denied.json` (deny)
- **Escalated:** `admin_escalation_escalated.json` (human review)

Additional utility scenarios:
- `worker_lifecycle.json` — agent lifecycle events
- `audit_compositor.json` — audit and compositor stream paths
- `shell_state_audit.json` — HTTP shell state and WebSocket audit paths

## Model Selection

| Model | Strengths | Weaknesses |
|---|---|---|
| `qwen3:8b` | Good instruction following, fast | Occasional drift on multi-step scenarios |
| `llama3.2:3b` | Very fast, small footprint | Weaker tool-use reasoning |
| `mistral:7b` | Strong reasoning on structured tasks | Slower, requires more VRAM |

**Recommendation:** Start with `qwen3:8b` for development smoke tests.
Use larger models (`mistral`, `llama3.1:70b`) for pre-release validation.

### Ollama Options

Pass model options via the smoke script or agent-sim:

```sh
agent-sim --brain-type ollama --ollama-model qwen3:8b \
  --var OLLAMA_TEMPERATURE=0.3
```

Common options: `temperature` (0.0–1.0, lower = more deterministic),
`seed` (for reproducibility), `top_p` (nucleus sampling).

## Interpreting Results

### Output Structure

```
test/phase4/artifacts/<timestamp>/
├── summary.txt           Human-readable summary
├── results.jsonl         Machine-readable run results (JSON Lines)
├── report.json           Aggregate report (pass rate, threshold status)
└── run-001/              Per-run artifacts
    ├── result.json        Structured RunResult
    ├── stderr.log         agent-sim stderr
    ├── transcript.json    Action/event transcript
    └── events.jsonl       Raw event bus events
```

### Exit Codes

| Code | Meaning |
|---|---|
| 0 | Pass rate meets or exceeds threshold |
| 1 | Pass rate below threshold |
| 2 | Environment failure (Ollama unreachable, setup error) |

### Triage Guide

When a scenario falls below threshold:

1. **Check env_failures first.** If all or most runs are env_failures,
   the environment (Ollama, event bus, service state) is the problem.

2. **Examine failing runs.** Look at `transcript.json` and `events.jsonl`
   in the per-run directories. What did the agent observe? What actions
   did it take?

3. **Classify the failure:**
   - **Code bug:** The system behaves incorrectly under deterministic
     conditions. Fix the code, then re-run.
   - **Protocol ambiguity:** The scenario's expected outcomes require
     the agent to take an action that isn't clearly documented or
     discoverable. Consider improving the protocol documentation or
     adding a more explicit API.
   - **Model limitation:** The model consistently fails to understand
     the task. Try a larger model, adjust the prompt, or simplify
     the scenario.
   - **Flaky pass:** Some runs pass, some fail with no clear pattern.
     Increase the run count and widen the threshold to characterize
     the flakiness.

### WARNING: Stochastic Failures ≠ Code Bugs

A stochastic failure (some runs pass, some fail) does not necessarily
mean the code is broken. It may indicate:

- The protocol is ambiguous and the model interprets it differently
  across runs.
- The model sometimes takes a suboptimal path that still achieves the
  intent but doesn't match the expected outcomes exactly.
- The expected outcomes are too strict for LLM-driven interaction.

**Do not weaken deterministic tests to make empirical runs pass.**
Deterministic tests validate contracts. Empirical tests validate
intent. A gap between them is data — it may point to a documentation
issue, a missing API, or an edge case the deterministic tests don't
cover.

## Promoting to Deterministic Coverage

Promote a scenario from Phase 4 to Phase 1–3 when:

1. **The scenario consistently passes** (e.g., >90% over 50+ runs
   with multiple models).

2. **The expected outcomes are precisely defined** — each outcome is
   an exact, observable assertion on a specific event topic, payload
   field, or action.

3. **The scenario tests a contract** rather than an intent. If the
   scenario says "agent must subscribe to X before publishing Y,"
   that's a contract. If it says "agent should coordinate with
   another agent," that's an intent.

4. **You can extract the deterministic golden path.** The successful
   LLM runs trace a consistent action sequence. Write that sequence
   as a deterministic script and add it to the Phase 1–3 test suite.

**Do not promote scenarios that are inherently stochastic.**
Two-agent coordination where either agent could go first, scenarios
that depend on timing, and scenarios where the "correct" response is
open-ended should remain in Phase 4.

## Adding New Scenarios

1. Create a scenario JSON in `test/phase4/scenarios/` following the
   `EmpiricalScenario` schema.

2. Create a deterministic script in `test/phase4/scripts/` for the
   golden path.

3. Add at least one negative/adversarial variant.

4. Add a test in `internal/agentsim/empirical_scenarios_test.go` that
   loads and runs the scenario with the deterministic brain.

5. Run the smoke script against the scenario:
   ```sh
   ./test/phase4/smoke.sh --scenario test/phase4/scenarios/<new>.json \
     --runs 20 --threshold 60
   ```

6. If the scenario passes above threshold, add it to the regular
   smoke rotation.

## Security Notes

- **Model output is untrusted agent behavior.** The system prompt
  explicitly states this. The runner never executes model output as
  privileged instructions.
- **Empirical tests do not weaken security invariants.** If a model
  finds a way to bypass kernel-level isolation (nftables, cgroups,
  peer credentials), that's a kernel security issue — not a test
  adjustment.
- **Admin escalation requests are structured data.** The admin agent
  evaluates `EscalationRequest` fields as data, never as instructions.
