# Agora OS

An agent-native desktop environment. The thesis: current agent frameworks are bolted onto desktops designed for human users, then given workarounds to fight Wayland's refusal to let them spy on and puppet the display. Invert the model — build a Wayland compositor where agents are first-class citizens at the OS level, with isolation, permission, and audit primitives surfaced from the Linux kernel rather than reinvented in application-layer permission schemes.

This repo is the system-services and bridge layer of that idea. The compositor work comes later, but the Phase 2 direction is now Wayfire plus a thin plugin bridge rather than Pinnacle.

## The pieces

| Component | Role |
|---|---|
| **Agent isolation service** | Each agent is a Linux system user with its own uid. The service creates/destroys those uids, sets per-agent cgroup v2 limits via systemd transient slices, and applies per-uid `nft meta skuid` rules for network access control. |
| **Admin agent daemon** | Privilege escalation gateway. A stateless LLM evaluator that receives structured requests over a Unix socket, evaluates each independently against an out-of-band system prompt, and returns approve/deny/escalate. No conversation history, no user-facing input channel, no in-band prompt updates. |
| **Audit service** | fanotify watches on agent-writable paths; events are attributed by uid via `/proc/<pid>/status`, structured, and written to an append-only log. |
| **Event bus** | Local Unix-socket pub/sub broker for typed events such as audit activity, agent lifecycle changes, and later compositor surface events. Subscriber-visible sender uids are broker-stamped from `SO_PEERCRED`, not trusted from payload claims. |
| **Compositor bridge** | Phase 2 is split between a root-owned Go bridge daemon and a thin Wayfire plugin. The bridge owns typed Unix-socket translation between Wayfire surface events, event-bus publication, plugin policy-cache updates, forced surface close controls, and root-approved viewport grants recorded to an append-only log. |

The novel contribution isn't any one of these in isolation. It's the integration of all of them into a single OS-level coordination model rather than a stack of application-layer frameworks bolted onto an existing desktop.

The longer literature review is in [`research/research.md`](research/research.md). The original framing is in [`research/idea.md`](research/idea.md). The component breakdown is in [`research/plan.md`](research/plan.md). The build phases are in [`research/phases.md`](research/phases.md).

## Status

Phase 1 is now real enough to exercise end-to-end in the disposable VM:
- spawn an agent with per-uid isolation and resource limits
- prove nftables blocks its network access
- prove audit events are attributed to its uid
- submit a privilege escalation request and observe the append-only admin log

Phase 2 planning is also more concrete now:
- the compositor spike concluded that Pinnacle's current gRPC API does not expose the enforcement primitives this project needs
- the first compositor-facing slice is now in place as a Wayfire plugin plus a Go bridge daemon speaking a typed Unix-socket protocol
- a minimal root-only `compositorctl` flow now records explicit viewport grants and drives compositor input context for Phase 2 testing before an agent may interact with a human-owned surface

## Repo layout

```
cmd/
  isolation-service/   Unix socket server for spawn/terminate/list
  admin-agent/         stateless LLM evaluator over a Unix socket
  audit-service/       fanotify event collector with uid attribution
  event-bus/           local pub/sub broker over a Unix socket
  event-bus-web/       WebSocket gateway for token-scoped webview and shell clients
  compositor-bridge/   Wayfire bridge daemon for surface events and policy/control
  compositorctl/       root-only CLI for viewport grants, access checks, and input context
  webview-launcher/    minimal WebKitGTK launcher for agent-owned windows
internal/
  admin/               admin-agent request handling and evaluation logic
  agent/               agent lifecycle orchestration and system integration
  audit/               audit service core and subscriber broker
  bus/                 event-bus types, matcher, broker, and client library
  isolation/           isolation-service request handling and authorization
  compositor/          compositor-bridge state, protocol translation, and control handling
  peercred/            SO_PEERCRED helpers
  webview/             launcher orchestration and embedded WebKitGTK helper
  webbus/              signed-token WebSocket gateway and topic policy
  schema/              shared socket paths, service contracts, and 3PO/R2 protocol types
config/
  admin-agent-system-prompt.md   the out-of-band prompt — edited only outside the running system
  default-nftables.conf
research/
  idea.md              the original thesis
  plan.md              architecture and component breakdown
  phases.md            build phases and acceptance criteria
  research.md          literature review
  local-agents.md      3PO/R2 architecture for local-first sub-agents
  agent-slot-inventory.md  deterministic vs. LLM slot inventory
  3po-r2.md            concrete 3PO/R2 system design
  agent-supervisor.md  deterministic supervisor between 3PO and isolation
  compositor-decision.md  Wayfire vs Pinnacle spike outcome
scripts/
  vm.sh                disposable Arch VM workflow
  provision-phase2-vm.sh  guest-side Wayfire/plugin dependency installer
compositor/
  wayfire-plugin/      thin C++ plugin for credential extraction and local input deny
test/
  phase1.sh            end-to-end Phase 1 VM proof
  phase1-peercred.sh   focused SO_PEERCRED and authorization proof
  phase2.sh            end-to-end Phase 2 Wayfire proof
```

## Build and test

Build locally:

```sh
go build ./cmd/...
```

For authoritative Phase 1 validation, use the disposable VM:

```sh
scripts/vm.sh start
scripts/vm.sh ssh -- 'cd /repo && go build ./cmd/...'
scripts/vm.sh ssh -- 'cd /repo && sudo test/phase1.sh'
scripts/vm.sh ssh -- 'cd /repo && sudo test/phase1-peercred.sh'
scripts/vm.sh stop
```

For authoritative Phase 2 validation, use the VM as the disposable guest environment. A typical setup loop is:

```sh
scripts/vm.sh start
scripts/vm.sh phase2-deps
scripts/vm.sh ssh -- 'cd /repo/compositor/wayfire-plugin && meson setup build && meson compile -C build'
scripts/vm.sh stop
scripts/vm.sh snap phase2-deps
```

For live compositor validation, restore that snapshot and boot the guest with graphics:

```sh
scripts/vm.sh restore phase2-deps
scripts/vm.sh gui
```

Use the headless VM path for Phase 1 and guest-side dependency setup. Use the graphical guest path when the task needs a real Wayfire session or `test/phase2.sh`. The goal is still to keep host interaction unprivileged after the one-time `scripts/vm.sh build` and do the risky compositor setup/testing inside the guest.

Phase 3 also introduces `cmd/webview-launcher`, which opens a WebKitGTK window as a normal Wayland client and mirrors its own lifecycle onto the event bus. Example usage: `webview-launcher --url=https://example.com` or `webview-launcher --path=./index.html`. It expects `python3` plus GTK/WebKit GI bindings in the guest runtime. Those `compositor.surface.*` messages are advisory convenience signals for shell/UI work; when the compositor bridge is present, it remains the authoritative source of surface ownership and policy decisions.

`cmd/event-bus-web` is the companion gateway for browser-style clients that cannot speak the Unix socket bus directly. It authenticates each WebSocket with a signed token, stamps published events with the authenticated uid via the trusted root-owned local bus connection, and filters subscriptions by identity. Programmatic clients should use `Authorization: Bearer <token>`; browser WebSocket clients should use `Sec-WebSocket-Protocol: agora.token.<token>`. Query-parameter tokens are intentionally unsupported. Origins default to strict same-origin, or can be overridden with the comma-separated `AGORA_WEBBUS_ALLOWED_ORIGINS` allow-list. The reserved bridge namespaces are `webview.broadcast.*` for shared channels and `webview.inbox.<uid>.*` for uid-scoped inboxes; human-shell tokens get the full feed.

**Don't run the privileged services on your host.** They create system users, modify nftables rules, and write under `/var/log/agent-os/`. The VM-first workflow is the intended development loop.

## License

MIT. See [`LICENSE`](LICENSE).
