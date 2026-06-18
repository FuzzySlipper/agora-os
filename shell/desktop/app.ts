import { createBusConnection, type BusConnection } from "../shared/bus.js";
import type { AgentInfo, BusEnvelope, DesktopShellState, ShellNotification, ShellWidget, SurfaceEvent } from "../shared/types.js";

const DEFAULT_SUBSCRIPTIONS = [
    "compositor.surface.*",
    "compositor.advisory.surface.*",
    "agent.lifecycle.*",
    "agent.work.progress",
    "conversation.turn.responded",
    "shell.theme",
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

export class ShellApp {
    private readonly bus: BusConnection;
    private readonly widgets = new Map<string, ShellWidget>();
    private state: DesktopShellState = emptyState();
    private root: HTMLElement | null = null;
    private mounted = false;
    private subscribed = false;
    private clockTimer: number | null = null;

    constructor(bus: BusConnection = createBusConnection({ protocols: tokenProtocols() })) {
        this.bus = bus;
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
        this.startClock();
        this.update(this.state);
    }

    unmount(): void {
        this.bus.disconnect();
        if (this.clockTimer !== null) {
            window.clearInterval(this.clockTimer);
            this.clockTimer = null;
        }
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

    update(state: DesktopShellState): void {
        this.state = state;
        this.renderShellState();
        for (const widget of this.widgets.values()) {
            widget.update(state);
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

    private startClock(): void {
        if (this.clockTimer !== null) {
            window.clearInterval(this.clockTimer);
        }
        const tick = () => {
            const clock = this.root?.querySelector<HTMLTimeElement>(".shell-clock");
            if (!clock) {
                return;
            }
            const now = new Date();
            clock.dateTime = now.toISOString();
            clock.textContent = now.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
        };
        tick();
        this.clockTimer = window.setInterval(tick, 30_000);
    }
}

function shellLayout(): string {
    return `
        <section class="shell-background" aria-hidden="true"></section>
        <section class="shell-grid" aria-label="Agora desktop shell">
            <div class="shell-zone shell-zone--top-left" data-widget-slot="agent-health">
                <span class="shell-health-dot"></span>
                <span><span data-agent-count>0</span> agents</span>
            </div>
            <div class="shell-zone shell-zone--top-right" data-widget-slot="clock">
                <time class="shell-clock">--:--</time>
            </div>
            <div class="shell-zone shell-zone--center" data-widget-slot="center"></div>
            <div class="shell-zone shell-zone--bottom-right" data-widget-slot="notifications"></div>
            <nav class="shell-taskbar" data-widget-slot="taskbar" aria-label="Desktop taskbar">
                <button class="shell-launcher" type="button" aria-label="Open launcher">⌘</button>
                <span class="shell-surface-count"><span data-surface-count>0</span> surfaces</span>
            </nav>
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

function applySurfaceEvent(state: DesktopShellState, event: BusEnvelope): void {
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

function applyAgentEvent(state: DesktopShellState, event: BusEnvelope): void {
    if (!event.body || typeof event.body !== "object") {
        return;
    }
    const body = event.body as { agent?: AgentInfo } | AgentInfo;
    const agent = ("agent" in body ? body.agent : body) as AgentInfo | undefined;
    if (!agent?.identity) {
        return;
    }
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

function tokenProtocols(): string[] | undefined {
    const token = new URLSearchParams(window.location.hash.replace(/^#/, "")).get("token")
        ?? localStorage.getItem("agora.shell.token");
    return token ? [`agora.token.${token}`] : undefined;
}

const widgetRoot = document.getElementById("widget-root");
if (widgetRoot) {
    const app = new ShellApp();
    app.mount(widgetRoot);
    Object.assign(window, { agoraDesktopShell: app });
}
