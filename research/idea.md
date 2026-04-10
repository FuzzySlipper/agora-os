## Agent-Native Desktop Environment (working title)

### The core thesis

Current agent frameworks are bolted onto existing desktop environments designed for human users, then given workarounds to deal with the fact that Wayland doesn't let them spy on and puppet the display. The right approach inverts this: build a Wayland compositor from the ground up where agents are first-class citizens at the OS level, not afterthoughts managed by application-layer permission schemes.

### Display model: webview surfaces as native windows

The compositor's window surfaces are webviews (wpe-webkit or equivalent) rather than arbitrary GPU buffers. This gets you the ChromeOS architecture without Google's constraints — every "window" is a browser context, input is always handled locally at the compositor level so there's no display protocol remotely controlling a cursor, and work executes wherever makes sense. The compositor owns window management (tiling, grouping, focus) natively without fighting another DE's assumptions. Individual apps buy into the model by being web-native; legacy apps still work as standard Wayland clients since the compositor speaks standard Wayland to everything.

The webview-as-window model also gives the shared message bus for free — windows can communicate through the compositor's coordination layer rather than through fragile OS clipboard mechanisms or application-layer hacks.

### Agent isolation: Linux multi-user as the permission model

Each agent is a Linux system user with its own uid. This isn't a new permission system — it's surfacing what the kernel already provides for free: file ownership, process attribution, per-user cgroups for resource limits, iptables uid-match rules for network access control, and auditd for a complete action log. Every file write is attributed to an agent uid. Every process spawn is owned. inotify/fanotify wired into the compositor's event bus gives real-time visibility into what each agent is actually doing rather than requiring trust in agent self-reporting.

The compositor mediates surface access between human and agent users. An agent user can only affect Wayland surfaces it owns unless the human user explicitly grants viewport access for a specific task — a principled version of what today's computer-use agents do by screenshotting everything indiscriminately.

### Privilege escalation: the admin agent as sudo replacement

When an agent needs to operate beyond its uid's permissions, it can't escalate itself. Instead it submits a structured request to an admin agent — a purpose-built LLM that:

- **Has no user-facing input channel whatsoever.** It receives only structured system-originated requests. A compromised agent can write whatever reasoning it wants into the request body, but that reasoning is always the _subject_ of evaluation, never _instructions_. The prompt injection attack surface is structurally eliminated rather than defended against probabilistically.
- **Wipes context between every request.** No conversation history, no accumulated state. The only persistent influence on its behavior is the system prompt, which is edited exclusively out-of-band — outside the running system entirely, requiring administrative access. This means gradual poisoning across sessions is impossible.
- **Logs every interaction to append-only system files.** The audit trail is a side effect of normal operation, not an optional feature.
- **Errs toward blocking with explanation.** A false negative (blocking a legitimate request) produces a logged entry the operator can use to tune the system prompts. A false positive (approving a malicious escalation) is the actual threat, so the asymmetry should be built into the default behavior.

As an optional extension, the same admin agent can pre-screen elevation requests before they reach the human user — transforming "do you want to allow this?" into "agent X is requesting write access to /etc/hosts because it's trying to resolve a domain conflict it encountered while executing the task you gave it 4 minutes ago — approve?" That's actionable information rather than a meaningless UAC dialog.

### Relation to existing work

The research space has pieces of this but not the combination. SEAgent (January 2026) proposes a mandatory access control framework for LLM agents using attribute-based access control, monitoring agent-tool interactions via an information flow graph [arXiv](https://arxiv.org/abs/2601.11893) — but it's an application-layer framework bolted onto existing systems, not an OS-level design. Prompt Flow Integrity proposes agent isolation, secure untrusted data processing, and privilege escalation guardrails [arXiv](https://arxiv.org/abs/2503.15547) — again software layer, not compositor layer. The broader research consensus is shifting toward deterministic system-level defenses rather than probabilistic ML-based detection, because ML detectors are susceptible to adversarial attacks and LLM-based defenders fail against cascading injection [arXiv](https://arxiv.org/html/2601.11893v1) — which is precisely why the stateless out-of-band admin agent design is interesting: it's deterministic in its isolation properties rather than probabilistic. The "LLM as OS" framing exists in academic work (AIOS) but targets process scheduling, not the compositor and display layer. Nobody has built the compositor as the coordination primitive.

On the Wayland side, the River compositor project is exploring separation of the compositor and window manager into distinct programs with a documented protocol between them [The Register](https://www.theregister.com/2026/02/11/river_wayland_with_wms/), which is architecturally relevant — a non-monolithic compositor is much easier to extend with agent-aware coordination logic. The xx-zones protocol just merged into Wayland, introducing per-client coordinate systems that let multiple windows of the same application share coordinate space and layer relative to each other [Phoronix](https://www.phoronix.com/news/Wayland-Experimental-Zones), which directly supports the multi-agent window coordination model.

### What makes this a project rather than a paper

The component technologies all exist: wpe-webkit for webview surfaces, wlroots or a custom implementation for the compositor, standard Linux uid/cgroup/audit infrastructure for agent isolation, any capable LLM via API for the admin agent. The novel contribution is the architectural decision to treat all of these as a single coherent design rather than independent layers that have to negotiate with each other. The hard part isn't any individual component — it's building the compositor as the coordination primitive that makes the rest composable.