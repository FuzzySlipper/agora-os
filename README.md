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
| **Compositor bridge** | Not built yet. Phase 2 is planned around a Go bridge daemon plus a thin Wayfire plugin that handles credentials, events, and local enforcement. |

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
- the next compositor-facing path is Wayfire plus a thin in-process plugin and a Go bridge daemon over a Unix socket

## Repo layout

```
cmd/
  isolation-service/   Unix socket server for spawn/terminate/list
  admin-agent/         stateless LLM evaluator over a Unix socket
  audit-service/       fanotify event collector with uid attribution
  event-bus/           local pub/sub broker over a Unix socket
internal/
  admin/               admin-agent request handling and evaluation logic
  agent/               agent lifecycle orchestration and system integration
  audit/               audit service core and subscriber broker
  bus/                 event-bus types, matcher, broker, and client library
  isolation/           isolation-service request handling and authorization
  peercred/            SO_PEERCRED helpers
  schema/              shared socket paths, contracts, and typed domain values
config/
  admin-agent-system-prompt.md   the out-of-band prompt — edited only outside the running system
  default-nftables.conf
research/
  idea.md              the original thesis
  plan.md              architecture and component breakdown
  phases.md            build phases and acceptance criteria
  research.md          literature review
  local-agents.md      3PO/R2 architecture for local-first sub-agents
  3po-r2.md            concrete 3PO/R2 system design
  compositor-decision.md  Wayfire vs Pinnacle spike outcome
scripts/
  vm.sh                disposable Arch VM workflow
test/
  phase1.sh            end-to-end Phase 1 VM proof
  phase1-peercred.sh   focused SO_PEERCRED and authorization proof
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

**Don't run the privileged services on your host.** They create system users, modify nftables rules, and write under `/var/log/agent-os/`. The VM-first workflow is the intended development loop.

## License

MIT. See [`LICENSE`](LICENSE).
