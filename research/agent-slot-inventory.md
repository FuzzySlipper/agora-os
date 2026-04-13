# Agent Slot Inventory

This doc is a simple guardrail: not every "agent-shaped" component in Agora OS
should be an LLM.

The architecture gets easier to reason about when we classify each planned slot
by the minimum intelligence it actually needs:

- `deterministic daemon`: pure rules, explicit policy, no inference
- `tiny LLM (tool routing)`: narrow dispatch across a fixed tool/profile set
- `small LLM (light reasoning)`: bounded execution, summarization, code work, or
  UI interpretation without open-ended human dialogue
- `frontier (human-facing)`: ambiguity handling, intent translation, narrative
  synthesis, and other user-facing reasoning

The default bias should be downward. If a slot can be a small Go daemon, it
should not become a model because "agent" sounds more exciting.

## Current inventory

| Slot | Class | Why |
|---|---|---|
| Isolation service | deterministic daemon | Creates agent uids, slices, nftables rules, and process state from explicit requests. This is kernel/plumbing work, not reasoning. |
| Admin agent | small LLM (light reasoning) | Evaluates a rigid escalation schema and fail-closes to human review on uncertainty. It needs judgment, but not a human-facing frontier persona. |
| Audit service | deterministic daemon | Fanotify/event attribution, append-only logging, and subscriber fanout are pure systems work. |
| Event bus broker | deterministic daemon | Local pub/sub transport with peer-attributed sender identity is explicit protocol handling, not inference. |
| Compositor bridge service | deterministic daemon | Policy cache updates, surface tracking, grants, and control operations should stay explicit and reviewable. |
| Wayfire plugin | deterministic daemon | In-process credential extraction and input deny/allow are enforcement logic, not model work. |
| Webview launcher | deterministic daemon | Opens a WebKitGTK Wayland client and forwards its lifecycle. This is process/UI glue. |
| Event bus web bridge | deterministic daemon | A scoped WebSocket-to-bus gateway is protocol translation and authorization, not reasoning. |
| Agent supervisor | deterministic daemon | Reuse/spawn/lease policy must stay root-owned, explicit, and auditable rather than LLM-shaped. |
| Shell UI | deterministic daemon | The shell is a user interface, not a reasoning slot. It renders state and forwards structured requests. |
| 3PO ambassador | frontier (human-facing) | This is the one place that should absorb ambiguity, translate human intent, decide delegation shape, and synthesize results back into prose. |
| R2 deterministic adapters | deterministic daemon | Filesystem watchers, procfs liveness checks, nftables appliers, log appenders, and similar helpers should remain pure daemons. |
| R2 tool router | tiny LLM (tool routing) | If a worker only needs to pick from a small, fixed tool/profile menu, a tiny model is enough. Do not upgrade this class by default. |
| R2 repo inspector / code reviewer | small LLM (light reasoning) | This needs bounded code understanding and structured outputs, but not broad human-facing reasoning. |
| R2 patch drafter / code editor | small LLM (light reasoning) | Editing against a fixed repo/tool budget is a small-model worker problem, not a frontier problem. |
| R2 monitor / summarizer | small LLM (light reasoning) | Pattern extraction and summarization over logs/events fit the "small model with rigid outputs" bucket. |
| R2 UI observer | small LLM (light reasoning) | Interpreting a granted viewport is still bounded execution, even if it eventually uses a multimodal model. |

## Rules of thumb

### Keep deterministic slots deterministic

If a component is primarily:

- kernel mediation
- socket protocol handling
- file append / event relay
- lifecycle bookkeeping
- policy enforcement

then it should stay a deterministic daemon.

Adding an LLM there usually makes the system less legible, less auditable, and
harder for future agents to extend safely.

### Tiny models are for dispatch, not prose

Use a tiny model only when the real job is:

- choose one profile from a small set
- choose one tool from a known menu
- normalize a structured request into a known command shape

If the task starts needing long-form synthesis, uncertain tradeoffs, or broad
code reasoning, it is no longer a tiny-model slot.

### Small models should stay bounded

Small-model slots should have:

- rigid input/output contracts
- explicit tool budgets
- narrow authority
- easy escalation back to 3PO when uncertain

They are workers, not personalities.

### Frontier access should stay concentrated

Right now, only 3PO clearly deserves the `frontier (human-facing)` class.

That concentration is a feature:

- cost stays tied to actual human interaction
- ambiguous reasoning is localized
- the rest of the system stays auditable and easier to harden

The admin agent should remain separate from 3PO even if both happen to use
inference. Its job is structured privilege evaluation, not human-facing
conversation.

## Practical default for new slots

When adding a new component, ask in this order:

1. Can this be a deterministic daemon?
2. If not, can it be a tiny tool-routing model?
3. If not, can it be a small bounded worker?
4. Only then ask whether it truly needs frontier reasoning.

The burden of proof should be on moving upward.
