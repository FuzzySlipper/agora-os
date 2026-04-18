// @ts-nocheck
const defaultConstraints = "pointer,keyboard,read_pixels";
const state = {
    token: "",
    shellState: { agents: [], surfaces: [], pending_escalations: [] },
    auditEvents: [],
    bus: null,
    auditSocket: null,
    selectedAgent: "",
    busStatus: "Disconnected",
    auditStatus: "Disconnected",
    refreshHandle: null,
};
const app = document.getElementById("app");
bootstrap();
function bootstrap() {
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
async function connectAll() {
    await refreshState();
    connectBus();
    connectAudit();
    scheduleRefresh();
}
function scheduleRefresh() {
    if (state.refreshHandle) {
        window.clearInterval(state.refreshHandle);
    }
    state.refreshHandle = window.setInterval(() => {
        void refreshState();
    }, 15000);
}
async function refreshState() {
    if (!state.token) {
        return;
    }
    const response = await fetch("/api/shell/state", {
        headers: { Authorization: `Bearer ${state.token}` },
    });
    if (!response.ok) {
        throw new Error(`shell state failed: ${response.status}`);
    }
    state.shellState = await response.json();
    if (!state.selectedAgent && state.shellState.agents.length > 0) {
        state.selectedAgent = String(state.shellState.agents[0].uid);
    }
    render();
}
function connectBus() {
    if (!state.token) {
        return;
    }
    if (state.bus) {
        state.bus.close();
    }
    const url = new URL("/ws", window.location.href);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(url, [`agora.token.${state.token}`]);
    state.bus = socket;
    state.busStatus = "Connecting";
    render();
    socket.addEventListener("open", () => {
        state.busStatus = "Live";
        socket.send(JSON.stringify({ op: "sub", topic: "agent.lifecycle.*" }));
        socket.send(JSON.stringify({ op: "sub", topic: "compositor.surface.*" }));
        socket.send(JSON.stringify({ op: "sub", topic: "admin.escalation.*" }));
        render();
    });
    socket.addEventListener("message", (event) => {
        const payload = JSON.parse(event.data);
        if (payload.topic?.startsWith("agent.lifecycle.")) {
            applyLifecycleEvent(payload);
            render();
            return;
        }
        if (payload.topic?.startsWith("compositor.surface.")) {
            applySurfaceEvent(payload);
            render();
            return;
        }
        if (payload.topic === "admin.escalation.decided") {
            const decision = payload.body;
            state.shellState.pending_escalations = state.shellState.pending_escalations.filter((entry) => entry.id !== decision.id);
            render();
        }
    });
    socket.addEventListener("close", () => {
        state.busStatus = "Disconnected";
        render();
        window.setTimeout(() => connectBus(), 2000);
    });
}
function connectAudit() {
    if (!state.token) {
        return;
    }
    if (state.auditSocket) {
        state.auditSocket.close();
    }
    const url = new URL("/api/shell/audit/ws", window.location.href);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(url, [`agora.token.${state.token}`]);
    state.auditSocket = socket;
    state.auditStatus = "Connecting";
    render();
    socket.addEventListener("open", () => {
        state.auditStatus = "Live";
        render();
    });
    socket.addEventListener("message", (event) => {
        const payload = JSON.parse(event.data);
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
function applyLifecycleEvent(message) {
    const incoming = message.body?.agent;
    if (!incoming?.uid) {
        return;
    }
    const agents = [...state.shellState.agents];
    const index = agents.findIndex((agent) => agent.uid === incoming.uid);
    if (message.topic === "agent.lifecycle.terminated") {
        if (index >= 0) {
            agents[index] = { ...agents[index], ...incoming, status: "stopped" };
        }
        else {
            agents.push(incoming);
        }
    }
    else {
        if (index >= 0) {
            agents[index] = { ...agents[index], ...incoming };
        }
        else {
            agents.push(incoming);
        }
    }
    agents.sort((left, right) => left.uid - right.uid);
    state.shellState.agents = agents;
}
function applySurfaceEvent(message) {
    const body = message.body;
    if (!body?.surface?.id) {
        return;
    }
    const surfaces = [...state.shellState.surfaces];
    const index = surfaces.findIndex((entry) => entry.surface.id === body.surface.id);
    const next = {
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
    }
    else if (index >= 0) {
        surfaces[index] = { ...surfaces[index], ...next };
    }
    else {
        surfaces.push(next);
    }
    surfaces.sort((left, right) => right.updated_at.localeCompare(left.updated_at));
    state.shellState.surfaces = surfaces;
}
function setToken(nextToken) {
    state.token = nextToken.trim();
    if (state.token) {
        localStorage.setItem("agora.shell.token", state.token);
        void connectAll();
    }
    else {
        localStorage.removeItem("agora.shell.token");
    }
    render();
}
async function grantViewport(surfaceId, agentUid) {
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
async function decideEscalation(id, decision, notes, constraints) {
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
function render() {
    const selectedUid = Number.parseInt(state.selectedAgent || "0", 10) || 0;
    const agents = state.shellState.agents || [];
    const filteredAudit = selectedUid
        ? state.auditEvents.filter((event) => event.agent_uid === selectedUid)
        : state.auditEvents;
    const surfacesByUid = new Map();
    for (const surface of state.shellState.surfaces || []) {
        const uid = surface.client?.uid || 0;
        if (!surfacesByUid.has(uid)) {
            surfacesByUid.set(uid, []);
        }
        surfacesByUid.get(uid).push(surface);
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
                <span class="stat-value">${(state.shellState.surfaces || []).length}</span>
              </div>
              <div class="stat">
                <span class="stat-label">Pending Escalations</span>
                <span class="stat-value">${(state.shellState.pending_escalations || []).length}</span>
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
                ${(state.shellState.surfaces || []).length === 0 ? `<div class="surface-empty">No tracked compositor surfaces yet.</div>` : (state.shellState.surfaces || []).map((surface) => renderSurfaceCard(surface)).join("")}
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
                ${(state.shellState.pending_escalations || []).length === 0 ? `<div class="escalation-empty">No pending escalation reviews right now.</div>` : (state.shellState.pending_escalations || []).map(renderEscalationCard).join("")}
              </div>
              <p class="footer-note">Constraints can be entered one per line. The current shell records human decisions and broadcasts them on the event bus for downstream consumers.</p>
            </div>
          </section>
        </div>
      </section>
    </main>
  `;
    document.getElementById("connect-button")?.addEventListener("click", () => {
        const value = document.getElementById("shell-token")?.value || "";
        setToken(value);
    });
    document.getElementById("clear-token-button")?.addEventListener("click", () => {
        setToken("");
    });
    document.getElementById("refresh-button")?.addEventListener("click", () => {
        void refreshState();
    });
    document.getElementById("agent-filter")?.addEventListener("change", (event) => {
        state.selectedAgent = event.target.value;
        render();
    });
    document.querySelectorAll("[data-grant-surface]").forEach((button) => {
        button.addEventListener("click", async (event) => {
            const target = event.currentTarget;
            target.disabled = true;
            try {
                await grantViewport(target.dataset.grantSurface, Number.parseInt(target.dataset.grantUid, 10));
                await refreshState();
            }
            catch (error) {
                alert(error.message);
            }
            finally {
                target.disabled = false;
            }
        });
    });
    document.querySelectorAll("[data-escalation-action]").forEach((button) => {
        button.addEventListener("click", async (event) => {
            const target = event.currentTarget;
            const id = target.dataset.escalationId;
            const notesField = document.querySelector(`[data-escalation-notes="${id}"]`);
            const constraintsField = document.querySelector(`[data-escalation-constraints="${id}"]`);
            target.disabled = true;
            try {
                await decideEscalation(id, target.dataset.escalationAction, notesField?.value || "", (constraintsField?.value || "")
                    .split(/\n+/)
                    .map((line) => line.trim())
                    .filter(Boolean));
            }
            catch (error) {
                alert(error.message);
            }
            finally {
                target.disabled = false;
            }
        });
    });
}
function renderAgentCard(agent, surfaces) {
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
function renderSurfaceCard(surface) {
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
function renderAuditCard(event) {
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
function renderEscalationCard(event) {
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
function formatTime(value) {
    if (!value) {
        return "unknown";
    }
    return new Date(value).toLocaleString();
}
function statusClass(status) {
    if (status === "running") {
        return "success";
    }
    if (status === "exited") {
        return "warn";
    }
    return "danger";
}
function escapeHtml(value) {
    return String(value ?? "")
        .replaceAll("&", "&amp;")
        .replaceAll("<", "&lt;")
        .replaceAll(">", "&gt;")
        .replaceAll('"', "&quot;");
}
function escapeAttr(value) {
    return escapeHtml(value).replaceAll("'", "&#39;");
}
