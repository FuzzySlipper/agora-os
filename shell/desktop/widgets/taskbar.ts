import type { DesktopShellState, ShellWidget, SurfaceActionResponse, SurfaceEvent } from "../../shared/types.js";
import { applyVisualMarker, visualID } from "../visual-markers.js";

export type ShellPublisher = (topic: string, body: unknown) => void;
export type SurfaceFocusAction = (surfaceId: string) => Promise<SurfaceActionResponse>;
export type SurfaceMinimizeAction = (surfaceId: string, enabled: boolean) => Promise<SurfaceActionResponse>;
export type SurfaceRaiseAction = (surfaceId: string) => Promise<SurfaceActionResponse>;

export interface TaskbarWidgetOptions {
    publish?: ShellPublisher;
    focusSurface?: SurfaceFocusAction;
    minimizeSurface?: SurfaceMinimizeAction;
    onFocusResult?: (result: SurfaceActionResponse) => void;
    onOpenCommandCenter?: () => void;
}

interface SurfaceActionStatus {
    pending?: boolean;
    error?: string;
}

interface SurfaceTaskbarItem {
    surface: SurfaceEvent;
    label: string;
    title: string;
    icon: string;
}

export class TaskbarWidget extends HTMLElement implements ShellWidget {
    readonly id = "taskbar";
    readonly layer = 30;
    private publish: ShellPublisher;
    private focusSurface: SurfaceFocusAction;
    private minimizeSurface: SurfaceMinimizeAction;
    private onFocusResult: (result: SurfaceActionResponse) => void;
    private onOpenCommandCenter: () => void;
    private surfaces: SurfaceTaskbarItem[] = [];
    private actionStatus = new Map<string, SurfaceActionStatus>();

    constructor(options: TaskbarWidgetOptions | ShellPublisher = {}) {
        super();
        if (typeof options === "function") {
            this.publish = options;
            this.focusSurface = createSurfaceFocusAction();
            this.minimizeSurface = createSurfaceMinimizeAction();
            this.onFocusResult = () => undefined;
            this.onOpenCommandCenter = () => undefined;
            return;
        }
        this.publish = options.publish ?? (() => undefined);
        this.focusSurface = options.focusSurface ?? createSurfaceFocusAction();
        this.minimizeSurface = options.minimizeSurface ?? createSurfaceMinimizeAction();
        this.onFocusResult = options.onFocusResult ?? (() => undefined);
        this.onOpenCommandCenter = options.onOpenCommandCenter ?? (() => undefined);
    }

    connectedCallback(): void {
        this.classList.add("taskbar-widget");
        applyVisualMarker(this, "taskbar", "taskbar");
    }

    mount(container: HTMLElement): void {
        container.replaceChildren(this);
        this.render();
    }

    unmount(): void {
        this.remove();
    }

    update(state: DesktopShellState): void {
        this.surfaces = surfaceTaskbarItems(state.surfaces);
        for (const id of this.actionStatus.keys()) {
            if (!this.surfaces.some((item) => item.surface.id === id)) {
                this.actionStatus.delete(id);
            }
        }
        this.render();
    }

    private async requestFocus(surface: SurfaceEvent): Promise<void> {
        if (surface.minimized || surface.visibility_state === "minimized") {
            await this.requestRestore(surface);
            return;
        }
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


    private async requestRestore(surface: SurfaceEvent): Promise<void> {
        if (surface.disabled) {
            return;
        }
        this.actionStatus.set(surface.id, { pending: true });
        this.render();
        try {
            const result = await this.minimizeSurface(surface.id, false);
            if (result.decision === "denied") {
                this.actionStatus.set(surface.id, { error: result.error || result.reason || "restore denied" });
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
        const launch = button("taskbar-widget__launch", "⌘", "Open Command Center");
        applyVisualMarker(launch, "taskbar_command_center_button", "command_center_entrypoint");
        launch.title = "Command Center: ask Agora, launch apps, run shell actions";
        launch.dataset.action = "shell.command_center.toggle";
        launch.addEventListener("click", () => this.onOpenCommandCenter());
        const surfaceList = document.createElement("div");
        surfaceList.className = "taskbar-widget__surfaces";
        applyVisualMarker(surfaceList, "taskbar_surface_list", "surface_list");
        for (const item of this.surfaces) {
            const surface = item.surface;
            const status = this.actionStatus.get(surface.id);
            const classes = ["taskbar-widget__surface"];
            if (surface.focused) {
                classes.push("taskbar-widget__surface--focused");
            }
            if (surface.status === "closing") {
                classes.push("taskbar-widget__surface--closing");
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
            if (surface.minimized || surface.visibility_state === "minimized") {
                classes.push("taskbar-widget__surface--minimized");
            }
            const isRestore = Boolean(surface.minimized || surface.visibility_state === "minimized");
            const icon = button(classes.join(" "), item.icon, `${isRestore ? "Restore" : "Focus"} ${item.label}`);
            icon.title = status?.error || surface.action_error || item.title;
            applyVisualMarker(icon, visualID("surface_button", surface.id), "surface_button");
            icon.dataset.surfaceId = surface.id;
            icon.dataset.action = isRestore ? "surface.minimize" : "surface.focus";
            icon.disabled = Boolean(surface.disabled || status?.pending);
            icon.addEventListener("click", () => { void this.requestFocus(surface); });
            const text = document.createElement("span");
            text.className = "taskbar-widget__surface-label";
            text.textContent = item.label;
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

function surfaceTaskbarItems(surfaces: SurfaceEvent[]): SurfaceTaskbarItem[] {
    const grouped = new Map<string, SurfaceEvent[]>();
    for (const surface of surfaces) {
        const key = baseSurfaceLabel(surface);
        grouped.set(key, [...(grouped.get(key) ?? []), surface]);
    }
    return [...surfaces]
        .sort((a, b) => surfaceSortKey(a).localeCompare(surfaceSortKey(b)))
        .map((surface) => {
            const base = baseSurfaceLabel(surface);
            const duplicateGroup = grouped.get(base) ?? [];
            const label = duplicateGroup.length > 1 ? disambiguatedSurfaceLabel(surface, duplicateGroup) : base;
            return {
                surface,
                label,
                title: surfaceTitle(surface, label),
                icon: surfaceIcon(label),
            };
        });
}

function baseSurfaceLabel(surface: SurfaceEvent): string {
    return surface.title || surface.app_id || surface.id;
}

function disambiguatedSurfaceLabel(surface: SurfaceEvent, group: SurfaceEvent[]): string {
    if (surface.app_id && group.some((entry) => entry.app_id !== surface.app_id)) {
        return `${baseSurfaceLabel(surface)} · ${surface.app_id}`;
    }
    return `${baseSurfaceLabel(surface)} · ${surface.id}`;
}

function surfaceTitle(surface: SurfaceEvent, label: string): string {
    const bits = [label, surface.id];
    if (surface.app_id && !label.includes(surface.app_id)) {
        bits.push(surface.app_id);
    }
    if (surface.geometry) {
        bits.push(`${surface.geometry.width}×${surface.geometry.height}+${surface.geometry.x}+${surface.geometry.y}`);
    }
    if (surface.status === "closing") {
        bits.push("closing");
    }
    if (surface.minimized || surface.visibility_state === "minimized") {
        bits.push("minimized · click to restore");
    }
    return bits.join(" · ");
}

function surfaceSortKey(surface: SurfaceEvent): string {
    return `${baseSurfaceLabel(surface)}\u0000${surface.app_id ?? ""}\u0000${surface.id}`;
}

function surfaceIcon(label: string): string {
    return (label.trim()[0] ?? "•").toUpperCase();
}

export class SurfaceFocusError extends Error {
    constructor(readonly result?: SurfaceActionResponse, message = result?.error || result?.reason || "surface.focus failed") {
        super(message);
        this.name = "SurfaceFocusError";
    }
}

export function createSurfaceMinimizeAction(fetcher: typeof fetch = fetch, tokenProvider: () => string | null = shellToken): SurfaceMinimizeAction {
    return async (surfaceId: string, enabled: boolean): Promise<SurfaceActionResponse> => {
        const headers: Record<string, string> = { "Content-Type": "application/json" };
        const token = tokenProvider();
        if (token) {
            headers.Authorization = `Bearer ${token}`;
        }
        const response = await fetcher("/api/shell/surface/minimize", {
            method: "POST",
            headers,
            body: JSON.stringify({ surface_id: surfaceId, enabled }),
        });
        const body = await response.json().catch(() => undefined) as SurfaceActionResponse | { result?: SurfaceActionResponse; error_class?: string } | undefined;
        if (!response.ok) {
            const result = body && "result" in body ? body.result : undefined;
            throw new SurfaceFocusError(result, result?.error || result?.reason || `surface.minimize failed (${response.status})`);
        }
        return body as SurfaceActionResponse;
    };
}

export function createSurfaceRaiseAction(fetcher: typeof fetch = fetch, tokenProvider: () => string | null = shellToken): SurfaceRaiseAction {
    return async (surfaceId: string): Promise<SurfaceActionResponse> => {
        const headers: Record<string, string> = { "Content-Type": "application/json" };
        const token = tokenProvider();
        if (token) {
            headers.Authorization = `Bearer ${token}`;
        }
        const response = await fetcher("/api/shell/surface/raise", {
            method: "POST",
            headers,
            body: JSON.stringify({ surface_id: surfaceId, mode: "no-focus" }),
        });
        const body = await response.json().catch(() => undefined) as SurfaceActionResponse | { result?: SurfaceActionResponse; error_class?: string } | undefined;
        if (!response.ok) {
            const result = body && "result" in body ? body.result : undefined;
            throw new SurfaceFocusError(result, result?.error || result?.reason || `surface.raise failed (${response.status})`);
        }
        return body as SurfaceActionResponse;
    };
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
