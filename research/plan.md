# Agent-Native Desktop Environment — Project Plan

## Guiding principle

The novel contribution is the **coordination model**, not the compositor implementation. Build the coordination model first, prove it works, then add the compositor layer. A custom compositor is a late refinement, not a prerequisite.

---

## Language decisions

| Layer | Language | Rationale |
|---|---|---|
| System services (isolation, admin agent, audit, event bus) | **Go** | First-class Linux system library support; single static binary for daemons; readable/reviewable; most of the Linux infrastructure tooling this integrates with is itself Go |
| Compositor plugin/bridge | **C++ (Wayfire) or Rust (Pinnacle)** | Non-negotiable — compositor plugins must be in the host language. Keep this surface small. |
| App/window UI layer | **TypeScript + HTML/CSS** | Webview content is web tech; agents write excellent frontend code; reviewable regardless of systems background |
| Compositor config/scripting | **Lua or Rust** (Pinnacle), **C++ plugin** (Wayfire) | Whatever the compositor exposes |

C# is **not** used for this project. The system service layer requires direct cgroup v2, netlink, fanotify, and Unix socket work where Go's libraries are first-class and C# requires P/Invoke wrappers. Keep C# for QuillForge and application-layer projects.

---

## System architecture

```
┌─────────────────────────────────────────────────────┐
│                  Webview App Layer                  │
│         (TypeScript apps as Wayland clients)        │
└────────────────────┬────────────────────────────────┘
                     │ Wayland protocol
┌────────────────────▼────────────────────────────────┐
│              Compositor (Pinnacle / Wayfire)         │
│         surface ownership · access mediation        │
└────────┬───────────────────────────────┬────────────┘
         │ gRPC / plugin API             │ events
┌────────▼────────┐             ┌────────▼────────────┐
│  Compositor     │             │    Event Bus         │
│  Bridge (Go)    │             │    (Go daemon)       │
└────────┬────────┘             └────────┬────────────┘
         │                               │
┌────────▼───────────────────────────────▼────────────┐
│              Agent Isolation Service (Go)            │
│   uid management · cgroups · nftables · systemd     │
└────────────────────────┬────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────┐
│               Admin Agent Daemon (Go)                │
│  Unix socket · structured schema · LLM API · logs   │
└─────────────────────────────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────┐
│                  Audit Service (Go)                  │
│           fanotify · auditd · append-only log        │
└─────────────────────────────────────────────────────┘
```

---

## Component breakdown

### Agent Isolation Service

Manages the lifecycle of agent Linux users. Creates/destroys agent uids, configures their resource and network constraints, and provides a control API for the rest of the system.

**Responsibilities:**
- Create/destroy agent system users (`useradd`/`userdel` or direct `/etc/passwd` management)
- Create systemd transient user slices with cgroup v2 resource limits (CPU, memory, IO)
- Apply per-uid nftables rules for network access control
- Configure auditd watches for agent uid activity
- Expose a control API (gRPC or Unix socket) for spawning/terminating agent sessions

**Key Go libraries:**
- `github.com/containerd/cgroups/v3` — cgroup v2 management via dbus and cgroupfs
- `github.com/coreos/go-systemd/v22/dbus` — systemd transient unit/slice creation
- `github.com/google/nftables` — nftables rules including uid-based network filtering
- `golang.org/x/sys/unix` — low-level syscalls where needed
- Standard library `net` — Unix socket control API

---

### Admin Agent Daemon

The privilege escalation gateway. Receives structured requests from agents over a Unix socket, evaluates them against an out-of-band system prompt using an LLM API, returns structured responses, and logs everything to an append-only file.

**Design invariants (non-negotiable):**
- No conversation history passed to the LLM. Every request is evaluated independently.
- System prompt is read from a file at startup only, never updated by any running process.
- All requests and responses are logged before the response is returned.
- The socket is a server socket owned by root; agent uids can connect and write requests, not receive others' requests.
- Request schema is the only input surface. Agent reasoning in the request body is the *subject* of evaluation, not instructions.

**Request schema (draft):**
```json
{
  "agent_uid": 1001,
  "task_context": "string — what the agent is trying to accomplish",
  "requested_action": "string — specific action requiring elevation",
  "requested_resource": "string — path, service, or resource targeted",
  "justification": "string — agent's reasoning (evaluated, not trusted)"
}
```

**Key Go libraries:**
- Standard library `net` — Unix domain socket server
- `encoding/json` — request/response schema
- Anthropic Go SDK or `net/http` — LLM API calls
- Standard library `os` — append-only log file (`os.OpenFile` with `O_APPEND|O_CREATE`)

---

### Audit Service

Wires kernel audit events into a structured, queryable log attributed by agent uid. The compositor and other services subscribe to this for real-time visibility into agent activity.

**Responsibilities:**
- fanotify watches on agent-writable paths, annotated by uid
- auditd integration for syscall-level event capture
- Structured event stream (agent uid, timestamp, action, resource, outcome)
- Append-only persistent log; in-memory ring buffer for real-time subscribers

**Key Go libraries:**
- `golang.org/x/sys/unix` — fanotify setup and event reading
- `github.com/elastic/go-libaudit/v2` — auditd integration (used by Elastic Agent)
- Standard library `bufio` + `os` — append-only log

**Note on eBPF:** fanotify is sufficient for v1. eBPF via libbpf (see AgentSight, arXiv 2508.02736) gives significantly more power — can correlate filesystem events with LLM API calls by hooking SSL_read/SSL_write — but is a complexity step up. Natural upgrade path once the simpler layer is working.

---

### Event Bus

Inter-service communication backbone. Services publish typed events; subscribers receive what they've registered for. Simple enough that a custom implementation is preferable to a full message broker for v1.

**Options:**
- **Unix domain socket broker (Go, custom)** — simplest, no external dependency, sufficient for local services
- **NATS** — more capable, but operationally heavier for a local-only bus
- **D-Bus** — already present on most Linux desktops; has the right semantics but awkward API

Recommendation: custom Unix socket broker for v1. NATS if the fan-out complexity grows.

---

### Compositor Bridge

The seam between the Go services layer and the compositor. Translates compositor events (surface created/destroyed, focus changed, input event) into event bus messages, and exposes a control interface for the services to affect the compositor (move/close surfaces, restrict input).

**Compositor choice:**

**Pinnacle (preferred for v1):** gRPC API is language-agnostic — the bridge is a Go gRPC client. No C++ or Rust in the main development loop. Trade-off: Pinnacle is newer and the gRPC API surface may not yet expose all the hooks needed for deep agent access control.

**Wayfire (fallback):** C++ plugin with hooks into all compositor events. More mature API surface. Requires writing and maintaining C++ — keep this plugin minimal (200–300 lines), focused purely on bridging events to a Unix socket that the Go bridge service reads.

---

### Webview App Shell

The window model. Each "window" is a Wayland client that renders HTML/CSS/TypeScript content. For v1, this is thin: a minimal process that launches a WebKit window as a Wayland client, connected to the event bus.

**For v1:** Standard WebKitGTK app as a Wayland client. Architecturally this is an ordinary Wayland window that happens to render web content — no compositor changes needed. Agent surfaces vs. human surfaces are distinguished by the uid of the owning process.

**Longer term:** wpe-webkit surfaces managed directly by the compositor rather than as independent Wayland clients. This is the architectural purity point from `idea.md` — defer until the coordination model is proven.

---

## Compositor choice: Pinnacle vs Wayfire

| | Pinnacle | Wayfire |
|---|---|---|
| Language | Rust/Smithay | C++/wlroots |
| Extension mechanism | gRPC API (language-agnostic) | C++ plugin API |
| Maturity | Newer, active development | Mature, v0.10.0 Aug 2025 |
| Go integration | Native (gRPC client) | Requires thin C++ plugin bridge |
| Hook depth | gRPC API surface (may have gaps) | Full compositor internals |

Start with Pinnacle for the clean Go integration. If the gRPC API lacks hooks needed for agent surface ownership mediation, fall back to Wayfire with a thin C++ bridge plugin.

---

## What this is not

- Not an OS. Runs on top of a standard Linux install (Arch, NixOS, or Fedora are natural fits given Wayland-first posture).
- Not replacing PAM or sudo entirely — the admin agent daemon operates alongside the existing privilege system.
- Not requiring custom kernel patches. Everything uses stable kernel interfaces (cgroups v2, nftables, fanotify, auditd).
- Not Wayland-incompatible with legacy apps. XWayland handles X11 apps transparently.
