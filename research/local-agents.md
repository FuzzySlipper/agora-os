# Local Agents & The 3PO/R2 Architecture

## The core thesis

Task-oriented sub-agents in this framework are doing **tool routing**, not general reasoning. An agent that receives a well-scoped task (translated by the ambassador layer from human intent) and calls tools from a known set doesn't need frontier intelligence. The failure modes of small models — hallucination, reasoning drift — mostly manifest in open-ended generation and long multi-step reasoning chains, not in "pick the right tool from a known set and call it correctly."

This enables a two-tier architecture where background agent activity costs electricity rather than API tokens.

---

## The 3PO/R2 split

**R2 agents** — task-oriented, local, specialized:
- Receive structured task descriptions
- Operate against a predefined tool set (filesystem, process management, network, cgroup control, etc.)
- Return structured results
- Run continuously in the background; cost is local inference

**3PO ambassador** — human-facing, frontier API:
- Translates human intent into structured agent-to-agent commands
- Synthesizes multi-agent logs and outputs into coherent human narrative
- Handles ambiguity, context inference, what-you-meant vs. what-you-said
- Invoked on user-facing operations only; cost is bounded by human attention

The cost structure inversion is the key property: background agents run all day for the cost of power; the expensive frontier model is only billed when a human is actively engaged. 10 agents running 8 hours of background work + 20 user-facing interactions = 20 API calls.

---

## What R2 agents need from a model

For OS-level sub-agents doing system operations:

| Task type | Intelligence required | Viable local size |
|---|---|---|
| File ops, process management, cgroup control | Reliable tool calling, minimal reasoning | 7B or smaller |
| Code editing / generation | Strong code understanding | 14B–32B coding-specialized |
| Monitoring / observation / summarization | Pattern matching, light summarization | 3B–7B |
| UI interaction (webview/visual) | Multimodal understanding | 11B vision model or Moondream 2B |
| Ambiguous/high-stakes escalation decisions | Genuine reasoning | 3PO layer (frontier API) |

### Deterministic agents

Not every sub-agent needs an LLM. The most common system operations in this framework are fully deterministic:
- "Watch this path and notify on change" → inotify daemon, zero inference
- "Apply this nftables rule" → shell command
- "Is this process still running?" → procfs read
- "Append to audit log" → file write

The right mental model is a spectrum from pure rule-based daemons → tiny LLMs for structured routing → small LLMs for reasoning-light task execution → frontier model for human interface. Not every agent slot requires inference.

---

## Current viable local models (as of early 2026)

### Small series with genuine agentic capability

**Qwen3.5 small series** (Alibaba, 2026)
0.8B, 2B, 4B, and 9B models with 256K context across 201 languages. Support thinking + non-thinking modes; specifically optimized for agentic coding and tool use. The 4B and 9B variants are likely the most practical R2 agent models for this framework — small enough to run multiple concurrently on a single GPU, capable enough for structured tool calling.
https://unsloth.ai/docs/models/qwen3.5

**MiMo-V2-Flash** (Xiaomi, Feb 2026)
Explicitly built for agentic tool-calling workflows — code debugging, terminal operations, web dev, general tool use. Outperforms models 2–3x its parameter count on software engineering benchmarks. Very cheap via API ($0.10/$0.30 per million tokens) if you want cloud fallback for specific agents without the full frontier cost.
https://artificialanalysis.ai/models/mimo-v2-0206

**Qwen3-Coder / Qwen3.6-Plus** (Alibaba, 2026)
Qwen3-Coder (480B MoE, 35B active) is state-of-the-art among open models on agentic coding, browser use, and tool use — comparable to Claude Sonnet 4. Qwen3.6-Plus positions itself as an "agentic" model with tight reasoning/memory/execution integration. The smaller Qwen3-Coder variants are the relevant local models here.
https://github.com/QwenLM/Qwen3-Coder

**MiMo-V2-Pro** (Xiaomi, March 2026)
1T parameter MoE with 42B active params; agentic coding performance rivals Claude Sonnet 4.6. More relevant as a 3PO candidate or high-capability R2 than a lightweight sub-agent, but worth noting the trend: frontier-competitive agentic performance is arriving in open weights.
https://mimo.xiaomi.com/mimo-v2-pro

---

## Recent papers directly relevant to the 3PO/R2 design

**Difficulty-Aware Agent Orchestration (DAAO)** (arXiv 2509.11079)
Uses a VAE for query difficulty estimation, then dynamically routes to the appropriate model size and workflow depth. Cost- and performance-aware LLM router that assigns frontier models only to queries that need them, small models to the rest. This is a formalization of the 3PO/R2 intuition: the routing decision itself is the interesting problem.
https://arxiv.org/html/2509.11079v1

**AORCHESTRA: Automating Sub-Agent Creation for Agentic Orchestration** (arXiv 2602.03786)
On-demand sub-agent specialization: unifies agent definition as a 4-tuple (INSTRUCTION, CONTEXT, TOOLS, MODEL) and creates tailored executors on the fly. 80% success rate across GAIA benchmark tasks (Level 1: 88.7%, Level 3: 61.5%). Directly relevant to how you'd spawn specialized R2 agents for different task domains rather than using a single general agent.
https://arxiv.org/abs/2602.03786

**Utility-Guided Agent Orchestration** (arXiv 2603.19896)
Frames orchestration as an explicit decision problem: choose among respond / retrieve / tool call / verify / stop by estimating the utility of each action given current gain, step cost, uncertainty, and redundancy. Avoids unnecessary tool calls and early stops when sufficient information exists. Relevant to the R2 agent loop design — when should an agent call a tool vs. return a result vs. escalate.
https://arxiv.org/abs/2603.19896

**MCPAgentBench** (arXiv 2512.24565)
Benchmark for agent tool use via MCP (Model Context Protocol) with real-world task definitions and dynamic sandboxed evaluation. Relevant because MCP is becoming the de facto protocol for agent tool use — the tool interface the R2 agents expose to the 3PO layer may naturally be MCP-shaped.
https://arxiv.org/abs/2512.24565

---

## Hardware requirements for practical local R2 inference

| Hardware | Comfortable model size | Concurrent agents | Notes |
|---|---|---|---|
| 16GB RAM, no GPU | 7B CPU (quantized) | 1–2 | Slow; background-only |
| RTX 3090 / 4090 (24GB VRAM) | 14B full, 32B quantized | 3–5 (7B models) | Good practical setup |
| Mac M2/M3 Max (32–96GB unified) | 32B–70B | 4–8 (7B models) | Unified memory handles batching well |
| 2× 24GB GPU | 70B quantized | 6–10 (7B models) | Serious local inference |

Ollama and llama.cpp both support multi-model concurrent serving. Running 4–6 specialized 7B models simultaneously is practical on a single 24GB GPU, which maps well to having several R2 agents active concurrently with different specializations.

---

## Vision as a special case

If R2 agents need to observe GUI state (understand what's in a webview surface, interact with a legacy X11 app via XWayland), that requires multimodal capability. This is viable locally:

- **Llama 3.2 Vision 11B** — runs on a 24GB GPU, reasonable UI understanding
- **Moondream 2B** — very small, specifically optimized for UI element understanding, runs on CPU

In this framework, compositor-mediated surface access means an agent "seeing" a surface gets a structured viewport, not a raw screenshot of the whole display. That scoping may reduce what the vision model needs to reason about.

---

## The admin agent as a special case

The admin agent (privilege escalation gateway) sits in an interesting position: it's not a task-execution agent and it's not the human ambassador. It's an evaluator. For routine escalation requests that match known patterns, a well-prompted 14B local model with a fixed system prompt is likely sufficient. For ambiguous or high-stakes requests, the safe failure mode is "I'm uncertain — flagging for human review" rather than needing frontier reasoning. The asymmetric risk (false positive = blocked request = logged entry to tune the prompt; false negative = approved malicious escalation = actual threat) means erring toward "uncertain, escalate" is always correct, which is a behavior small models are easier to reliably elicit than frontier reasoning.

---

## Sources

- [MiMo-V2-Flash analysis — Artificial Analysis](https://artificialanalysis.ai/models/mimo-v2-0206)
- [Xiaomi MiMo-V2 family overview](https://mlq.ai/news/xiaomi-releases-trio-of-mimo-ai-models-tailored-for-agents-robots-and-voice-applications/)
- [Qwen3-Coder GitHub](https://github.com/QwenLM/Qwen3-Coder)
- [Qwen3.6-Plus announcement](https://alternativeto.net/news/2026/4/alibaba-cloud-launches-qwen3-6-plus-with-upgraded-multimodal-and-agentic-coding-capabilities/)
- [Qwen3.5 local setup docs](https://unsloth.ai/docs/models/qwen3.5)
- [DAAO — Difficulty-Aware Agent Orchestration](https://arxiv.org/html/2509.11079v1)
- [AORCHESTRA — Automating Sub-Agent Creation](https://arxiv.org/abs/2602.03786)
- [Utility-Guided Agent Orchestration](https://arxiv.org/abs/2603.19896)
- [MCPAgentBench](https://arxiv.org/abs/2512.24565)
