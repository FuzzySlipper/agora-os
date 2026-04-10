# Agora OS

An agent-native desktop environment. The thesis: current agent frameworks are bolted onto desktops designed for human users, then given workarounds to fight Wayland's refusal to let them spy on and puppet the display. Invert the model — build a Wayland compositor where agents are first-class citizens at the OS level, with isolation, permission, and audit primitives surfaced from the Linux kernel rather than reinvented in application-layer permission schemes.

This repo is the system services layer of that idea. The compositor work comes later.

## The four pieces

| Component | Role |
|---|---|
| **Agent isolation service** | Each agent is a Linux system user with its own uid. The service creates/destroys those uids, sets per-agent cgroup v2 limits via systemd transient slices, and applies per-uid `nft meta skuid` rules for network access control. |
| **Admin agent daemon** | Privilege escalation gateway. A stateless LLM evaluator that receives structured requests over a Unix socket, evaluates each independently against an out-of-band system prompt, and returns approve/deny/escalate. No conversation history, no user-facing input channel, no in-band prompt updates. The asymmetric "false positives are recoverable, false negatives are the actual threat" bias is baked in. |
| **Audit service** | fanotify watches on agent-writable paths; events are attributed by uid via `/proc/<pid>/status`, structured, and written to an append-only log. An eBPF upgrade is on the roadmap to also observe TLS-decrypted LLM traffic (AgentSight pattern). |
| **Compositor** | Not yet built. A spike will pick between Pinnacle (Rust + gRPC, language-agnostic) and Wayfire (C++ plugin, deeper hooks) based on whether agent coordination logic can live entirely out-of-process behind a wire protocol. |

The novel contribution isn't any one of these — each has precedent (IsolateGPT, CaMeL/PCAS, AgentSight, Pinnacle). It's the integration of all four into a single OS-level coordination model rather than a stack of application-layer frameworks bolted onto an existing desktop.

The longer literature review is in [`research/research.md`](research/research.md). The original framing is in [`research/idea.md`](research/idea.md). The component breakdown is in [`research/plan.md`](research/plan.md). The build phases are in [`research/phases.md`](research/phases.md).

## Status

Early. Phase 1 sketches of the three Go daemons exist under `cmd/`. They are not wired together end-to-end yet, and several Phase 1 gaps (process execution, nft rule cleanup, base-chain bootstrap, CLI client, integration test) are tracked in den. The next concrete milestone is a runnable end-to-end Phase 1: spawn an agent inside a VM, observe its filesystem writes attributed to its uid in the audit log, observe nftables blocking its network access, and submit a privilege escalation request that returns a structured decision.

## Repo layout

```
cmd/
  isolation-service/   Unix socket server for spawn/terminate/list
  admin-agent/         stateless LLM evaluator over a Unix socket
  audit-service/       fanotify event collector with uid attribution
internal/
  agent/               agent lifecycle: useradd, slices, nft rules
  schema/              wire types shared across services
config/
  admin-agent-system-prompt.md   the out-of-band prompt — edited only outside the running system
research/
  idea.md              the original thesis
  plan.md              architecture and component breakdown
  phases.md            build phases and acceptance criteria
  research.md          literature review
  local-agents.md      3PO/R2 architecture for local-first sub-agents
```

## Build and run

```sh
go build ./cmd/...
```

**Don't run these on your host.** The system services need root (`useradd`, `nft`, `systemd-run --uid`) and are designed to run inside a disposable VM. The intended target is an Arch VM driven by raw qemu — bootstrap script forthcoming. See `AGENTS.md` for the workflow.

## License

TBD.
