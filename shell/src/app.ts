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
}

interface SurfaceEventMessage {
    topic: string;
    body?: {
        surface?: CompositorSurface;
        client?: CompositorClientIdentity;
        event?: SurfaceEventName;
        device?: string;
    };
}

interface EscalationDecisionMessage {
    topic: string;
    body?: {
        id?: string;
    };
}

type BusEnvelope = LifecycleEventMessage | SurfaceEventMessage | EscalationDecisionMessage;

interface BusSender {
    uid: number;
    kind: string;
}

// SenderUID is the well-known uid of the root/daemon services.
const SenderRoot = 0;

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
        render();
    });
    socket.addEventListener("message", (event) => {
        const payload = parseJSON<BusEnvelope>(messageText(event.data));
        if (!payload || typeof payload.topic !== "string") {
            return;
        }
        if (payload.topic.startsWith("agent.lifecycle.")) {
            const msg = payload as LifecycleEventMessage & { sender?: BusSender };
            // Require sender metadata for privileged lifecycle facts.
            if (!msg.sender || (msg.sender.uid !== SenderRoot && msg.sender.kind !== "delegated")) {
                return;
            }
            applyLifecycleEvent(msg);
            render();
            return;
        }
        if (payload.topic.startsWith("compositor.surface.")) {
            const msg = payload as SurfaceEventMessage & { sender?: BusSender };
            // Require sender metadata for privileged surface facts.
            if (!msg.sender || (msg.sender.uid !== SenderRoot && msg.sender.kind !== "delegated")) {
                return;
            }
            applySurfaceEvent(msg);
            render();
            return;
        }
        if (payload.topic === "admin.escalation.pending") {
            const msg = payload as unknown as { body?: AdminEscalationEvent; sender?: BusSender };
            // Require sender metadata for admin escalation facts.
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
            // Require sender metadata for admin escalation facts.
            if (!msg.sender || (msg.sender.uid !== SenderRoot && msg.sender.kind !== "delegated")) {
                return;
            }
            if (!msg.body?.id) {
                return;
            }
            state.shellState.pending_escalations = state.shellState.pending_escalations.filter((entry) => entry.id !== msg.body?.id);
            render();
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

function render(): void {
    const selectedUid = Number.parseInt(state.selectedAgent || "0", 10) || 0;
    const agents = state.shellState.agents;
    const surfaces = state.shellState.surfaces;
    const pendingEscalations = state.shellState.pending_escalations;
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
              Live agent roster, compositor surfaces, audit activity, and human
              review controls in one place.
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
                <span class="stat-label">Audit Backlog</span>
                <span class="stat-value">${state.auditEvents.length}</span>
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
        <div class="column">
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

        <div class="column">
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
            render();
        });
    }

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
