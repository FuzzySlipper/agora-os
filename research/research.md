# Agent-Native Desktop Environment — Research

Extended literature and projects beyond what `idea.md` already cites (SEAgent, Prompt Flow Integrity, AIOS, River compositor, xx-zones Wayland protocol).

---

## Academic Literature

### OS-Level Isolation & Sandboxing for Agents

**IsolateGPT / SecGPT** (arXiv 2403.04960, NDSS 2025)
Yuhao Wu, Franziska Roesner, Tadayoshi Kohno et al. (University of Washington). Hub-and-spoke execution isolation: each LLM app runs in its own domain; a controlled hub mediates all cross-domain communication. Application-layer version of the per-agent uid model — the paper itself motivates why OS-level primitives would be more robust.
https://arxiv.org/abs/2403.04960

**ceLLMate: Sandboxing Browser AI Agents** (arXiv 2512.12594, Dec 2025, UC San Diego / Google)
Sandboxes browser-using agents at the HTTP layer rather than at UI interaction events, on the basis that all side-effecting UI actions ultimately produce network traffic. Closest existing work to compositor-level mediation of surface access: both pick an infrastructure layer *below* the application as the interposition point.
https://arxiv.org/abs/2512.12594

**Systems Security Foundations for Agentic Computing** (arXiv 2512.01295, Dec 2025, Google researchers)
Applies classical systems security — threat models, formal guarantees, attacker models — to agentic systems; 11 real-world attack case studies. Explicitly argues most agentic AI safety work lacks formal guarantees and a systems perspective — exactly the gap the idea addresses by using kernel primitives.
https://arxiv.org/abs/2512.01295

**Fault-Tolerant Sandboxing for AI Coding Agents** (arXiv 2512.12806, Dec 2025)
Wraps agent actions in transactional filesystem snapshots — every agent action is atomic and rollback-capable. Proxmox-based EVPN/VXLAN network isolation, 14.5% overhead per transaction. Complements the audit-trail design: makes agent actions reversible rather than just logged.
https://arxiv.org/abs/2512.12806

**Quantifying Frontier LLM Capabilities for Container Sandbox Escape** (arXiv 2603.02277, 2026)
Empirically evaluates how capable frontier models are at actively escaping container sandboxes; finds container escape is a realistic near-term threat. Directly motivates the uid-based isolation model — uid separation has no container boundary to escape from; it uses the kernel's own identity system.
https://arxiv.org/html/2603.02277v1

---

### Least Privilege for Agents

**Towards Automating Data Access Permissions in AI Agents** (arXiv 2511.17959, IEEE S&P 2026)
Yuhao Wu, Franziska Roesner, Tadayoshi Kohno et al. User study on how people make permission decisions for AI agents; ML permission assistant achieving 85.1% overall accuracy (94.4% high-confidence). Quantifies what "erring toward blocking" actually means from a user's perspective — relevant to calibrating admin agent default behavior.
https://arxiv.org/abs/2511.17959

**A Probabilistic Authorization Framework for Least Privilege** (BDCAT '25, ACM DL)
Calculates permissions at runtime based on task scope, context, and intent rather than static roles. 39–46% reduction in actions and 31–39% reduction in risk vs. static role assignment. Application-layer equivalent of the uid+cgroup kernel model.
https://dl.acm.org/doi/pdf/10.1145/3773276.3776564

**AgentGuardian: Learning Access Control Policies to Govern AI Agent Behavior** (arXiv 2601.10440, Jan 2026)
Monitors agent execution traces during staging to learn legitimate baseline behaviors; derives adaptive policies for subsequent tool calls accounting for multi-step workflow dependencies. Behavioral baselining as a complement to the auditd-based audit trail.
https://arxiv.org/abs/2601.10440

**A Vision for Access Control in LLM-based Agent Systems** (arXiv 2510.11108, Oct 2025)
Treats access control as dynamic information-flow governance: redaction, summarization, and paraphrasing rather than binary allow/deny. The soft layer inside hard uid boundaries — what information *within* an agent's allowed scope should be shaped before delivery.
https://arxiv.org/abs/2510.11108

---

### Structural Prompt Injection Defenses

**CaMeL** (arXiv 2503.18813, Mar 2025, Google DeepMind)
Capability-based execution layer: a Privileged LLM generates a structured plan; a Quarantined LLM handles untrusted content with strict schema validation. All data values carry provenance + capability metadata; all tool invocations must pass explicit policy checks. Inspired by CFI, ACL, and IFC from software security. Solves 77% of AgentDojo tasks with provable security guarantees. Closest existing formal analog to the admin agent design — the extension in the idea is that enforcement lives in the compositor (OS layer) rather than a wrapping LLM.
https://arxiv.org/abs/2503.18813

**PCAS: Policy Compiler for Secure Agentic Systems** (arXiv 2602.16708, Feb 2026)
Models agentic system state as a causal dependency graph of events (tool calls, results, messages); policies expressed in a Datalog-derived language supporting transitive information flow and cross-agent provenance; reference monitor intercepts all actions before execution. Improves policy compliance from 48% to 93%. Closest existing work to a formal realization of "structured request to admin agent": policies defined out-of-band, enforced deterministically at a reference monitor.
https://arxiv.org/abs/2602.16708

**Design Patterns for Securing LLM Agents Against Prompt Injections** (arXiv 2506.08837, Jun 2025)
Catalogs structural patterns: Plan-Then-Execute (commit to plan before encountering untrusted data), Action-Selector (LLM translates intent to pre-approved actions only), LLM Map-Reduce (isolated sub-agents handle untrusted data, never the main agent). Plan-Then-Execute is particularly close to the admin agent: commit to a validated plan, execute it deterministically.
https://arxiv.org/abs/2506.08837

**StruQ** (arXiv 2402.06363, USENIX Security 2025, UC Berkeley)
Separates prompts and data into two channels with distinct formatting; fine-tunes LLM to only follow instructions in the prompt channel. <2% attack success rates under optimization-free attacks. Training-time analog to the structural separation in the idea: both recognize that a single channel carrying both instructions and data is the root problem.
https://arxiv.org/abs/2402.06363

**MELON: Provable Defense Against Indirect Prompt Injection** (arXiv 2502.05174, ICML 2025)
Runs the agent trajectory twice — once normally, once with the user prompt masked — and flags cases where behavior diverges significantly. Provides provable detection bounds (not just empirical) on the basis that successful injections make agent behavior dependent on malicious instructions rather than the user's task.
https://arxiv.org/abs/2502.05174

**Architecting Secure AI Agents** (arXiv 2603.30016, 2026, NVIDIA / Johns Hopkins)
Position paper arguing that context-dependent security decisions should only be made in designs that strictly constrain what the model can observe and decide; advocates transforming untrusted environmental feedback into narrow structured artifacts. Exactly the structural argument for the admin agent's "no user-facing input channel" design.
https://arxiv.org/abs/2603.30016

---

### Audit Trails and Agent Provenance

**Audit Trails for Accountability in Large Language Models** (arXiv 2601.20727, Jan 2026, Brown University)
Proposes tamper-evident, context-rich ledgers of lifecycle events; reference architecture with lightweight event emitters, append-only audit storage, and an auditor interface enabling cross-organizational traceability. Open-source Python implementation. Directly formalizes the "append-only system files" requirement.
https://arxiv.org/abs/2601.20727

**AgentSight: System-Level Observability for AI Agents Using eBPF** (arXiv 2508.02736, Aug 2025, ACM PACS)
Uses eBPF uprobes to hook `SSL_read`/`SSL_write` in OpenSSL to intercept decrypted LLM communications; kernel tracepoints (`sched_process_exec`, `openat2`, `connect`, `execve`) for system-wide effects; causally correlates both streams across process boundaries. Framework-agnostic, zero-instrumentation, <3% overhead. Detects prompt injection attacks, resource-wasting reasoning loops, and multi-agent coordination bottlenecks. The strongest existing implementation of the eBPF-as-agent-monitor concept — and demonstrates eBPF as a more powerful alternative to inotify/fanotify.
https://arxiv.org/abs/2508.02736

**Verifiability-First Agents** (arXiv 2512.17259, Dec 2025)
Cryptographic and symbolic attestations of agent actions; embedded Audit Agents compare intent vs. behavior at runtime; ISpec schema for encoding agent intent. ISpec is the formalized version of "system prompt edited exclusively out-of-band."
https://arxiv.org/abs/2512.17259

---

### Capability-Based Security for Agents

**CaMeL** — see Structural Defenses above; also the primary capability-based paper.

**Authenticated Delegation and Authorized AI Agents** (arXiv 2501.09674, Jan 2025)
Formal models for cryptographically authenticated delegation chains and revocation for AI agents. Relevant to the privilege escalation model: a formal basis for how admin agent approval constitutes a verifiable delegation of elevated authority.
https://arxiv.org/abs/2501.09674

**Governing Dynamic Capabilities: Cryptographic Binding for AI Agent Tool Use** (arXiv 2603.14332, Mar 2026)
Cryptographic binding of capabilities to specific agents and tasks, with reproducibility verification. Addresses that capability-based access control for AI agents must be dynamic (task-scoped) rather than static, because agents decide at runtime what capabilities they need.
https://arxiv.org/html/2603.14332

---

## Existing Projects

### Wayland Compositors with Programmable Architecture

**Smithay** (Rust, active)
Library of compositor building blocks — protocol implementations, input handling, rendering primitives — deliberately omits window management. Used by COSMIC compositor, Pinnacle, and others. Clean programmatic API to all compositor state; the right foundation for adding agent-aware coordination logic as a first-class component.
https://github.com/Smithay/smithay

**Pinnacle** (Rust/Smithay, active)
Compositor configured entirely via Lua or Rust over a gRPC API — every window management decision is an API call from an external process. Working implementation of River-style WM separation; an agent controlling Pinnacle would call its gRPC API rather than synthesizing input events, making its actions auditable by design. The gRPC boundary is language-agnostic: Go or C# services can drive it without Rust.
https://github.com/pinnacle-comp/pinnacle

**Wayfire** (C++, wlroots-based, active — v0.10.0 Aug 2025)
Fully modular compositor where all functionality is implemented as plugins via a stable C++ plugin API. Most mature extensibility mechanism in the wlroots ecosystem; agent coordination could be a plugin rather than a core feature.
https://github.com/WayfireWM/wayfire

**COSMIC compositor (cosmic-comp)** (Rust/Smithay, System76, active)
Compositor and shell are hot-swappable; compositor is architecturally decoupled from the rest of the desktop stack. Demonstrates that a Smithay-based compositor can be separated from the shell — the same decoupling needed between agent coordination logic and display management.
https://github.com/pop-os/cosmic-comp

**Mir / Miracle-WM** (C++, Canonical, active)
Canonical's compositor toolkit designed explicitly for shell authors building custom Wayland-based desktop environments. Window management is a high-level API, customizable without touching compositor internals. Narrower ecosystem than wlroots/Smithay.
https://github.com/canonical/mir

---

### WebView-as-Native-Window

**Tauri** (Rust, v2.0 released 2024, active)
Framework where application UI is a webview (WKWebView/WebView2/WebKitGTK) and business logic is Rust. Permission model is explicit: each plugin declares required capabilities, the app grants only what it needs. Working implementation of the "webview surfaces as native windows" model with a principled capability system. Not a compositor, but the closest existing thing to the architecture.
https://github.com/tauri-apps/tauri

*Note: No compositor exists that manages wpe-webkit surfaces as native Wayland surfaces with agent-aware coordination. This is a genuine gap in the existing landscape.*

---

### Agent Frameworks Using OS-Level Isolation

**E2B** (Apache-2.0, active)
Firecracker microVM per agent execution context, ~150ms cold start, hardware-level isolation. Network-isolated, destroyed on completion. The cloud analog of per-agent uid: hard isolation boundary, with the trade-off being cold-start latency vs. continuous process.
https://github.com/e2b-dev/E2B

**Anthropic sandbox-runtime** (Apache-2.0, 2026)
bubblewrap (unprivileged user namespaces) + bind mounts for filesystem isolation on Linux; sandbox-exec (Seatbelt) on macOS; HTTP/SOCKS5 proxy for network filtering. Reduces permission prompts 84% in internal usage. Closest open-source implementation of OS-level agent sandboxing, focused on a single process rather than a system-wide per-agent identity model.
https://github.com/anthropic-experimental/sandbox-runtime

**OpenAI Codex linux-sandbox** (open source, active)
bubblewrap + seccomp + Landlock (kernel 5.13+), applied with `PR_SET_NO_NEW_PRIVS`; enabled by default. The only major coding agent to sandbox by default. Well-engineered reference implementation of Landlock + seccomp for agent containment.
https://github.com/openai/codex/tree/main/codex-rs/linux-sandbox

**Kubernetes Agent Sandbox** (kubernetes-sigs, post-KubeCon NA 2025)
Defines Sandbox, SandboxTemplate, and SandboxClaim Kubernetes primitives. Built on gVisor with Kata Containers support; pre-warmed pools enable <1 second cold starts. Standardizes the API for agent isolation at the infrastructure layer.
https://github.com/kubernetes-sigs/agent-sandbox

---

### Capability Libraries for Agents

**Tenuo** (Rust, active)
Signed cryptographic capability tokens (Warrants) specifying which tools an agent can call, under what argument constraints, and for how long. Delegation is monotonically scoped — can only narrow authority, never expand it. Verification is ~27μs offline. Even under prompt injection, warrant bounds hold cryptographically. Integrates with LangChain, LangGraph, Google ADK, OpenAI. Object-capability implementation specifically for the LLM agent threat model.
https://github.com/tenuo-ai/tenuo

---

### Agent OS Projects

**AIOS: LLM Agent Operating System** (agiresearch/AIOS, COLM 2025)
AIOS kernel handles scheduling, context management, memory, storage, and access control for LLM agents. Up to 2.1x faster execution for agents. Primary existing "LLM as OS" implementation — targets process scheduling and resource management, not the display layer or compositor.
https://github.com/agiresearch/AIOS

**smartcomputer-ai/agent-os** (GitHub, active)
Includes AIR (Agent Intermediate Representation) as a typed control plane; capability security where all effects are scoped, budgeted, and gated by policy. More philosophically aligned with the idea than AIOS but application-layer only.
https://github.com/smartcomputer-ai/agent-os

---

## Adjacent Concepts

### eBPF vs. inotify/fanotify

inotify/fanotify gives filesystem events but cannot observe syscall arguments, filter network by uid, or intercept TLS-encrypted LLM API calls. eBPF via kprobes/uprobes can intercept any syscall with full argument inspection, trace network connections by uid, and hook `SSL_read`/`SSL_write` in userspace to observe decrypted LLM API calls. AgentSight (above) demonstrates this concretely.

Practical recommendation: use fanotify for lightweight filesystem attribution (it is uid-aware and low overhead), use eBPF for the higher-fidelity observability channel that correlates filesystem events with LLM API calls.

**Tetragon** (Isovalent/Cilium, open source): Production eBPF security observability and runtime enforcement. Can block specific syscalls for specific process trees in real time. Relevant as a component for the compositor's agent monitoring layer.
https://tetragon.io

---

### Capability-Based OS Designs

**Fuchsia OS** (Google, active)
Zircon kernel is capability-based: every system call requires an explicit capability argument; capabilities cannot be forged and can only be transferred through explicit delegation. Components declare all capability routes statically in Component Manifest Language. The most mature deployed capability OS. The per-agent uid model is a POSIX approximation of what Fuchsia does fully.
https://fuchsia.dev/fuchsia-src/concepts/principles/secure

**seL4** (CSIRO, formally verified)
The only OS kernel with machine-checked proof of functional correctness. Capability-based, isolation proofs extend to the hypervisor level. DARPA PROVERS program (started 2024) is funding work to make seL4-based development more accessible. The theoretical foundation for provable isolation — the uid model approximates it; seL4 can prove it.
https://sel4.systems

**Genode OS Framework** (open source, active — v25.02 Feb 2025)
Capability-based component OS that runs on seL4, NOVA, Fiasco.OC, and others. Each program runs in a dedicated sandbox with only explicitly granted capabilities; delegation can only narrow authority. Genode 25.02 extended multi-monitor capabilities to window management and VMs, and ports Chromium WebEngine — the most relevant existing system combining compositor + capability isolation, though not Wayland-based.
https://genode.org

**Qubes OS** (Xen-based, v4.3.0 released 2026)
Security-by-isolation OS using Xen hypervisor VMs as isolation boundaries. The architectural pattern maps directly onto agent isolation: each agent in its own Qube, compositor/dom0 mediates all inter-Qube communication. No existing project has applied Qubes-style isolation specifically to LLM agents.
https://www.qubes-os.org

---

### The "Confused Deputy" Problem for LLM Agents

The confused deputy problem (an authority-bearing program tricked into misusing its authority by a less-privileged caller) maps directly onto LLM agents: the agent holds legitimate credentials and acts on them, but its decisions can be influenced by prompt injection. The problem is that authorization (does the agent have permission?) divorces from authentication (who actually issued this instruction?).

The admin agent design structurally resolves this: the admin agent's authority cannot be confused because it has no instruction channel from untrusted sources. A requesting agent's reasoning is always the *subject* of evaluation, never the *source* of instruction. PCAS (above) provides the formal framework for this.

Best formal treatment in the existing literature: SEAgent (already cited in `idea.md`), Section 3. Also appears explicitly in the 2025 OWASP Top 10 for LLM Applications and in Systems Security Foundations (arXiv 2512.01295).

---

## Priority Reading

If grounding the idea in existing formal work, these five are most directly relevant:

1. **CaMeL** (arXiv 2503.18813) — closest formal analog to the admin agent design
2. **PCAS** (arXiv 2602.16708) — reference monitor + dependency graph for deterministic policy enforcement
3. **IsolateGPT** (arXiv 2403.04960) — execution isolation architecture, NDSS 2025
4. **AgentSight** (arXiv 2508.02736) — eBPF-based agent observability, concrete implementation
5. **Systems Security Foundations** (arXiv 2512.01295) — systems-security lens on agentic computing, 11 attack case studies

---

## What Genuinely Doesn't Exist

Across all of this, the following combination is absent from the literature and from any existing project:

1. A Wayland compositor built with agent coordination as a first-class primitive
2. Per-agent Linux uid as the identity and permission model surfacing kernel audit trails natively
3. A stateless, out-of-band admin agent as the privilege escalation mechanism with prompt injection surface structurally eliminated
4. Webview surfaces (wpe-webkit) as the native window model with a compositor-level message bus

Each component has precedent. The integration does not.
