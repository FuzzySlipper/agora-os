import type { DesktopShellState, ShellWidget, SurfaceActionResponse, SurfaceEvent } from "../../shared/types.js";
import { createSurfaceFocusAction, SurfaceFocusError, type SurfaceFocusAction } from "./taskbar.js";

export type SurfaceCloseAction = (surfaceId: string) => Promise<SurfaceActionResponse>;
export type SurfaceMoveAction = (surfaceId: string, geometry: { x: number; y: number; width?: number; height?: number }) => Promise<SurfaceActionResponse>;

export interface WindowChromeWidgetOptions {
    focusSurface?: SurfaceFocusAction;
    closeSurface?: SurfaceCloseAction;
    moveSurface?: SurfaceMoveAction;
    onActionResult?: (result: SurfaceActionResponse) => void;
}

interface ChromeActionStatus {
    pending?: string;
    error?: string;
}

export class WindowChromeWidget extends HTMLElement implements ShellWidget {
    readonly id = "window-chrome";
    readonly layer = 20;
    private focusSurface: SurfaceFocusAction;
    private closeSurface: SurfaceCloseAction;
    private moveSurface: SurfaceMoveAction;
    private onActionResult: (result: SurfaceActionResponse) => void;
    private surfaces: SurfaceEvent[] = [];
    private actionStatus = new Map<string, ChromeActionStatus>();

    constructor(options: WindowChromeWidgetOptions = {}) {
        super();
        this.focusSurface = options.focusSurface ?? createSurfaceFocusAction();
        this.closeSurface = options.closeSurface ?? createSurfaceCloseAction();
        this.moveSurface = options.moveSurface ?? createSurfaceMoveAction();
        this.onActionResult = options.onActionResult ?? (() => undefined);
    }

    connectedCallback(): void {
        this.classList.add("window-chrome-widget");
    }

    mount(container: HTMLElement): void {
        container.replaceChildren(this);
        this.render();
    }

    unmount(): void {
        this.remove();
    }

    update(state: DesktopShellState): void {
        this.surfaces = [...state.surfaces]
            .filter((surface) => !isShellSurface(surface))
            .sort((a, b) => surfaceLabel(a).localeCompare(surfaceLabel(b)));
        for (const id of this.actionStatus.keys()) {
            if (!this.surfaces.some((surface) => surface.id === id)) {
                this.actionStatus.delete(id);
            }
        }
        this.render();
    }

    private async requestFocus(surface: SurfaceEvent): Promise<void> {
        await this.runAction(surface, "surface.focus", () => this.focusSurface(surface.id));
    }

    private async requestClose(surface: SurfaceEvent): Promise<void> {
        await this.runAction(surface, "surface.close", () => this.closeSurface(surface.id));
    }

    private async requestMove(surface: SurfaceEvent, dx: number, dy: number): Promise<void> {
        const geometry = surface.geometry;
        if (!geometry) {
            this.actionStatus.set(surface.id, { error: "surface.move requires geometry readback" });
            this.render();
            return;
        }
        await this.runAction(surface, "surface.move", () => this.moveSurface(surface.id, {
            x: geometry.x + dx,
            y: geometry.y + dy,
            width: geometry.width,
            height: geometry.height,
        }));
    }

    private async runAction(surface: SurfaceEvent, action: string, invoke: () => Promise<SurfaceActionResponse>): Promise<void> {
        if (surface.disabled) {
            return;
        }
        this.actionStatus.set(surface.id, { pending: action });
        this.render();
        try {
            const result = await invoke();
            if (result.decision === "denied") {
                this.actionStatus.set(surface.id, { error: result.error || result.reason || `${action} denied` });
            } else {
                this.actionStatus.delete(surface.id);
            }
            this.onActionResult(result);
        } catch (error) {
            const result = error instanceof SurfaceFocusError ? error.result : undefined;
            const message = result?.error || result?.reason || (error instanceof Error ? error.message : String(error));
            this.actionStatus.set(surface.id, { error: message });
            if (result) {
                this.onActionResult(result);
            }
        }
        this.render();
    }

    private render(): void {
        this.replaceChildren();
        const frame = document.createElement("section");
        frame.className = "window-chrome-widget__frame";
        frame.setAttribute("aria-label", "Agora-managed window chrome");

        const header = document.createElement("header");
        header.className = "window-chrome-widget__header";
        const title = document.createElement("span");
        title.textContent = "Work surfaces";
        const subtitle = document.createElement("span");
        subtitle.className = "window-chrome-widget__subtitle";
        subtitle.textContent = this.surfaces.length === 0 ? "No controllable toplevels" : "Agora chrome · same commands as agents";
        header.append(title, subtitle);
        frame.append(header);

        const list = document.createElement("div");
        list.className = "window-chrome-widget__list";
        for (const surface of this.surfaces) {
            list.append(this.renderSurfaceChrome(surface));
        }
        frame.append(list);
        this.append(frame);
    }

    private renderSurfaceChrome(surface: SurfaceEvent): HTMLElement {
        const status = this.actionStatus.get(surface.id);
        const row = document.createElement("article");
        row.className = "window-chrome-widget__surface";
        if (surface.focused) {
            row.classList.add("window-chrome-widget__surface--focused");
        }
        if (status?.error || surface.action_error) {
            row.classList.add("window-chrome-widget__surface--error");
        }
        row.dataset.surfaceId = surface.id;

        const titlebar = document.createElement("div");
        titlebar.className = "window-chrome-widget__titlebar";
        titlebar.dataset.action = "surface.focus";
        titlebar.addEventListener("click", () => { void this.requestFocus(surface); });

        const title = document.createElement("div");
        title.className = "window-chrome-widget__title";
        title.textContent = surfaceLabel(surface);
        const meta = document.createElement("div");
        meta.className = "window-chrome-widget__meta";
        meta.textContent = `${surface.id}${surface.role ? ` · ${surface.role}` : ""}${surface.geometry ? ` · ${surface.geometry.width}×${surface.geometry.height}` : ""}`;
        const titles = document.createElement("div");
        titles.className = "window-chrome-widget__titles";
        titles.append(title, meta);

        const controls = document.createElement("div");
        controls.className = "window-chrome-widget__controls";
        for (const move of [
            { label: "←", dx: -32, dy: 0, name: "left" },
            { label: "→", dx: 32, dy: 0, name: "right" },
            { label: "↑", dx: 0, dy: -32, name: "up" },
            { label: "↓", dx: 0, dy: 32, name: "down" },
        ]) {
            const moveButton = button("window-chrome-widget__button window-chrome-widget__button--move", move.label, `Move ${surfaceLabel(surface)} ${move.name}`);
            moveButton.dataset.action = "surface.move";
            moveButton.dataset.dx = String(move.dx);
            moveButton.dataset.dy = String(move.dy);
            moveButton.disabled = status?.pending === "surface.move" || !surface.geometry;
            moveButton.addEventListener("click", (event) => {
                event.stopPropagation();
                void this.requestMove(surface, move.dx, move.dy);
            });
            controls.append(moveButton);
        }
        const focus = button("window-chrome-widget__button", "Focus", `Focus ${surfaceLabel(surface)}`);
        focus.dataset.action = "surface.focus";
        focus.disabled = status?.pending === "surface.focus";
        focus.addEventListener("click", (event) => {
            event.stopPropagation();
            void this.requestFocus(surface);
        });
        const close = button("window-chrome-widget__button window-chrome-widget__button--close", "×", `Close ${surfaceLabel(surface)}`);
        close.dataset.action = "surface.close";
        close.disabled = status?.pending === "surface.close";
        close.addEventListener("click", (event) => {
            event.stopPropagation();
            void this.requestClose(surface);
        });
        controls.append(focus, close);
        titlebar.append(titles, controls);
        row.append(titlebar);

        const footer = document.createElement("div");
        footer.className = "window-chrome-widget__status";
        footer.textContent = status?.pending ? `${status.pending} pending…` : (status?.error || surface.action_error || surfaceStatusLabel(surface));
        row.append(footer);
        return row;
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

function surfaceStatusLabel(surface: SurfaceEvent): string {
    if (surface.status === "closing") {
        return "Close requested — waiting for compositor unmap";
    }
    return surface.focused ? "Focused" : "Ready";
}

export function createSurfaceMoveAction(fetcher: typeof fetch = fetch, tokenProvider: () => string | null = shellToken): SurfaceMoveAction {
    return async (surfaceId: string, geometry: { x: number; y: number; width?: number; height?: number }): Promise<SurfaceActionResponse> => {
        const headers: Record<string, string> = { "Content-Type": "application/json" };
        const token = tokenProvider();
        if (token) {
            headers.Authorization = `Bearer ${token}`;
        }
        const response = await fetcher("/api/shell/surface/move", {
            method: "POST",
            headers,
            body: JSON.stringify({ surface_id: surfaceId, ...geometry }),
        });
        const body = await response.json().catch(() => undefined) as SurfaceActionResponse | { result?: SurfaceActionResponse; error_class?: string } | undefined;
        if (!response.ok) {
            const result = body && "result" in body ? body.result : undefined;
            throw new SurfaceFocusError(result, result?.error || result?.reason || `surface.move failed (${response.status})`);
        }
        return body as SurfaceActionResponse;
    };
}

function isShellSurface(surface: SurfaceEvent): boolean {
    const text = `${surface.title ?? ""} ${surface.app_id ?? ""} ${surface.role ?? ""}`.toLowerCase();
    return text.includes("agora desktop shell") || text.includes("agora-shell") || surface.role === "panel" || surface.role === "dock" || surface.role === "background";
}

export function createSurfaceCloseAction(fetcher: typeof fetch = fetch, tokenProvider: () => string | null = shellToken): SurfaceCloseAction {
    return async (surfaceId: string): Promise<SurfaceActionResponse> => {
        const headers: Record<string, string> = { "Content-Type": "application/json" };
        const token = tokenProvider();
        if (token) {
            headers.Authorization = `Bearer ${token}`;
        }
        const response = await fetcher("/api/shell/surface/close", {
            method: "POST",
            headers,
            body: JSON.stringify({ surface_id: surfaceId }),
        });
        const body = await response.json().catch(() => undefined) as SurfaceActionResponse | { result?: SurfaceActionResponse; error_class?: string } | undefined;
        if (!response.ok) {
            const result = body && "result" in body ? body.result : undefined;
            throw new SurfaceFocusError(result, result?.error || result?.reason || `surface.close failed (${response.status})`);
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

if (!customElements.get("agora-window-chrome")) {
    customElements.define("agora-window-chrome", WindowChromeWidget);
}
