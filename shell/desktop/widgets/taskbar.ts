import type { DesktopShellState, ShellWidget, SurfaceActionResponse, SurfaceEvent } from "../../shared/types.js";

export type ShellPublisher = (topic: string, body: unknown) => void;
export type SurfaceFocusAction = (surfaceId: string) => Promise<SurfaceActionResponse>;

export interface TaskbarWidgetOptions {
    publish?: ShellPublisher;
    focusSurface?: SurfaceFocusAction;
    onFocusResult?: (result: SurfaceActionResponse) => void;
}

interface SurfaceActionStatus {
    pending?: boolean;
    error?: string;
}

export class TaskbarWidget extends HTMLElement implements ShellWidget {
    readonly id = "taskbar";
    readonly layer = 30;
    private publish: ShellPublisher;
    private focusSurface: SurfaceFocusAction;
    private onFocusResult: (result: SurfaceActionResponse) => void;
    private surfaces: SurfaceEvent[] = [];
    private actionStatus = new Map<string, SurfaceActionStatus>();

    constructor(options: TaskbarWidgetOptions | ShellPublisher = {}) {
        super();
        if (typeof options === "function") {
            this.publish = options;
            this.focusSurface = createSurfaceFocusAction();
            this.onFocusResult = () => undefined;
            return;
        }
        this.publish = options.publish ?? (() => undefined);
        this.focusSurface = options.focusSurface ?? createSurfaceFocusAction();
        this.onFocusResult = options.onFocusResult ?? (() => undefined);
    }

    connectedCallback(): void {
        this.classList.add("taskbar-widget");
    }

    mount(container: HTMLElement): void {
        container.replaceChildren(this);
        this.render();
    }

    unmount(): void {
        this.remove();
    }

    update(state: DesktopShellState): void {
        this.surfaces = [...state.surfaces].sort((a, b) => surfaceLabel(a).localeCompare(surfaceLabel(b)));
        for (const id of this.actionStatus.keys()) {
            if (!this.surfaces.some((surface) => surface.id === id)) {
                this.actionStatus.delete(id);
            }
        }
        this.render();
    }

    private async requestFocus(surface: SurfaceEvent): Promise<void> {
        if (surface.disabled) {
            return;
        }
        this.actionStatus.set(surface.id, { pending: true });
        this.render();
        try {
            const result = await this.focusSurface(surface.id);
            if (result.decision === "denied") {
                this.actionStatus.set(surface.id, { error: result.error || result.reason || "focus denied" });
            } else {
                this.actionStatus.delete(surface.id);
            }
            this.onFocusResult(result);
        } catch (error) {
            const result = error instanceof SurfaceFocusError ? error.result : undefined;
            const message = result?.error || result?.reason || (error instanceof Error ? error.message : String(error));
            this.actionStatus.set(surface.id, { error: message });
            if (result) {
                this.onFocusResult(result);
            }
        }
        this.render();
    }

    private render(): void {
        this.replaceChildren();
        const launch = button("taskbar-widget__launch", "⌘", "Open launcher");
        launch.addEventListener("click", () => this.publish("conversation.turn.requested", { prompt: "Open launcher" }));
        const surfaceList = document.createElement("div");
        surfaceList.className = "taskbar-widget__surfaces";
        for (const surface of this.surfaces) {
            const status = this.actionStatus.get(surface.id);
            const classes = ["taskbar-widget__surface"];
            if (surface.focused) {
                classes.push("taskbar-widget__surface--focused");
            }
            if (status?.pending) {
                classes.push("taskbar-widget__surface--pending");
            }
            if (status?.error || surface.action_error) {
                classes.push("taskbar-widget__surface--error");
            }
            if (surface.disabled) {
                classes.push("taskbar-widget__surface--disabled");
            }
            const label = surfaceLabel(surface);
            const icon = button(classes.join(" "), surfaceIcon(surface), `Focus ${label}`);
            icon.title = status?.error || surface.action_error || `${label} · ${surface.id}`;
            icon.dataset.surfaceId = surface.id;
            icon.dataset.action = "surface.focus";
            icon.disabled = Boolean(surface.disabled || status?.pending);
            icon.addEventListener("click", () => { void this.requestFocus(surface); });
            const text = document.createElement("span");
            text.className = "taskbar-widget__surface-label";
            text.textContent = label;
            icon.append(text);
            surfaceList.append(icon);
        }
        const indicator = document.createElement("span");
        indicator.className = "taskbar-widget__session";
        indicator.textContent = `${this.surfaces.length} surfaces · Agora`;
        this.append(launch, surfaceList, indicator);
    }
}

function button(className: string, text: string, label: string): HTMLButtonElement {
    const node = document.createElement("button");
    node.type = "button";
    node.className = className;
    node.textContent = text;
    node.setAttribute("aria-label", label);
    return node;
}

function surfaceLabel(surface: SurfaceEvent): string {
    return surface.title || surface.app_id || surface.id;
}

function surfaceIcon(surface: SurfaceEvent): string {
    const label = surfaceLabel(surface).trim();
    return (label[0] ?? "•").toUpperCase();
}

export class SurfaceFocusError extends Error {
    constructor(readonly result?: SurfaceActionResponse, message = result?.error || result?.reason || "surface.focus failed") {
        super(message);
        this.name = "SurfaceFocusError";
    }
}

export function createSurfaceFocusAction(fetcher: typeof fetch = fetch, tokenProvider: () => string | null = shellToken): SurfaceFocusAction {
    return async (surfaceId: string): Promise<SurfaceActionResponse> => {
        const headers: Record<string, string> = { "Content-Type": "application/json" };
        const token = tokenProvider();
        if (token) {
            headers.Authorization = `Bearer ${token}`;
        }
        const response = await fetcher("/api/shell/surface/focus", {
            method: "POST",
            headers,
            body: JSON.stringify({ surface_id: surfaceId }),
        });
        const body = await response.json().catch(() => undefined) as SurfaceActionResponse | { result?: SurfaceActionResponse; error_class?: string } | undefined;
        if (!response.ok) {
            const result = body && "result" in body ? body.result : undefined;
            throw new SurfaceFocusError(result, result?.error || result?.reason || `surface.focus failed (${response.status})`);
        }
        return body as SurfaceActionResponse;
    };
}

function shellToken(): string | null {
    const hashToken = new URLSearchParams(globalThis.location?.hash?.replace(/^#/, "") ?? "").get("token");
    return hashToken
        ?? globalThis.sessionStorage?.getItem("agora.shell.token")
        ?? globalThis.localStorage?.getItem("agora.shell.token")
        ?? null;
}

if (!customElements.get("agora-taskbar")) {
    customElements.define("agora-taskbar", TaskbarWidget);
}
