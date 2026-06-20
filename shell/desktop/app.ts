import { createBusConnection, type BusConnection } from "../shared/bus.js";
import type { AgentInfo, BusEnvelope, DesktopShellState, ShellNotification, ShellWidget, SurfaceActionResponse, SurfaceEvent } from "../shared/types.js";
import { AgentHealthWidget } from "./widgets/agent-health.js";
import { ClockWidget } from "./widgets/clock.js";
import { NotificationCenter } from "./widgets/notification-center.js";
import { TaskbarWidget } from "./widgets/taskbar.js";
import { WindowChromeWidget } from "./widgets/window-chrome.js";
import { createLayoutController, type LayoutController } from "./layout.js";
import { createThemeController, type ThemeController } from "./theme.js";
import { createWidgetController, type WidgetController } from "./widgets.js";

const DEFAULT_SUBSCRIPTIONS = [
    "compositor.surface.*",
    "compositor.advisory.surface.*",
    "agent.lifecycle.*",
    "agent.work.progress",
    "conversation.turn.responded",
    "shell.theme",
    "shell.apply_theme",
    "shell.reset_theme",
    "shell.action.*",
    "shell.layout_updated",
    "shell.widget.inject",
    "shell.widget.remove",
];

const emptyState = (): DesktopShellState => ({
    surfaces: [],
    agents: [],
    notifications: [],
    config: {
        theme: {
            background: "var(--shell-bg)",
            accent: "var(--shell-accent)",
        },
    },
});

export interface ShellStateSnapshot {
    agents?: AgentInfo[];
    surfaces?: Array<{ surface?: SurfaceEvent; focused?: boolean; visible?: boolean; updated_at?: string } | SurfaceEvent>;
}


export class ShellApp {
    private readonly bus: BusConnection;
    private readonly widgets = new Map<string, ShellWidget>();
    private state: DesktopShellState = emptyState();
    private root: HTMLElement | null = null;
    private mounted = false;
    private subscribed = false;
    private stateRefreshTimer: ReturnType<typeof setInterval> | undefined;
    private readonly theme: ThemeController;
    private readonly layout: LayoutController;
    private readonly injectedWidgets: WidgetController;

    constructor(bus: BusConnection = createBusConnection({ protocols: tokenProtocolsFromStorage() })) {
        this.bus = bus;
        this.theme = createThemeController(this.bus);
        this.layout = createLayoutController({ bus: this.bus, onTheme: (theme) => this.theme.applyTheme(theme) });
        this.injectedWidgets = createWidgetController({ bus: this.bus });
        this.registerDefaultWidgets();
    }

    mount(container: HTMLElement): void {
        if (this.mounted) {
            this.unmount();
        }
        this.root = container;
        this.mounted = true;
        this.root.classList.add("desktop-shell");
        this.root.innerHTML = shellLayout();
        for (const widget of this.widgets.values()) {
            this.mountWidget(widget);
        }
        this.connectBus();
        this.update(this.state);
        void this.refreshShellStateSnapshot();
        this.stateRefreshTimer = setInterval(() => { void this.refreshShellStateSnapshot(); }, 15_000);
        void this.layout.loadFromServer();
        void this.injectedWidgets.loadFromServerLayout();
    }

    unmount(): void {
        if (this.stateRefreshTimer) {
            clearInterval(this.stateRefreshTimer);
            this.stateRefreshTimer = undefined;
        }
        this.bus.disconnect();
        for (const widget of this.widgets.values()) {
            widget.unmount();
        }
        if (this.root) {
            this.root.innerHTML = "";
            this.root.classList.remove("desktop-shell");
        }
        this.root = null;
        this.mounted = false;
    }

    registerWidget(widget: ShellWidget): void {
        const existing = this.widgets.get(widget.id);
        if (existing) {
            existing.unmount();
        }
        this.widgets.set(widget.id, widget);
        if (this.mounted) {
            this.mountWidget(widget);
            widget.update(this.state);
        }
    }

    getWidget(id: string): ShellWidget | undefined {
        return this.widgets.get(id);
    }

    private registerDefaultWidgets(): void {
        this.registerWidget(new AgentHealthWidget());
        this.registerWidget(new ClockWidget());
        this.registerWidget(new NotificationCenter());
        const applyLocalActionResult = (result: SurfaceActionResponse): void => {
            const next = cloneState(this.state);
            applySurfaceActionEvent(next, {
                topic: result.decision === "denied" ? "shell.action.denied" : "shell.action.completed",
                body: result,
                timestamp: new Date().toISOString(),
            });
            this.update(next);
        };
        this.registerWidget(new WindowChromeWidget({ onActionResult: applyLocalActionResult }));
        this.registerWidget(new TaskbarWidget({
            publish: (topic, body) => this.bus.publish(topic, body),
            onFocusResult: applyLocalActionResult,
        }));
    }

    update(state: DesktopShellState): void {
        this.state = state;
        this.renderShellState();
        for (const widget of this.widgets.values()) {
            widget.update(state);
        }
    }

    private async refreshShellStateSnapshot(): Promise<void> {
        if (typeof fetch !== "function") {
            return;
        }
        const headers: Record<string, string> = {};
        const token = tokenFromLocationOrStorage();
        if (token) {
            headers.Authorization = `Bearer ${token}`;
        }
        try {
            const response = await fetch("/api/shell/state", { cache: "no-store", headers });
            if (!response.ok) {
                return;
            }
            const snapshot = await response.json() as ShellStateSnapshot;
            const next = cloneState(this.state);
            applyShellStateSnapshot(next, snapshot);
            this.update(next);
        } catch {
            // Event-bus updates remain the primary path; snapshots are best-effort stale cleanup/readback.
        }
    }

    private connectBus(): void {
        if (!this.subscribed) {
            for (const topic of DEFAULT_SUBSCRIPTIONS) {
                this.bus.subscribe(topic, (event) => this.handleBusEvent(event));
            }
            this.subscribed = true;
        }
        this.bus.connect();
    }

    private handleBusEvent(event: BusEnvelope): void {
        const next = cloneState(this.state);
        if (event.topic.startsWith("compositor.surface.") || event.topic.startsWith("compositor.advisory.surface.")) {
            applySurfaceEvent(next, event);
        } else if (event.topic.startsWith("shell.action.")) {
            applySurfaceActionEvent(next, event);
        } else if (event.topic.startsWith("agent.lifecycle.")) {
            applyAgentEvent(next, event);
        } else if (event.topic === "shell.theme") {
            next.config.theme = { ...next.config.theme, ...(event.body as Record<string, unknown>) };
            applyTheme(next.config.theme ?? {});
        } else if (event.topic === "agent.work.progress" || event.topic === "conversation.turn.responded") {
            next.notifications = [notificationFromEvent(event), ...next.notifications].slice(0, 8);
        }
        this.update(next);
    }

    private mountWidget(widget: ShellWidget): void {
        const slot = this.root?.querySelector<HTMLElement>(`[data-widget-slot="${widget.id}"]`) ?? this.root;
        if (!slot) {
            return;
        }
        widget.mount(slot);
    }

    private renderShellState(): void {
        if (!this.root) {
            return;
        }
        const surfaceCount = this.root.querySelector<HTMLElement>("[data-surface-count]");
        if (surfaceCount) {
            surfaceCount.textContent = String(this.state.surfaces.length);
        }
        const agentCount = this.root.querySelector<HTMLElement>("[data-agent-count]");
        if (agentCount) {
            agentCount.textContent = String(this.state.agents.length);
        }
    }
}

function shellLayout(): string {
    return `
        <section class="shell-background" aria-hidden="true"></section>
        <section class="shell-grid" aria-label="Agora desktop shell">
            <div class="shell-widget-container shell-zone pos-top-left" data-widget-slot="agent-health"></div>
            <div class="shell-widget-container shell-zone pos-top-right" data-widget-slot="clock"></div>
            <div class="shell-widget-container shell-zone pos-center" data-widget-slot="window-chrome"></div>
            <div class="shell-widget-container shell-zone pos-bottom-right" data-widget-slot="notifications"></div>
            <nav class="shell-widget-container shell-taskbar pos-bottom" data-widget-slot="taskbar" aria-label="Desktop taskbar"></nav>
        </section>`;
}

function cloneState(state: DesktopShellState): DesktopShellState {
    return {
        surfaces: [...state.surfaces],
        agents: [...state.agents],
        notifications: [...state.notifications],
        config: { ...state.config, theme: { ...state.config.theme } },
    };
}

export function applyShellStateSnapshot(state: DesktopShellState, snapshot: ShellStateSnapshot): void {
    const live = new Map<string, SurfaceEvent>();
    for (const entry of snapshot.surfaces ?? []) {
        const candidate = "surface" in entry && entry.surface ? entry.surface : entry as SurfaceEvent;
        if (!candidate?.id) {
            continue;
        }
        live.set(candidate.id, { ...candidate, focused: ("focused" in entry && typeof entry.focused === "boolean") ? entry.focused : candidate.focused });
    }
    state.surfaces = [...live.values()].sort((a, b) => stableSurfaceSort(a).localeCompare(stableSurfaceSort(b)));
    if (snapshot.agents) {
        state.agents = snapshot.agents;
    }
}

function stableSurfaceSort(surface: SurfaceEvent): string {
    return `${surface.title ?? ""}\u0000${surface.app_id ?? ""}\u0000${surface.id}`;
}

export function applySurfaceEvent(state: DesktopShellState, event: BusEnvelope): void {
    const body = event.body as { surface?: SurfaceEvent; id?: string; event?: string } | undefined;
    const surface = body?.surface ?? (body?.id ? { id: body.id } : undefined);
    if (!surface?.id) {
        return;
    }
    if (event.topic.endsWith(".unmapped") || body?.event === "unmapped") {
        state.surfaces = state.surfaces.filter((entry) => entry.id !== surface.id);
        return;
    }
    const existing = state.surfaces.findIndex((entry) => entry.id === surface.id);
    if (existing >= 0) {
        state.surfaces[existing] = { ...state.surfaces[existing], ...surface };
    } else {
        state.surfaces.push(surface);
    }
}

export function applySurfaceActionEvent(state: DesktopShellState, event: BusEnvelope): void {
    const body = event.body as SurfaceActionResponse | undefined;
    if (!body || !body.surface_id) {
        return;
    }
    if (event.topic === "shell.action.completed" && body.decision !== "denied") {
        if (body.action === "surface.focus") {
            const focusedID = body.focused_surface_id || body.surface_id;
            state.surfaces = state.surfaces.map((entry) => ({ ...entry, focused: entry.id === focusedID, action_error: undefined, disabled: false }));
            mergeActionReadback(state, body, focusedID);
            return;
        }
        if (body.action === "surface.close") {
            state.surfaces = state.surfaces.map((entry) => entry.id === body.surface_id ? { ...entry, status: "closing", action_error: undefined } : entry);
            mergeActionReadback(state, body);
            return;
        }
    }
    if (body.action !== "surface.focus" && body.action !== "surface.close") {
        return;
    }
    const message = body.error || body.reason || `${body.action} denied`;
    if (message.toLowerCase().includes("stale") || message.toLowerCase().includes("not found") || message.toLowerCase().includes("unmapped")) {
        state.surfaces = state.surfaces.filter((entry) => entry.id !== body.surface_id);
        return;
    }
    state.surfaces = state.surfaces.map((entry) => entry.id === body.surface_id ? { ...entry, action_error: message, disabled: true } : entry);
}

function mergeActionReadback(state: DesktopShellState, body: SurfaceActionResponse, focusedID?: string): void {
    const readback = body.surface?.surface;
    if (!readback?.id) {
        return;
    }
    const existing = state.surfaces.findIndex((entry) => entry.id === readback.id);
    const merged: SurfaceEvent = { ...(existing >= 0 ? state.surfaces[existing] : {}), ...readback, focused: body.surface?.focused ?? (readback.id === focusedID) };
    if (existing >= 0) {
        state.surfaces[existing] = merged;
    } else {
        state.surfaces.push(merged);
    }
}

function applyAgentEvent(state: DesktopShellState, event: BusEnvelope): void {
    if (!event.body || typeof event.body !== "object") {
        return;
    }
    const body = event.body as { agent?: Partial<AgentInfo> } | Partial<AgentInfo>;
    const rawAgent = ("agent" in body ? body.agent : body) as Partial<AgentInfo> | undefined;
    const identity = rawAgent?.identity ?? rawAgent?.name ?? (rawAgent?.uid === undefined ? undefined : String(rawAgent.uid));
    if (!identity || !rawAgent?.status) {
        return;
    }
    const agent: AgentInfo = { ...rawAgent, identity, status: rawAgent.status };
    const existing = state.agents.findIndex((entry) => entry.identity === agent.identity);
    if (existing >= 0) {
        state.agents[existing] = { ...state.agents[existing], ...agent };
    } else {
        state.agents.push(agent);
    }
}

function notificationFromEvent(event: BusEnvelope): ShellNotification {
    const body = event.body as Record<string, unknown> | undefined;
    return {
        id: `${event.topic}:${event.timestamp ?? Date.now()}`,
        title: event.topic,
        message: String(body?.message ?? body?.summary ?? "Shell event received"),
        level: "info",
        timestamp: event.timestamp ?? new Date().toISOString(),
        topic: event.topic,
    };
}

function applyTheme(theme: Record<string, unknown>): void {
    const root = document.documentElement;
    if (typeof theme.background === "string") {
        root.style.setProperty("--shell-bg", theme.background);
    }
    if (typeof theme.accent === "string") {
        root.style.setProperty("--shell-accent", theme.accent);
    }
}

function tokenProtocolsFromStorage(): string[] | undefined {
    const token = tokenFromLocationOrStorage();
    return token ? [`agora.token.${token}`] : undefined;
}

function tokenFromLocationOrStorage(): string | null {
    return new URLSearchParams(window.location.hash.replace(/^#/, "")).get("token")
        ?? sessionStorage.getItem("agora.shell.token")
        ?? localStorage.getItem("agora.shell.token");
}

async function tokenProtocolsForBootstrap(): Promise<string[] | undefined> {
    const existing = tokenFromLocationOrStorage();
    if (existing) {
        return [`agora.token.${existing}`];
    }
    try {
        const response = await fetch("/api/shell/session-token", { cache: "no-store" });
        if (!response.ok) {
            return undefined;
        }
        const body = await response.json() as { token?: unknown };
        if (typeof body.token !== "string" || body.token === "") {
            return undefined;
        }
        sessionStorage.setItem("agora.shell.token", body.token);
        return [`agora.token.${body.token}`];
    } catch {
        return undefined;
    }
}

async function mountDefaultShell(widgetRoot: HTMLElement): Promise<void> {
    const app = new ShellApp(createBusConnection({ protocols: await tokenProtocolsForBootstrap() }));
    app.mount(widgetRoot);
    Object.assign(window, { agoraDesktopShell: app });
}

const widgetRoot = document.getElementById("widget-root");
if (widgetRoot) {
    void mountDefaultShell(widgetRoot);
}
