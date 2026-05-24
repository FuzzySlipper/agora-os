const defaultConstraints = "pointer,keyboard,read_pixels";

type AgentStatus = "running" | "exited" | "stopped" | string;
type ConnectionStatus = "Disconnected" | "Connecting" | "Live";
type EscalationDecision = "approve" | "deny" | "escalate" | string;
type HumanDecision = "approve" | "deny";
type SurfaceEventName = "mapped" | "unmapped" | "focused" | "input_denied" | string;

interface AgentInfo {
    name: string;
    uid: number;
    status: AgentStatus;
    slice: string;
    cpu_quota?: string;
    memory_max?: string;
    net_access?: string;
    created_at: string;
}

interface CompositorSurface {
    id: string;
    wayfire_view_id?: number;
    app_id?: string;
    title?: string;
    role?: string;
}

interface CompositorClientIdentity {
    pid: number;
    uid: number;
    gid: number;
}

interface TrackedSurface {
    surface: CompositorSurface;
    client: CompositorClientIdentity;
    last_event: SurfaceEventName;
    device?: string;
    updated_at: string;
}

interface EscalationRequest {
    agent_uid: number;
    task_context: string;
    requested_action: string;
    requested_resource: string;
    justification: string;
}

interface EscalationResponse {
    decision: EscalationDecision;
    reasoning?: string;
    constraints?: string[];
    error?: string;
}

interface AdminEscalationEvent {
    id: string;
    timestamp: string;
    request: EscalationRequest;
    response: EscalationResponse;
}

interface AuditEvent {
    timestamp: string;
    agent_uid: number;
    agent_name?: string;
    action: string;
    resource: string;
    outcome: string;
}

interface ShellStateSnapshot {
    agents: AgentInfo[];
    surfaces: TrackedSurface[];
    pending_escalations: AdminEscalationEvent[];
}

interface LifecycleEventMessage {
    topic: string;
    body?: {
        agent?: AgentInfo;
    };
    sender?: BusSender;
}

interface SurfaceEventMessage {
    topic: string;
    body?: {
        surface?: CompositorSurface;
        client?: CompositorClientIdentity;
        event?: SurfaceEventName;
        device?: string;
    };
    sender?: BusSender;
}

interface EscalationDecisionMessage {
    topic: string;
    body?: {
        id?: string;
    };
    sender?: BusSender;
}

// --- Chat / Conversation Types ---

interface ConversationTurnRequest {
    session_id: string;
    turn_id: string;
    prompt: string;
    context?: unknown;
}

interface ConversationTurnResponse {
    session_id: string;
    turn_id: string;
    summary: string;
    result?: unknown;
}

interface WorkProgress {
    task_id: string;
    stage: string;
    message: string;
    step?: number;
    max_steps?: number;
}

interface ChatMessage {
    id: string;
    turn_id: string;
    role: "user" | "assistant";
    content: string;
    timestamp: string;
    status?: "pending" | "streaming" | "done" | "error";
}

interface ChatSession {
    session_id: string;
    messages: ChatMessage[];
    created_at: string;
    selected_agent_uid?: number;
}

type BusEnvelope = LifecycleEventMessage | SurfaceEventMessage | EscalationDecisionMessage | BusEventBase;

interface BusSender {
    uid: number;
    kind: string;
}

interface BusEventBase {
    topic: string;
    body?: unknown;
    sender?: BusSender;
}

// SenderUID is the well-known uid of the root/daemon services.
const SenderRoot = 0;

// Chat / session localStorage keys
const STORAGE_KEY_SESSION = "agora.shell.chat.session";
const STORAGE_KEY_SELECTED_AGENT = "agora.shell.chat.selected_agent";

interface AppState {
    token: string;
    shellState: ShellStateSnapshot;
    auditEvents: AuditEvent[];
    bus: WebSocket | null;
    auditSocket: WebSocket | null;
    selectedAgent: string;
    busStatus: ConnectionStatus;
    auditStatus: ConnectionStatus;
    refreshHandle: number | null;
    // Chat state
    chatSession: ChatSession;
    chatInputHistory: string[];
    chatHistoryIndex: number;
    streamingResponse: string;
}

const state: AppState = {
    token: "",
    shellState: emptyShellState(),
    auditEvents: [],
    bus: null,
    auditSocket: null,
    selectedAgent: "",
    busStatus: "Disconnected",
    auditStatus: "Disconnected",
    refreshHandle: null,
    // Chat state
    chatSession: loadChatSession(),
    chatInputHistory: [],
    chatHistoryIndex: -1,
    streamingResponse: "",
};

const app = requireElement("app");

bootstrap();

function bootstrap(): void {
    const hash = new URLSearchParams(window.location.hash.replace(/^#/, ""));
    const tokenFromHash = hash.get("token");
    if (tokenFromHash) {
        localStorage.setItem("agora.shell.token", tokenFromHash);
        history.replaceState(null, "", window.location.pathname + window.location.search);
    }
    state.token = localStorage.getItem("agora.shell.token") || "";
    // Restore selected agent from separate localStorage key
    const storedAgent = localStorage.getItem(STORAGE_KEY_SELECTED_AGENT);
    if (storedAgent) {
        state.selectedAgent = storedAgent;
    }
    render();
    if (state.token) {
        void connectAll();
    }
}

async function connectAll(): Promise<void> {
    await refreshState();
    connectBus();
    connectAudit();
    scheduleRefresh();
}

function scheduleRefresh(): void {
    if (state.refreshHandle) {
        window.clearInterval(state.refreshHandle);
    }
    state.refreshHandle = window.setInterval(() => {
        void refreshState();
    }, 15000);
}

async function refreshState(): Promise<void> {
    if (!state.token) {
        return;
    }
    const response = await fetch("/api/shell/state", {
        headers: { Authorization: `Bearer ${state.token}` },
    });
    if (!response.ok) {
        throw new Error(`shell state failed: ${response.status}`);
    }
    state.shellState = toShellState(await response.json());
    if (!state.selectedAgent && state.shellState.agents.length > 0) {
        state.selectedAgent = String(state.shellState.agents[0].uid);
    }
    render();
}

function generateId(): string {
    const buf = new Uint32Array(4);
    crypto.getRandomValues(buf);
    return Array.from(buf).map((n) => n.toString(36)).join("");
}

function loadChatSession(): ChatSession {
    try {
        const raw = localStorage.getItem(STORAGE_KEY_SESSION);
        if (raw) {
            const parsed = JSON.parse(raw) as ChatSession;
            if (parsed && typeof parsed.session_id === "string" && Array.isArray(parsed.messages)) {
                return parsed;
            }
        }
    } catch {
        // corrupted storage, reset
    }
    return {
        session_id: generateId(),
        messages: [],
        created_at: new Date().toISOString(),
    };
}

function saveChatSession(): void {
    try {
        localStorage.setItem(STORAGE_KEY_SESSION, JSON.stringify(state.chatSession));
    } catch {
        // storage full or unavailable; non-critical
    }
}

function connectBus(): void {
    if (!state.token) {
        return;
    }
    if (state.bus) {
        state.bus.close();
    }
    const url = new URL("/ws", window.location.href);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(url.toString(), [`agora.token.${state.token}`]);
    state.bus = socket;
    state.busStatus = "Connecting";
    render();
    socket.addEventListener("open", () => {
        state.busStatus = "Live";
        socket.send(JSON.stringify({ op: "sub", topic: "agent.lifecycle.*" }));
        socket.send(JSON.stringify({ op: "sub", topic: "compositor.surface.*" }));
        socket.send(JSON.stringify({ op: "sub", topic: "compositor.advisory.surface.*" }));
        socket.send(JSON.stringify({ op: "sub", topic: "admin.escalation.*" }));
        // Subscribe to conversation and work topics
        socket.send(JSON.stringify({ op: "sub", topic: "conversation.turn.*" }));
        socket.send(JSON.stringify({ op: "sub", topic: "agent.work.progress" }));
        render();
    });
    socket.addEventListener("message", (event) => {
        const payload = parseJSON<BusEnvelope>(messageText(event.data));
        if (!payload || typeof payload.topic !== "string") {
            return;
        }
        if (payload.topic.startsWith("agent.lifecycle.")) {
            const msg = payload as LifecycleEventMessage & { sender?: BusSender };
            if (!msg.sender || (msg.sender.uid !== SenderRoot && msg.sender.kind !== "delegated")) {
                return;
            }
            applyLifecycleEvent(msg);
            render();
            return;
        }
        if (payload.topic.startsWith("compositor.surface.")) {
            const msg = payload as SurfaceEventMessage & { sender?: BusSender };
            if (!msg.sender || (msg.sender.uid !== SenderRoot && msg.sender.kind !== "delegated")) {
                return;
            }
            applySurfaceEvent(msg);
            render();
            return;
        }
        if (payload.topic === "admin.escalation.pending") {
            const msg = payload as unknown as { body?: AdminEscalationEvent; sender?: BusSender };
            if (!msg.sender || (msg.sender.uid !== SenderRoot && msg.sender.kind !== "delegated")) {
                return;
            }
            if (!msg.body) {
                return;
            }
            const exists = state.shellState.pending_escalations.some((entry) => entry.id === msg.body!.id);
            if (!exists) {
                state.shellState.pending_escalations.unshift(msg.body);
                render();
            }
            return;
        }
        if (payload.topic === "admin.escalation.decided") {
            const msg = payload as EscalationDecisionMessage & { sender?: BusSender };
            if (!msg.sender || (msg.sender.uid !== SenderRoot && msg.sender.kind !== "delegated")) {
                return;
            }
            if (!msg.body?.id) {
                return;
            }
            state.shellState.pending_escalations = state.shellState.pending_escalations.filter((entry) => entry.id !== msg.body?.id);
            render();
            return;
        }
        // Handle conversation turn response
        if (payload.topic === "conversation.turn.responded") {
            const body = payload.body as ConversationTurnResponse | null;
            if (body && body.turn_id && body.summary) {
                handleConversationResponse(body);
                render();
            }
            return;
        }
        // Handle agent work progress
        if (payload.topic === "agent.work.progress") {
            const body = payload.body as WorkProgress | null;
            if (body) {
                handleWorkProgress(body);
                render();
            }
            return;
        }
    });
    socket.addEventListener("close", () => {
        state.busStatus = "Disconnected";
        render();
        window.setTimeout(() => connectBus(), 2000);
    });
}

function connectAudit(): void {
    if (!state.token) {
        return;
    }
    if (state.auditSocket) {
        state.auditSocket.close();
    }
    const url = new URL("/api/shell/audit/ws", window.location.href);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(url.toString(), [`agora.token.${state.token}`]);
    state.auditSocket = socket;
    state.auditStatus = "Connecting";
    render();
    socket.addEventListener("open", () => {
        state.auditStatus = "Live";
        render();
    });
    socket.addEventListener("message", (event) => {
        const payload = parseJSON<AuditEvent>(messageText(event.data));
        if (!payload) {
            return;
        }
        state.auditEvents.unshift(payload);
        state.auditEvents = state.auditEvents.slice(0, 80);
        render();
    });
    socket.addEventListener("close", () => {
        state.auditStatus = "Disconnected";
        render();
        window.setTimeout(() => connectAudit(), 2500);
    });
}

// --- Chat / Conversation Handlers ---

function submitChatPrompt(prompt: string): void {
    if (!state.bus || state.bus.readyState !== WebSocket.OPEN) {
        alert("Not connected to event bus. Cannot submit prompt.");
        return;
    }
    const session = state.chatSession;
    const turnId = generateId();
    const timestamp = new Date().toISOString();

    // Add user message
    const userMsg: ChatMessage = {
        id: generateId(),
        turn_id: turnId,
        role: "user",
        content: prompt,
        timestamp,
    };
    session.messages.push(userMsg);

    // Add a pending assistant message
    const assistantMsg: ChatMessage = {
        id: generateId(),
        turn_id: turnId,
        role: "assistant",
        content: "",
        timestamp: new Date().toISOString(),
        status: "pending",
    };
    session.messages.push(assistantMsg);
    state.streamingResponse = "";

    // Determine selected agent for context
    const selectedUid = Number.parseInt(state.selectedAgent || "0", 10) || 0;
    const context: Record<string, unknown> = {};
    if (selectedUid > 0) {
        context.selected_agent_uid = selectedUid;
        const agent = state.shellState.agents.find((a) => a.uid === selectedUid);
        if (agent) {
            context.selected_agent_name = agent.name;
        }
    }

    // Publish to the event bus
    const envelope: ConversationTurnRequest = {
        session_id: session.session_id,
        turn_id: turnId,
        prompt: prompt,
        context: Object.keys(context).length > 0 ? context : undefined,
    };

    state.bus.send(JSON.stringify({ op: "pub", topic: "conversation.turn.requested", body: envelope }));

    saveChatSession();
    render();
    scrollChatToBottom();
}

function handleConversationResponse(response: ConversationTurnResponse): void {
    const session = state.chatSession;
    // Find the assistant message with matching turn_id
    const assistantMsg = session.messages.find(
        (m) => m.role === "assistant" && m.turn_id === response.turn_id,
    );
    if (assistantMsg) {
        assistantMsg.content = response.summary;
        assistantMsg.status = "done";
    }
    state.streamingResponse = "";
    saveChatSession();
}

function handleWorkProgress(progress: WorkProgress): void {
    // Find pending assistant messages and update their status
    const pending = state.chatSession.messages.filter(
        (m) => m.role === "assistant" && m.status === "pending",
    );
    if (pending.length > 0) {
        // Update the latest pending message with progress info
        const latest = pending[pending.length - 1];
        state.streamingResponse = `${progress.stage}: ${progress.message}`;
        if (progress.step !== undefined && progress.max_steps !== undefined) {
            state.streamingResponse += ` (${progress.step}/${progress.max_steps})`;
        }
    }
}

function scrollChatToBottom(): void {
    requestAnimationFrame(() => {
        const chatBody = document.getElementById("chat-body");
        if (chatBody) {
            chatBody.scrollTop = chatBody.scrollHeight;
        }
    });
}

// --- Lifecycle & Surface event handlers (unchanged from original) ---

function applyLifecycleEvent(message: LifecycleEventMessage): void {
    const incoming = message.body?.agent;
    if (!incoming || incoming.uid === 0) {
        return;
    }
    const agents = [...state.shellState.agents];
    const index = agents.findIndex((agent) => agent.uid === incoming.uid);
    if (message.topic === "agent.lifecycle.terminated") {
        if (index >= 0) {
            agents[index] = { ...agents[index], ...incoming, status: "stopped" };
        } else {
            agents.push({ ...incoming, status: "stopped" });
        }
    } else if (index >= 0) {
        agents[index] = { ...agents[index], ...incoming };
    } else {
        agents.push(incoming);
    }
    agents.sort((left, right) => left.uid - right.uid);
    state.shellState.agents = agents;
}

function applySurfaceEvent(message: SurfaceEventMessage): void {
    const body = message.body;
    if (!body?.surface?.id || !body.client || !body.event) {
        return;
    }
    const surfaces = [...state.shellState.surfaces];
    const index = surfaces.findIndex((entry) => entry.surface.id === body.surface.id);
    const next: TrackedSurface = {
        surface: body.surface,
        client: body.client,
        last_event: body.event,
        device: body.device,
        updated_at: new Date().toISOString(),
    };
    if (body.event === "unmapped" || message.topic === "compositor.surface.destroyed") {
        if (index >= 0) {
            surfaces.splice(index, 1);
        }
    } else if (index >= 0) {
        surfaces[index] = { ...surfaces[index], ...next };
    } else {
        surfaces.push(next);
    }
    surfaces.sort((left, right) => right.updated_at.localeCompare(left.updated_at));
    state.shellState.surfaces = surfaces;
}

function setToken(nextToken: string): void {
    state.token = nextToken.trim();
    if (state.token) {
        localStorage.setItem("agora.shell.token", state.token);
        void connectAll();
    } else {
        localStorage.removeItem("agora.shell.token");
    }
    render();
}

async function grantViewport(surfaceId: string, agentUid: number): Promise<void> {
    const response = await fetch("/api/shell/grants", {
        method: "POST",
        headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${state.token}`,
        },
        body: JSON.stringify({
            surface_id: surfaceId,
            agent_uid: agentUid,
            actions: ["pointer", "keyboard", "read_pixels"],
        }),
    });
    if (!response.ok) {
        throw new Error(`grant failed: ${response.status}`);
    }
}

async function decideEscalation(id: string, decision: HumanDecision, notes: string, constraints: string[]): Promise<void> {
    const response = await fetch("/api/shell/escalations/decide", {
        method: "POST",
        headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${state.token}`,
        },
        body: JSON.stringify({
            id,
            decision,
            notes,
            constraints,
        }),
    });
    if (!response.ok) {
        throw new Error(`decision failed: ${response.status}`);
    }
    state.shellState.pending_escalations = state.shellState.pending_escalations.filter((entry) => entry.id !== id);
    render();
}

// --- Safe Markdown Rendering ---

function renderSafeMarkdown(text: string): string {
    // Escape HTML first
    let html = escapeHtml(text);

    // Block-level: code blocks (``` ... ```)
    html = html.replace(/```(\w*)\n([\s\S]*?)```/g, (_match, _lang, code) => {
        return `<pre class="chat-code-block">${code.trim()}</pre>`;
    });

    // Inline: code (`...`)
    html = html.replace(/`([^`]+)`/g, (_match, code) => {
        return `<code class="chat-inline-code">${code}</code>`;
    });

    // Bold (**text**)
    html = html.replace(/\*\*([^*]+)\*\*/g, (_match, inner) => {
        return `<strong>${inner}</strong>`;
    });

    // Italic (*text*)
    html = html.replace(/\*([^*]+)\*/g, (_match, inner) => {
        return `<em>${inner}</em>`;
    });

    // Links [text](url)
    html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_match, linkText, url) => {
        const safeUrl = sanitizeUrl(url);
        return `<a href="${safeUrl}" target="_blank" rel="noopener noreferrer">${linkText}</a>`;
    });

    // Line breaks
    html = html.replace(/\n/g, "<br>");

    return html;
}

function sanitizeUrl(url: string): string {
    const trimmed = url.trim();
    // Only allow http, https, and relative URLs
    if (/^https?:\/\//i.test(trimmed) || trimmed.startsWith("/") || trimmed.startsWith("#")) {
        return escapeAttr(trimmed);
    }
    // Anything else (javascript:, data:, etc.) — return safe fallback
    return "#invalid-url";
}

// --- Render ---

function render(): void {
    const selectedUid = Number.parseInt(state.selectedAgent || "0", 10) || 0;
    const agents = state.shellState.agents;
    const surfaces = state.shellState.surfaces;
    const pendingEscalations = state.shellState.pending_escalations;
    const selectedAgent = agents.find((a) => a.uid === selectedUid);
    const filteredAudit = selectedUid
        ? state.auditEvents.filter((event) => event.agent_uid === selectedUid)
        : state.auditEvents;
    const surfacesByUid = new Map<number, TrackedSurface[]>();
    for (const surface of surfaces) {
        const agentSurfaces = surfacesByUid.get(surface.client.uid);
        if (agentSurfaces) {
            agentSurfaces.push(surface);
            continue;
        }
        surfacesByUid.set(surface.client.uid, [surface]);
    }
    app.innerHTML = `
    <main class="shell">
      <section class="hero">
        <div class="hero-top">
          <div>
            <p class="eyebrow">Human Operator Console</p>
            <h1>Agora Shell</h1>
            <p>
              Live agent roster, compositor surfaces, audit activity, human
              review controls, and agent chat in one place.
            </p>
            <div class="stats">
              <div class="stat">
                <span class="stat-label">Agents</span>
                <span class="stat-value">${agents.length}</span>
              </div>
              <div class="stat">
                <span class="stat-label">Surfaces</span>
                <span class="stat-value">${surfaces.length}</span>
              </div>
              <div class="stat">
                <span class="stat-label">Pending Escalations</span>
                <span class="stat-value">${pendingEscalations.length}</span>
              </div>
              <div class="stat">
                <span class="stat-label">Chat Messages</span>
                <span class="stat-value">${state.chatSession.messages.length}</span>
              </div>
            </div>
          </div>
          <section class="connection-card">
            <label for="shell-token">Human token</label>
            <input id="shell-token" type="password" value="${escapeAttr(state.token)}" placeholder="Paste token or load via #token=..." />
            <div class="connection-actions">
              <button id="connect-button" class="button warn">Connect</button>
              <button id="clear-token-button" class="button secondary">Clear</button>
            </div>
            <div class="status-chip">Bus: ${state.busStatus}</div>
            <div class="status-chip">Audit: ${state.auditStatus}</div>
          </section>
        </div>
      </section>

      <section class="layout">
        <div class="column column-left">
          <section class="panel">
            <div class="panel-header">
              <div>
                <h2 class="panel-title">Agent Roster</h2>
                <p class="panel-subtitle">Live updates from agent lifecycle events with CPU and memory policy context when available.</p>
              </div>
            </div>
            <div class="panel-body">
              <select id="agent-filter" class="agent-filter">
                <option value="">All agents</option>
                ${agents.map((agent) => `<option value="${agent.uid}" ${String(agent.uid) === state.selectedAgent ? "selected" : ""}>${escapeHtml(agent.name)} (${agent.uid})</option>`).join("")}
              </select>
              <div class="agent-list">
                ${agents.length === 0 ? `<div class="agents-empty">No agents reported yet.</div>` : agents.map((agent) => renderAgentCard(agent, surfacesByUid.get(agent.uid) || [])).join("")}
              </div>
            </div>
          </section>

          <section class="panel">
            <div class="panel-header">
              <div>
                <h2 class="panel-title">Surface Tree</h2>
                <p class="panel-subtitle">Authoritative compositor ownership with one-click viewport grants.</p>
              </div>
              <button id="refresh-button" class="button ghost">Refresh snapshot</button>
            </div>
            <div class="panel-body">
              <div class="surface-list">
                ${surfaces.length === 0 ? `<div class="surface-empty">No tracked compositor surfaces yet.</div>` : surfaces.map((surface) => renderSurfaceCard(surface)).join("")}
              </div>
            </div>
          </section>
        </div>

        <div class="column column-right">
          ${selectedAgent ? renderAgentDetailPanel(selectedAgent, surfacesByUid.get(selectedUid) || []) : ""}

          <section class="panel panel-chat">
            <div class="panel-header">
              <div>
                <h2 class="panel-title">Agent Chat</h2>
                <p class="panel-subtitle">${selectedAgent ? `Chatting with agent context: ${escapeHtml(selectedAgent.name)} (UID ${selectedUid})` : "Conversation session with agent workers via the event bus."}</p>
              </div>
              <button id="clear-chat-button" class="button ghost">Clear session</button>
            </div>
            <div class="panel-body">
              <div id="chat-body" class="chat-messages">
                ${state.chatSession.messages.length === 0
                    ? `<div class="chat-empty">Send a message to start a conversation with the agent system.</div>`
                    : state.chatSession.messages.map(renderChatMessage).join("")}
                ${state.streamingResponse ? `<div class="chat-streaming"><span class="badge cool">working</span> ${escapeHtml(state.streamingResponse)}</div>` : ""}
              </div>
              <div class="chat-input-area">
                <textarea
                  id="chat-input"
                  class="chat-input"
                  rows="2"
                  placeholder="Type a message... (Enter to send, Shift+Enter for newline)"
                ></textarea>
                <button id="chat-send-button" class="button cool" ${state.busStatus !== "Live" ? "disabled" : ""}>Send</button>
              </div>
            </div>
          </section>

          <section class="panel">
            <div class="panel-header">
              <div>
                <h2 class="panel-title">Audit Tail</h2>
                <p class="panel-subtitle">Backlog and live audit lines, filtered by the currently selected agent.</p>
              </div>
            </div>
            <div class="panel-body">
              <div class="audit-list">
                ${filteredAudit.length === 0 ? `<div class="audit-empty">No audit events for this filter yet.</div>` : filteredAudit.slice(0, 24).map(renderAuditCard).join("")}
              </div>
            </div>
          </section>

          <section class="panel">
            <div class="panel-header">
              <div>
                <h2 class="panel-title">Escalation Queue</h2>
                <p class="panel-subtitle">Pending model-escalated requests awaiting human review.</p>
              </div>
            </div>
            <div class="panel-body">
              <div class="escalation-list">
                ${pendingEscalations.length === 0 ? `<div class="escalation-empty">No pending escalation reviews right now.</div>` : pendingEscalations.map(renderEscalationCard).join("")}
              </div>
              <p class="footer-note">Constraints can be entered one per line. The current shell records human decisions and broadcasts them on the event bus for downstream consumers.</p>
            </div>
          </section>
        </div>
      </section>
    </main>
  `;

    // --- Event listeners ---

    const connectButton = document.getElementById("connect-button");
    if (connectButton instanceof HTMLButtonElement) {
        connectButton.addEventListener("click", () => {
            setToken(getInputValue("shell-token"));
        });
    }

    const clearTokenButton = document.getElementById("clear-token-button");
    if (clearTokenButton instanceof HTMLButtonElement) {
        clearTokenButton.addEventListener("click", () => {
            setToken("");
        });
    }

    const refreshButton = document.getElementById("refresh-button");
    if (refreshButton instanceof HTMLButtonElement) {
        refreshButton.addEventListener("click", () => {
            void refreshState();
        });
    }

    const agentFilter = document.getElementById("agent-filter");
    if (agentFilter instanceof HTMLSelectElement) {
        agentFilter.addEventListener("change", () => {
            state.selectedAgent = agentFilter.value;
            localStorage.setItem(STORAGE_KEY_SELECTED_AGENT, state.selectedAgent);
            render();
        });
    }

    // Surface grant buttons
    for (const button of queryButtons("[data-grant-surface]")) {
        button.addEventListener("click", async () => {
            const surfaceId = button.dataset.grantSurface ?? "";
            const grantUID = Number.parseInt(button.dataset.grantUid ?? "0", 10);
            if (!surfaceId || Number.isNaN(grantUID) || grantUID === 0) {
                return;
            }
            button.disabled = true;
            try {
                await grantViewport(surfaceId, grantUID);
                await refreshState();
            } catch (error) {
                alert(toErrorMessage(error));
            } finally {
                button.disabled = false;
            }
        });
    }

    // Escalation decision buttons
    for (const button of queryButtons("[data-escalation-action]")) {
        button.addEventListener("click", async () => {
            const id = button.dataset.escalationId ?? "";
            const action = button.dataset.escalationAction;
            if (!id || !isHumanDecision(action)) {
                return;
            }
            const notesField = queryTextarea(`[data-escalation-notes="${id}"]`);
            const constraintsField = queryTextarea(`[data-escalation-constraints="${id}"]`);
            button.disabled = true;
            try {
                await decideEscalation(
                    id,
                    action,
                    notesField?.value ?? "",
                    (constraintsField?.value ?? "")
                        .split(/\n+/)
                        .map((line) => line.trim())
                        .filter(Boolean),
                );
            } catch (error) {
                alert(toErrorMessage(error));
            } finally {
                button.disabled = false;
            }
        });
    }

    // Chat input handler
    const chatInput = document.getElementById("chat-input") as HTMLTextAreaElement | null;
    const chatSendButton = document.getElementById("chat-send-button") as HTMLButtonElement | null;

    if (chatInput) {
        // Restore current input from history index
        chatInput.focus();

        chatInput.addEventListener("keydown", (event) => {
            if (event.key === "Enter" && !event.shiftKey) {
                event.preventDefault();
                sendChatFromInput();
            }
        });
    }

    if (chatSendButton) {
        chatSendButton.addEventListener("click", () => {
            sendChatFromInput();
        });
    }

    // Clear chat button
    const clearChatButton = document.getElementById("clear-chat-button");
    if (clearChatButton instanceof HTMLButtonElement) {
        clearChatButton.addEventListener("click", () => {
            state.chatSession = {
                session_id: generateId(),
                messages: [],
                created_at: new Date().toISOString(),
            };
            state.streamingResponse = "";
            saveChatSession();
            render();
        });
    }
}

function sendChatFromInput(): void {
    const chatInput = document.getElementById("chat-input") as HTMLTextAreaElement | null;
    if (!chatInput) {
        return;
    }
    const prompt = chatInput.value.trim();
    if (!prompt) {
        return;
    }

    // Save to input history
    state.chatInputHistory.push(prompt);
    state.chatHistoryIndex = -1;

    // Submit
    submitChatPrompt(prompt);

    // Clear input
    chatInput.value = "";
    chatInput.style.height = "auto";
}

function renderAgentCard(agent: AgentInfo, surfaces: TrackedSurface[]): string {
    const selected = String(agent.uid) === state.selectedAgent;
    return `
    <article class="agent-card ${selected ? "is-selected" : ""}">
      <div class="card-top">
        <h3 class="card-title">${escapeHtml(agent.name)}</h3>
        <span class="badge ${statusClass(agent.status)}">${escapeHtml(agent.status)}</span>
      </div>
      <div class="card-meta">
        <span>UID ${agent.uid}</span>
        <span>Slice ${escapeHtml(agent.slice || "n/a")}</span>
        <span>CPU ${escapeHtml(agent.cpu_quota || "default")}</span>
        <span>Memory ${escapeHtml(agent.memory_max || "default")}</span>
        <span>Created ${formatTime(agent.created_at)}</span>
      </div>
      <div class="surface-tree">
        ${surfaces.length === 0 ? `<div class="surface-empty">No mapped surfaces for this agent.</div>` : surfaces.map((surface) => `
          <div class="surface-card ${selected ? "is-selected" : ""}">
            <div class="card-top">
              <h4 class="card-title">${escapeHtml(surface.surface.title || surface.surface.app_id || surface.surface.id)}</h4>
              <span class="badge cool">${escapeHtml(surface.last_event)}</span>
            </div>
            <div class="surface-meta">
              <span>${escapeHtml(surface.surface.id)}</span>
              <span>PID ${surface.client.pid}</span>
              <span>App ${escapeHtml(surface.surface.app_id || "n/a")}</span>
            </div>
          </div>
        `).join("")}
      </div>
    </article>
  `;
}

function renderSurfaceCard(surface: TrackedSurface): string {
    const grantTarget = Number.parseInt(state.selectedAgent || "0", 10) || surface.client.uid;
    return `
    <article class="surface-card ${String(surface.client?.uid || "") === state.selectedAgent ? "is-selected" : ""}">
      <div class="card-top">
        <h3 class="card-title">${escapeHtml(surface.surface.title || surface.surface.app_id || surface.surface.id)}</h3>
        <span class="badge cool">${escapeHtml(surface.last_event)}</span>
      </div>
      <div class="surface-meta">
        <span>${escapeHtml(surface.surface.id)}</span>
        <span>Owner UID ${surface.client.uid}</span>
        <span>PID ${surface.client.pid}</span>
        <span>Updated ${formatTime(surface.updated_at)}</span>
      </div>
      <div class="surface-actions">
        <button class="button cool" data-grant-surface="${escapeAttr(surface.surface.id)}" data-grant-uid="${grantTarget}">Grant viewport to UID ${grantTarget}</button>
      </div>
    </article>
  `;
}

function renderAuditCard(event: AuditEvent): string {
    return `
    <article class="audit-card">
      <div class="card-top">
        <h3 class="card-title">${escapeHtml(event.action)}</h3>
        <span class="badge ${event.outcome === "allowed" ? "success" : "danger"}">${escapeHtml(event.outcome)}</span>
      </div>
      <div class="audit-meta">
        <span>UID ${event.agent_uid}</span>
        <span>${formatTime(event.timestamp)}</span>
      </div>
      <pre>${escapeHtml(event.resource)}</pre>
    </article>
  `;
}

function renderEscalationCard(event: AdminEscalationEvent): string {
    return `
    <article class="escalation-card">
      <div class="card-top">
        <h3 class="card-title">${escapeHtml(event.request.requested_action)} → ${escapeHtml(event.request.requested_resource)}</h3>
        <span class="badge warn">${escapeHtml(event.response.decision)}</span>
      </div>
      <div class="escalation-meta">
        <span>ID ${escapeHtml(event.id)}</span>
        <span>Agent UID ${event.request.agent_uid}</span>
        <span>${formatTime(event.timestamp)}</span>
      </div>
      <pre>${escapeHtml(event.request.justification || event.response.reasoning || "No justification provided.")}</pre>
      <textarea class="constraints" data-escalation-constraints="${escapeAttr(event.id)}" placeholder="${defaultConstraints}"></textarea>
      <textarea class="constraints" data-escalation-notes="${escapeAttr(event.id)}" placeholder="Human review notes or constraints rationale"></textarea>
      <div class="escalation-actions">
        <button class="button cool" data-escalation-action="approve" data-escalation-id="${escapeAttr(event.id)}">Approve</button>
        <button class="button warn" data-escalation-action="deny" data-escalation-id="${escapeAttr(event.id)}">Deny</button>
      </div>
    </article>
  `;
}

function renderAgentDetailPanel(agent: AgentInfo, surfaces: TrackedSurface[]): string {
    const agentSurfaces = surfaces.length > 0
        ? surfaces.map((s) => `<span class="chat-agent-surface">${escapeHtml(s.surface.title || s.surface.app_id || s.surface.id)} (${escapeHtml(s.last_event)})</span>`).join("")
        : "<span class=\"chat-agent-surface\">No surfaces</span>";

    return `
    <section class="panel panel-agent-detail">
      <div class="panel-header">
        <div>
          <h2 class="panel-title">Selected Agent: ${escapeHtml(agent.name)}</h2>
          <p class="panel-subtitle">UID ${agent.uid} | ${escapeHtml(agent.status)} | Slice ${escapeHtml(agent.slice || "n/a")}</p>
        </div>
      </div>
      <div class="panel-body">
        <div class="agent-detail-grid">
          <div class="agent-detail-item">
            <span class="agent-detail-label">CPU Quota</span>
            <span class="agent-detail-value">${escapeHtml(agent.cpu_quota || "default")}</span>
          </div>
          <div class="agent-detail-item">
            <span class="agent-detail-label">Memory</span>
            <span class="agent-detail-value">${escapeHtml(agent.memory_max || "default")}</span>
          </div>
          <div class="agent-detail-item">
            <span class="agent-detail-label">Network</span>
            <span class="agent-detail-value">${escapeHtml(agent.net_access || "deny")}</span>
          </div>
          <div class="agent-detail-item">
            <span class="agent-detail-label">Created</span>
            <span class="agent-detail-value">${formatTime(agent.created_at)}</span>
          </div>
        </div>
        <div class="agent-detail-surfaces">
          <span class="agent-detail-label">Surfaces:</span>
          <div class="agent-detail-surfaces-list">${agentSurfaces}</div>
        </div>
      </div>
    </section>
  `;
}

function renderChatMessage(msg: ChatMessage): string {
    const isUser = msg.role === "user";
    const isPending = msg.status === "pending";
    const isError = msg.status === "error";
    const cssClass = isUser ? "chat-msg-user" : "chat-msg-assistant";
    const roleLabel = isUser ? "You" : "Agent";

    let contentHtml: string;
    if (isPending) {
        contentHtml = `<span class="chat-pending-indicator">Thinking<span class="chat-dot-anim">.</span><span class="chat-dot-anim">.</span><span class="chat-dot-anim">.</span></span>`;
    } else if (isError) {
        contentHtml = `<span class="chat-error">${escapeHtml(msg.content)}</span>`;
    } else if (isUser) {
        contentHtml = `<p>${escapeHtml(msg.content)}</p>`;
    } else {
        contentHtml = renderSafeMarkdown(msg.content);
    }

    const statusBadge = isPending
        ? `<span class="badge cool">working</span>`
        : isError
        ? `<span class="badge danger">error</span>`
        : msg.status === "streaming"
        ? `<span class="badge cool">streaming</span>`
        : "";

    return `
    <div class="chat-message ${cssClass}">
      <div class="chat-msg-header">
        <span class="chat-msg-role">${roleLabel}</span>
        <span class="chat-msg-time">${formatTime(msg.timestamp)}</span>
        ${statusBadge}
      </div>
      <div class="chat-msg-body">${contentHtml}</div>
    </div>
  `;
}

function formatTime(value: string | null | undefined): string {
    if (!value) {
        return "unknown";
    }
    return new Date(value).toLocaleString();
}

function statusClass(status: AgentStatus): string {
    if (status === "running") {
        return "success";
    }
    if (status === "exited") {
        return "warn";
    }
    return "danger";
}

function escapeHtml(value: unknown): string {
    return String(value ?? "")
        .replaceAll("&", "&amp;")
        .replaceAll("<", "&lt;")
        .replaceAll(">", "&gt;")
        .replaceAll('"', "&quot;");
}

function escapeAttr(value: unknown): string {
    return escapeHtml(value).replaceAll("'", "&#39;");
}

function emptyShellState(): ShellStateSnapshot {
    return {
        agents: [],
        surfaces: [],
        pending_escalations: [],
    };
}

function toShellState(payload: unknown): ShellStateSnapshot {
    const snapshot = payload as Partial<ShellStateSnapshot> | null;
    return {
        agents: snapshot?.agents ?? [],
        surfaces: snapshot?.surfaces ?? [],
        pending_escalations: snapshot?.pending_escalations ?? [],
    };
}

function requireElement(id: string): HTMLElement {
    const element = document.getElementById(id);
    if (!(element instanceof HTMLElement)) {
        throw new Error(`missing #${id}`);
    }
    return element;
}

function getInputValue(id: string): string {
    const input = document.getElementById(id);
    return input instanceof HTMLInputElement ? input.value : "";
}

function queryButtons(selector: string): HTMLButtonElement[] {
    return Array.from(document.querySelectorAll(selector)).filter(
        (element): element is HTMLButtonElement => element instanceof HTMLButtonElement,
    );
}

function queryTextarea(selector: string): HTMLTextAreaElement | null {
    const element = document.querySelector(selector);
    return element instanceof HTMLTextAreaElement ? element : null;
}

function isHumanDecision(value: string | undefined): value is HumanDecision {
    return value === "approve" || value === "deny";
}

function messageText(data: unknown): string {
    return typeof data === "string" ? data : "";
}

function parseJSON<T>(text: string): T | null {
    if (!text) {
        return null;
    }
    try {
        return JSON.parse(text) as T;
    } catch {
        return null;
    }
}

function toErrorMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
}
