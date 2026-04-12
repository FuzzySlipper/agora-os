# 3PO/R2 Architecture

## Why this doc exists

`research/local-agents.md` makes the strategic case for a two-tier agent model: small local workers for background tool use, a frontier-facing ambassador for the human interface. This document turns that into a concrete system design that fits the rest of Agora OS.

The key constraint is that the 3PO/R2 split must reinforce, not bypass, the project's existing invariants:

- Linux uids remain the identity primitive
- root-owned services remain deterministic control points
- the admin agent stays a separate stateless evaluator
- inter-agent communication uses explicit contracts, not soft conversational glue

---

## Roles

### 3PO ambassador

The 3PO ambassador is the human-facing agent. Its job is translation and orchestration:

- turn human requests into structured work orders
- decide whether work can stay local or needs frontier reasoning
- synthesize R2 outputs back into human-readable responses
- route ambiguity, conflict, and "what did the user mean?" questions

3PO is not privileged. It does not create Linux users, does not talk to systemd directly, and does not bypass the admin agent.

### R2 agents

R2 agents are task executors. Most are small local model-backed workers or fully deterministic daemons with a narrow tool set. Their job is not general reasoning; it is bounded execution against explicit inputs, tools, and budgets.

An R2 should be specialized enough that its success criteria are obvious from the work order:

- repo search / code inspection
- patch drafting
- test execution
- filesystem observation
- UI observation for a specific granted viewport
- deterministic service adapters

### Agent supervisor

The system needs a deterministic spawn authority between 3PO and the isolation service. This doc calls it the `agent supervisor`.

Responsibilities:

- receive structured spawn requests from 3PO or the shell
- decide whether to reuse an existing R2 or create a new one
- call `spawn_agent` / `terminate_agent` on the isolation service
- enforce per-session budgets, default resource limits, and allowed worker profiles
- attach kernel-attributed identity to work assignments

This is a root-owned control-plane service, not an LLM.

### Admin agent

The admin agent remains exactly what the rest of the architecture says it is: a stateless privilege evaluator for structured escalation requests. It is not 3PO, not a general orchestrator, and not the interface by which ordinary R2 workers get created.

3PO may explain an admin decision to the user. It must not replace or impersonate the admin agent.

---

## Core decisions

### 1. 3PO lives as a dedicated long-lived agent uid

3PO should run under its own dedicated agent uid and systemd slice, not as root and not as the human uid.

Why:

- its filesystem writes, network calls, and model traffic should be attributable
- frontier-facing code is exactly the kind of thing worth containing
- it keeps the human-facing assistant inside the same security model as every other agent

3PO is special in role, not in privilege. Its special status comes from policy and compositor grants, not from escaping the uid model.

### 2. R2 agents are spawned via the isolation service, but not directly by 3PO

R2 workers are ordinary agent sessions created with the existing `spawn_agent` path. In practice that means:

1. 3PO emits a structured worker request
2. the agent supervisor validates it against policy and budgets
3. the supervisor calls the isolation service
4. the isolation service creates the agent uid, slice, nftables rules, and process

This preserves the current security posture:

- root-owned sockets stay root-owned
- non-root agents do not gain spawn authority by convention
- the path from "user asked for help" to "new executable process exists" stays explicit and auditable

### 3. Inter-agent transport is the event bus, not ad hoc direct sockets

The default wire protocol between 3PO and R2s should be the local event bus with typed JSON payloads and topic routing.

Use the event bus because it matches the rest of the system:

- local-only transport over a Unix socket
- explicit topics and payload schemas
- natural fan-out for audit, shell UI, and compositor consumers
- future ability to stamp sender identity from peer credentials at the broker

Direct Unix sockets still have a role, but they are for service boundaries:

- isolation service control
- admin-agent requests
- audit subscriber feed
- compositor bridge plugin socket

MCP is useful inside an agent boundary, not as the primary transport between agents. An R2 runtime can expose or consume MCP-shaped tools internally, but cross-agent traffic should stay on the Agora event bus so identity, policy, and observability remain local concerns.

### 4. Frontier access is centralized through 3PO

By default, only 3PO should talk to a frontier API.

R2 workers should be:

- deterministic
- local-model-backed
- or, in exceptional cases, attached to a larger local model class

If an R2 hits ambiguity or needs broader reasoning, it should return a structured `needs_3po` result rather than silently deciding to spend tokens on a frontier model. This keeps cost, trust, and user-visible reasoning concentrated in one place.

### 5. The admin agent is orthogonal to 3PO/R2

Do not collapse these roles just because both involve models.

3PO answers: "What did the user mean, and how should work be delegated?"

The admin agent answers: "Should this privileged action be approved, denied, or escalated to a human?"

Those are different trust boundaries. They should remain separate even if both happen to use local models in some deployments.

---

## Process model

### Human request flow

1. The human shell UI sends a structured turn to 3PO.
2. 3PO classifies the request:
   - answer directly
   - delegate to one R2
   - delegate to several R2s
   - ask follow-up questions
   - request human approval or admin escalation
3. If worker execution is needed, 3PO emits one or more structured spawn or reuse requests to the agent supervisor.
4. The agent supervisor either reuses an existing compatible R2 or creates a new one through the isolation service.
5. The R2 receives a work order over the event bus.
6. The R2 publishes progress and a structured result.
7. 3PO synthesizes those results into the user-facing response.

### Privileged action flow

1. An R2 encounters a permission boundary.
2. It sends a structured escalation request to the admin agent over the admin socket.
3. The admin agent evaluates and logs the decision.
4. The decision is returned to the R2.
5. 3PO may summarize the outcome for the human, but the approval path itself does not run through 3PO.

### Worker lifetime

The supervisor should support both:

- short-lived task workers created for one work order
- long-lived specialist workers reused across a session

The default should be reuse when the security envelope is equivalent:

- same tool profile
- same network policy
- same watched paths
- same compositor grants

If the task needs broader access than the current worker already has, create a new worker rather than mutating an existing one in place.

---

## Contracts

The project will stay more maintainable if the 3PO/R2 layer is built around a small set of rigid message types rather than open-ended chat transcripts. The tracked contract definitions live in `internal/schema/agent_protocol.go`; the examples below explain the intent of those contracts rather than replacing them.

### Worker profile

A worker profile describes the sandbox shape, not the specific task text.

Example:

```json
{
  "profile": "repo-inspector",
  "runtime": "local_llm",
  "tools": ["fs.read", "git.diff", "ripgrep"],
  "cpu_quota": "50%",
  "memory_max": "2G",
  "net_access": "deny",
  "watch_paths": ["/repo"]
}
```

### Spawn request

3PO should not send raw commands. It should send a structured request to the supervisor:

```json
{
  "session_id": "sess_01",
  "request_id": "req_01",
  "requester_role": "3po",
  "worker_profile": "repo-inspector",
  "objective": "Review task 501 and summarize architectural risks.",
  "inputs": {
    "repo_path": "/repo",
    "task_id": 501
  },
  "reply_topic": "agent.work.result.req_01"
}
```

The supervisor translates that into the actual `spawn_agent` command line and resource settings.

### Work order

Once a worker exists, the assignment sent over the bus should be explicit and replayable:

```json
{
  "task_id": "task_01",
  "session_id": "sess_01",
  "assigned_role": "repo-inspector",
  "objective": "Review task 501 and summarize architectural risks.",
  "inputs": {
    "paths": ["research/local-agents.md", "research/plan.md"]
  },
  "budget": {
    "max_steps": 20,
    "deadline_seconds": 300
  },
  "reply_topic": "agent.work.result.task_01"
}
```

### Result

R2 results should come back in machine-friendly form first. 3PO is what turns them into prose.

```json
{
  "task_id": "task_01",
  "status": "ok",
  "summary": "Two architectural seams need clarification.",
  "artifacts": [
    {"kind": "file", "path": "research/3po-r2.md"}
  ],
  "follow_up": [
    "Needs supervisor-side spawn policy",
    "Needs peer-attributed bus sender metadata"
  ]
}
```

This keeps R2 workers legible and testable. They return facts and structured conclusions, not polished narration.

---

## Topic taxonomy

The event bus should carry inter-agent traffic on a small, named topic set.

Suggested initial topics:

- `conversation.turn.requested`
- `conversation.turn.responded`
- `agent.spawn.requested`
- `agent.spawn.accepted`
- `agent.spawn.rejected`
- `agent.work.assigned`
- `agent.work.progress`
- `agent.work.result`
- `agent.work.cancelled`
- `agent.work.needs_3po`
- `admin.escalation.requested`
- `admin.escalation.decided`

Payloads should include task or session IDs. Identity should not be trusted from payload fields alone. The bus broker should stamp sender metadata from `SO_PEERCRED` so recipients can treat claimed roles as advisory and kernel uid as authoritative.

---

## What stays local vs what goes frontier

### Local by default

- deterministic daemons
- repo search and code inspection
- bounded patch drafting
- log summarization
- filesystem or process monitoring
- tool routing within a known tool set
- routine admin-agent evaluations where a small local model is sufficient

### Frontier through 3PO

- ambiguous user requests
- high-context synthesis across multiple workers
- difficult planning under uncertainty
- nuanced explanation back to the human
- deciding whether a task needs more capability than the local worker tier can provide

### Explicitly not frontier by default

- every background worker
- every tool call
- every escalation request
- every multi-agent exchange

The whole point of the 3PO/R2 split is to keep the expensive and higher-trust model concentrated at the human interface rather than distributing it across the execution layer.

---

## Relationship to the shell and compositor

Phase 3 introduces the human shell UI. In this architecture:

- the shell UI is the human-facing surface
- 3PO is the conversational back end for that shell
- the event bus web bridge carries shell messages into the bus
- compositor mediation still controls which surfaces an agent may observe or affect

3PO may receive standing access to its own shell surface, but it should not implicitly gain access to every agent or human surface. Compositor grants remain explicit and task-scoped.

---

## Implications for implementation

When this architecture is built, the code should follow the same explicit-boundary style as the rest of the cleanup pass:

- keep `cmd/*` thin; put 3PO logic in an `internal/ambassador` package
- keep the supervisor deterministic; no model calls in spawn policy
- define shared message structs in a tracked contract package instead of sprinkling string topics and `map[string]any`
- treat MCP as an internal adapter protocol for tools, not the core inter-agent transport
- make bus sender attribution a prerequisite for trusting inter-agent identity

The durable pattern is:

- deterministic root-owned control plane
- contained agent uids for all model-facing execution
- explicit JSON contracts at every seam

That is a better fit for agent-authored code than clever orchestration frameworks or conversationally implied behavior.

---

## Recommended next tasks

This document implies a few concrete follow-ons:

1. add peer-attributed sender metadata to the event bus
2. design the deterministic agent supervisor service
3. define tracked 3PO/R2 message schemas and topic constants
4. decide the first concrete R2 worker profiles to support

Those should land before a real 3PO implementation starts, so the user-facing layer grows on top of firm control-plane boundaries.
