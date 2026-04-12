# Agent Supervisor Service

## Purpose

The agent supervisor is the deterministic control-plane service that sits between user-facing orchestration and the root-only isolation primitives.

It exists to solve one specific problem cleanly:

- 3PO and the shell should be able to request worker execution
- neither 3PO nor the shell should gain direct spawn authority over Linux users, slices, nftables rules, or privileged runtime configuration
- worker creation, reuse, and teardown policy should be explicit, reviewable, and auditable rather than emerging from ad hoc agent behavior

The supervisor is therefore a root-owned daemon with a narrow contract. It is not a model, not a planner, and not a replacement for the admin agent.

---

## Position in the architecture

The intended call path is:

1. the shell or 3PO decides that work should run in an R2 worker
2. it sends a structured request to the agent supervisor
3. the supervisor validates the request against policy, budgets, and caller identity
4. the supervisor either reuses an existing worker or calls the isolation service to create a new one
5. the supervisor returns a structured worker assignment record
6. work orders then move over the event bus to the selected worker

That gives the system three distinct layers:

- **3PO / shell**: human-facing intent and task decomposition
- **agent supervisor**: deterministic spawn and lifecycle policy
- **isolation service**: privileged Linux uid, cgroup, nftables, and process mechanics

This layering matters. Without it, 3PO ends up acting like a privileged process manager, which is exactly the kind of soft boundary that becomes spaghetti once more agents are added.

---

## Trust model

### What the supervisor trusts

The supervisor trusts:

- kernel-attributed caller identity from `SO_PEERCRED`
- its local configuration for worker profiles, budgets, and policy
- authoritative worker state it records itself
- authoritative agent process state returned by the isolation service

### What the supervisor does not trust

The supervisor does not trust:

- self-reported uid, role, or authority in request payloads
- free-form natural-language justification as a source of policy
- arbitrary command lines from callers
- caller claims that an existing worker is safe to reuse

### Security posture

The supervisor should be fail-closed:

- unknown caller -> reject
- unknown worker profile -> reject
- budget violation -> reject
- ambiguous reuse decision -> create a narrower new worker or reject
- isolation-service failure -> return an error, do not partially continue

The design goal is to make worker creation policy deterministic in the same way the admin agent makes privileged approval structurally constrained.

---

## Relationship to adjacent services

### Isolation service

The isolation service remains the only service that actually:

- creates agent users
- creates systemd slices/units
- applies per-uid network policy
- tears agents down at the Linux level

The supervisor never reaches around it. It composes it.

The isolation service owns operating-system mechanics.
The supervisor owns policy about *which* worker should exist and *when*.

### Admin agent

The supervisor is not the admin agent.

The admin agent answers:
- should an already-running agent be allowed to perform a privileged action?

The supervisor answers:
- should a caller be allowed to obtain or reuse a worker with this predefined sandbox profile?

No LLM belongs in the supervisor decision path. If a human-facing workflow needs ambiguity resolution before requesting a worker, that belongs in 3PO. If a privileged action is needed after a worker is running, that belongs in the admin agent.

### Event bus

The supervisor should publish lifecycle events to the event bus, but it should not use the bus as its control API.

Use the event bus for:

- `agent.spawn.requested`
- `agent.spawn.accepted`
- `agent.spawn.rejected`
- `agent.lifecycle.assigned`
- `agent.lifecycle.reused`
- `agent.lifecycle.terminated`

Use the supervisor socket for request/response control operations where the caller needs a synchronous answer.

### Shell and 3PO

The shell and 3PO are normal clients of the supervisor. Neither receives special privileges by convention alone.

The shell may be allowed to request broader classes of worker than 3PO, but that distinction should come from explicit policy keyed to peer uid or role configuration, not from unstructured request text.

---

## Runtime model

The supervisor should run as a root-owned daemon under its own systemd service. Its socket should live under `/run/agent-os/` and use peer credentials for caller attribution.

Recommended shape:

- service: `agent-supervisor`
- socket: `/run/agent-os/agent-supervisor.sock`
- transport: Unix socket, newline-delimited JSON
- caller attribution: `SO_PEERCRED`
- authorization: policy keyed to peer uid and allowed request classes

The supervisor may keep in-memory state for active worker leases and session bookkeeping, but that state is operational, not conversational. It should be reconstructible from authoritative service state or persisted records if the daemon restarts.

---

## Responsibilities

The supervisor should own the following responsibilities and no more:

- map callers to allowed worker profiles
- apply spawn budgets per session and per caller
- decide reuse vs new worker vs reject
- allocate stable logical worker identities for the session layer
- translate worker profiles into isolation-service requests
- terminate workers when leases end or sessions close
- publish lifecycle events for observability

The supervisor should not:

- perform LLM inference
- parse open-ended task text to derive policy
- invent one-off sandbox settings from natural language
- execute general user commands directly
- bypass the isolation service or admin agent

---

## Worker profiles

The supervisor should only create workers from predefined profiles. A profile is the unit of policy.

A profile should include:

- profile name
- runtime class
- allowed tool set
- default CPU and memory limits
- network policy
- watched paths
- compositor grant requirements, if any
- maximum lease duration
- whether the profile is reusable or always ephemeral

Example profile:

```json
{
  "profile": "repo-inspector",
  "runtime": "local-llm",
  "tools": ["fs.read", "git.diff", "ripgrep"],
  "cpu_quota": "50%",
  "memory_max": "2G",
  "net_access": "deny",
  "watch_paths": ["/repo"],
  "max_lease_seconds": 900,
  "reuse_policy": "session"
}
```

Callers request a named profile. They do not get to supply arbitrary command lines, nft rules, or cgroup values.

---

## Reuse policy

Reuse should be conservative. Reusing too little costs performance; reusing too much silently broadens authority and muddies audit trails.

A worker may be reused only when all of the following are equivalent:

- same profile name
- same requester session
- same or narrower input scope
- same network policy
- same watched paths
- same compositor grant scope
- same runtime class
- current lease still valid
- worker status is healthy

If any of those differ, the default is to create a new worker.

### Safe reuse cases

- repeated repository inspection within one session
- iterative code edits against the same repo scope and profile
- continuing a bounded review/debugging thread where the sandbox is unchanged

### Force-new cases

- broader filesystem scope than the current worker has
- different network policy
- additional compositor grants
- switching from deterministic runtime to local-LLM runtime
- caller changes from shell to 3PO or vice versa when policy differs
- previous worker is in an errored or uncertain state

### Termination policy

The supervisor should terminate workers when:

- the lease expires
- the owning session ends
- the caller explicitly releases the worker
- policy requires one-shot execution
- the worker becomes unhealthy or disconnected from the session model

Long-lived workers should still have explicit leases rather than becoming ambient background daemons forever.

---

## Budgets and quotas

The supervisor should enforce two kinds of limits.

### Session budgets

These cap how much worker activity one user-facing session can accumulate.

Examples:

- maximum concurrent workers
- maximum total worker leases
- maximum total runtime per session
- maximum number of frontier escalations back to 3PO

### Caller budgets

These cap what a specific caller uid may request globally.

Examples:

- 3PO can request `repo-inspector`, `patch-writer`, `ui-observer`
- shell can additionally request diagnostic or administrative observer profiles
- non-shell agent uids may be denied supervisor access entirely

The supervisor should reject requests that exceed budget instead of silently queueing or degrading them unless an explicit queueing policy is defined.

---

## Initial control API

The initial API should stay small and synchronous.

### `ensure_worker`

Purpose:
- return an existing compatible worker or create a new one

Request:

```json
{
  "session_id": "sess_01",
  "request_id": "req_01",
  "worker_profile": "repo-inspector",
  "objective": "Review task 615 and summarize risks.",
  "inputs": {
    "repo_path": "/repo",
    "task_id": 615
  },
  "lease_seconds": 600,
  "reply_topic": "agent.work.result.req_01"
}
```

Response:

```json
{
  "worker_id": "worker_01",
  "created": true,
  "agent": {
    "uid": 60021,
    "name": "repo-inspector-sess-01",
    "status": "running"
  },
  "profile": "repo-inspector",
  "lease_expires_at": "2026-04-12T15:00:00Z",
  "assignment_topic": "agent.work.assign.worker_01"
}
```

### `release_worker`

Purpose:
- release the caller's lease on a worker; may trigger terminate if no lease remains

Request:

```json
{
  "session_id": "sess_01",
  "worker_id": "worker_01"
}
```

### `terminate_worker`

Purpose:
- force termination of a worker the caller is authorized to manage

Request:

```json
{
  "session_id": "sess_01",
  "worker_id": "worker_01",
  "reason": "user_cancelled"
}
```

### `list_workers`

Purpose:
- return workers visible to the caller for the current session or policy scope

Request:

```json
{
  "session_id": "sess_01"
}
```

### `describe_profiles`

Purpose:
- let clients discover what named worker profiles they are allowed to request

Request body:
- empty object

This keeps the contract explicit and avoids clients hard-coding profile assumptions they are not actually allowed to use.

---

## Internal data model

The supervisor should track a small, explicit set of records.

### Worker lease

```json
{
  "worker_id": "worker_01",
  "agent_uid": 60021,
  "profile": "repo-inspector",
  "owner_session_id": "sess_01",
  "requester_uid": 60010,
  "lease_expires_at": "2026-04-12T15:00:00Z",
  "assignment_topic": "agent.work.assign.worker_01",
  "state": "running"
}
```

### Profile grant

```json
{
  "requester_uid": 60010,
  "allowed_profiles": ["repo-inspector", "patch-writer"],
  "max_concurrent_workers": 3,
  "max_lease_seconds": 1800
}
```

### Reuse decision

This should be computed deterministically from profile, scope, grant, and current lease state. It should not be an opaque heuristic.

---

## Logging and observability

The supervisor should log:

- requester uid from peer credentials
- requested profile
- reuse vs create vs reject decision
- reason for rejection
- resulting worker id and agent uid when successful
- lease release and termination events

This log can be ordinary structured service logging rather than an append-only audit log. The append-only requirement belongs to the admin agent and audit service. The supervisor still needs strong observability, but not a new special logging rule.

It should also publish lifecycle events to the event bus so the shell and later compositor can observe worker allocation in real time.

---

## Failure handling

The supervisor should prefer explicit failure over partially successful state.

Examples:

- if isolation-service spawn fails, do not create a supervisor-side lease record
- if event publication fails, the spawn result still stands, but the log should record the missed lifecycle publication
- if the supervisor restarts, it should rebuild worker state from the isolation service or terminate orphaned leases conservatively
- if a worker misses its lease heartbeat or expires, the supervisor should terminate it rather than assume it is still correctly attached to session state

---

## Suggested implementation shape

When this moves from design to code, the package layout should stay boring and explicit:

- `cmd/agent-supervisor/main.go` for wiring only
- `internal/supervisor/service.go` for request handling
- `internal/supervisor/policy.go` for grants, budgets, and reuse rules
- `internal/supervisor/state.go` for lease tracking
- shared wire contracts in a tracked schema package

The important pattern is separation of concerns:

- transport and peer attribution
- policy evaluation
- state tracking
- isolation-service client calls
- event publication

Do not put all of that back into a single `main.go` just because it starts small.

---

## Why this service matters

The supervisor is the piece that keeps the 3PO/R2 architecture from collapsing into either of two bad shapes:

- 3PO becoming an implicit privileged process manager
- the isolation service accreting session policy, worker reuse logic, and caller-specific orchestration concerns

It preserves a clean split:

- 3PO decides what work is needed
- the supervisor decides what worker is allowed to exist
- the isolation service makes that worker real at the Linux level
- the admin agent governs privileged actions that occur after the worker is already running

That boundary will make the implementation easier for future agents to extend without smearing policy across the whole system.
