# Build Phases

The compositor is the hardest component and the least novel. Start with the coordination model, which can be built and validated entirely without a compositor. Each phase produces something runnable and testable.

---

## Phase 1 — Coordination model (no compositor)

**Goal:** Prove the agent isolation + admin agent design works. No display server involvement. Agents are processes; the "desktop" is a terminal.

**What gets built:**

1. **Agent Isolation Service** — create/destroy agent uids, set up systemd slices with cgroup limits, apply nftables uid rules for network filtering. Expose a simple Unix socket control API: `spawn_agent`, `terminate_agent`, `list_agents`.

2. **Admin Agent Daemon** — Unix socket server, structured JSON request/response schema, stateless LLM evaluation, append-only request log. System prompt loaded from a file at startup.

3. **Audit Service (basic)** — fanotify watches on a configurable set of paths, events tagged by uid, streamed to stdout and an append-only log file. No real-time subscriber support yet — just prove the attribution works.

**Test scenario:** Spawn an agent as uid 1001. Give it a shell. Observe that its filesystem writes are attributed in the audit log, its network access is blocked by the nftables rules, and its cgroup limits cap its resource usage. Have it submit an escalation request to the admin agent daemon and observe the structured evaluation and log entry.

**Does not require:** compositor, webview, or any display server changes.

**This phase also directly informs QuillForge** if any of that work involves agent-side task execution or privilege management.

---

## Phase 2 — Compositor integration

**Goal:** Agent surfaces are owned by agent uids; the compositor mediates access. A human user can see and interact with all surfaces; an agent can only affect surfaces it owns unless explicitly granted access.

**What gets built:**

1. **Compositor Bridge Service (Go)** — Wayfire bridge daemon speaking a Unix-socket protocol to a thin in-process Wayfire plugin. Translates compositor events to event bus messages and pushes policy/grant-cache updates down to the plugin. Exposes compositor control (close/move surface, restrict input) to other services.

2. **Event Bus** — custom Unix socket broker. Services publish and subscribe to typed events. Compositor bridge, audit service, and isolation service all connect.

3. **Surface ownership model** — agent-spawned processes run as agent uids; the compositor bridge tracks which surfaces are owned by which uid; access mediation is enforced via compositor APIs.

**Test scenario:** Spawn an agent as uid 1001. Have it open a window (WebKitGTK or any Wayland client). Observe that the compositor bridge correctly attributes the surface to uid 1001. Attempt to have the agent interact with a surface owned by uid 0 (the human user) — observe the denial.

---

## Phase 3 — Webview shell

**Goal:** Windows are web apps. The standard interface for agent-created UI is HTML/TypeScript. Inter-window communication flows through the compositor bridge rather than the clipboard.

**What gets built:**

1. **Webview launcher** — minimal process that takes a URL or local path and opens it as a WebKitGTK Wayland window. This is the "native window" for an agent's UI output. Process is owned by the agent's uid.

2. **Event bus web bridge** — WebSocket gateway that lets webview content subscribe to and publish event bus messages. Each webview window gets a connection scoped to its owning agent's uid.

3. **Basic shell UI** — a human-side webview that shows running agents, their surfaces, audit events, and pending escalation requests.

**Test scenario:** An agent spawns a webview window with a simple UI. It sends a message via the event bus web bridge to another agent's window. The human shell UI shows both agents, their surfaces, and their recent audit activity.

---

## Phase 4 — Hardening and depth (ongoing)

Things to add as the system matures, in rough priority order:

- **eBPF upgrade for audit** — replace/augment fanotify with eBPF to correlate filesystem events with LLM API calls (see AgentSight, arXiv 2508.02736). Gives visibility into what an agent is *saying* as well as what it's *doing*.
- **Admin agent UI** — surface pending escalation requests in the shell UI rather than just logging them. The "approve?" flow from `idea.md`.
- **Agent-to-agent communication protocol** — structured message passing between agent uids via the event bus, with the compositor bridge enforcing that agents can't forge sender identity.
- **wpe-webkit surface ownership** — move from "webview as Wayland client" to "compositor-managed wpe-webkit surface." This is the architectural purity point from `idea.md` and requires deeper compositor work (may necessitate moving from Pinnacle to a custom Smithay compositor).
- **Custom compositor** — if Pinnacle's gRPC API proves too limited, fork or replace with a Smithay-based compositor where agent coordination is a first-class primitive. This is the long-horizon project.

---

## Repository structure (suggested)

```
agora-os/
  cmd/
    isolation-service/    # main.go for the agent isolation daemon
    admin-agent/          # main.go for the admin agent daemon
    audit-service/        # main.go for the audit relay daemon
    event-bus/            # main.go for the event bus broker
    compositor-bridge/    # main.go for the Wayfire bridge daemon
    webview-launcher/     # main.go for the webview window launcher
  internal/
    agent/                # agent lifecycle types and management
    schema/               # shared request/response schemas
    bus/                  # event bus types and client library
  compositor/
    wayfire-plugin/       # thin C++ enforcement/data-extraction plugin
  shell/
    src/                  # TypeScript shell UI
  config/
    admin-agent-system-prompt.md   # edited out-of-band
    default-nftables.conf
  docs/
    idea.md
    plan.md
    phases.md
    research.md
```

---

## Starting point

Build **Phase 1** first. The isolation service and admin agent daemon are independent of everything else and prove the most novel part of the design. A working Phase 1 is:

- A runnable system on any standard Linux install with systemd + nftables
- Demonstrates agent uid isolation, resource limits, network access control, and privilege escalation with a structured audit trail
- Requires no compositor work, no Rust, no C++
- Produces reusable infrastructure (the admin agent pattern generalizes beyond this project)
