import type { CommandCenterState, ConversationTurnRequest, DesktopShellState, ShellWidget, SurfaceActionResponse, SurfaceEvent } from "../../shared/types.js";
import { createSurfaceFocusAction, SurfaceFocusError, type SurfaceFocusAction } from "./taskbar.js";

export type ShellPublisher = (topic: string, body: unknown) => void;

export interface CommandCenterWidgetOptions {
    publish?: ShellPublisher;
    focusSurface?: SurfaceFocusAction;
    onFocusResult?: (result: SurfaceActionResponse) => void;
    onPromptSubmit?: (request: ConversationTurnRequest) => void;
    onClose?: () => void;
    sessionId?: string;
    turnIdFactory?: () => string;
}

export class CommandCenterWidget extends HTMLElement implements ShellWidget {
    readonly id = "command-center";
    readonly layer = 25;
    private publish: ShellPublisher;
    private focusSurface: SurfaceFocusAction;
    private onFocusResult: (result: SurfaceActionResponse) => void;
    private onPromptSubmit: (request: ConversationTurnRequest) => void;
    private onClose: () => void;
    private sessionId: string;
    private turnIdFactory: () => string;
    private surfaces: SurfaceEvent[] = [];
    private commandCenter: CommandCenterState = { open: false, transcript: [] };
    private localError: string | undefined;

    constructor(options: CommandCenterWidgetOptions = {}) {
        super();
        this.publish = options.publish ?? (() => undefined);
        this.focusSurface = options.focusSurface ?? createSurfaceFocusAction();
        this.onFocusResult = options.onFocusResult ?? (() => undefined);
        this.onPromptSubmit = options.onPromptSubmit ?? ((request) => this.publish("conversation.turn.requested", request));
        this.onClose = options.onClose ?? (() => undefined);
        this.sessionId = options.sessionId ?? stableSessionID();
        this.turnIdFactory = options.turnIdFactory ?? (() => `turn:${Date.now().toString(36)}:${Math.random().toString(36).slice(2, 8)}`);
    }

    connectedCallback(): void {
        this.classList.add("command-center-widget");
    }

    mount(container: HTMLElement): void {
        container.append(this);
        this.render();
    }

    unmount(): void {
        this.remove();
    }

    update(state: DesktopShellState): void {
        this.surfaces = [...state.surfaces].filter((surface) => surface.role !== "layer-shell" && surface.status !== "closing");
        this.commandCenter = state.commandCenter;
        this.render();
    }

    private close(): void {
        this.localError = undefined;
        this.onClose();
    }

    private submitPrompt(input: HTMLInputElement): void {
        const prompt = input.value.trim();
        if (!prompt) {
            this.localError = "Type a prompt before submitting.";
            this.render();
            return;
        }
        this.localError = undefined;
        const request: ConversationTurnRequest = {
            session_id: this.sessionId,
            turn_id: this.turnIdFactory(),
            prompt,
            context: {
                source: "desktop-command-center",
                focused_surface_id: this.surfaces.find((surface) => surface.focused)?.id,
                visible_surface_ids: this.surfaces.map((surface) => surface.id),
            },
        };
        this.onPromptSubmit(request);
    }

    private async focusSurfaceRow(surface: SurfaceEvent): Promise<void> {
        this.localError = undefined;
        this.render();
        try {
            const result = await this.focusSurface(surface.id);
            this.onFocusResult(result);
        } catch (error) {
            const result = error instanceof SurfaceFocusError ? error.result : undefined;
            if (result) {
                this.onFocusResult(result);
            }
            const message = result?.error || result?.reason || (error instanceof Error ? error.message : String(error));
            this.localError = message;
            this.render();
        }
    }

    private render(): void {
        this.replaceChildren();
        if (!this.commandCenter.open) {
            return;
        }
        const scrim = document.createElement("div");
        scrim.className = "command-center-widget__scrim";
        scrim.addEventListener("click", (event) => {
            if (event.target === scrim) {
                this.close();
            }
        });

        const panel = document.createElement("section");
        panel.className = "command-center-widget__panel";
        panel.setAttribute("role", "dialog");
        panel.setAttribute("aria-modal", "true");
        panel.setAttribute("aria-label", "Command Center");
        panel.addEventListener("keydown", (event) => {
            if (event instanceof KeyboardEvent && event.key === "Escape") {
                event.preventDefault();
                this.close();
            }
        });

        const header = document.createElement("header");
        header.className = "command-center-widget__header";
        const title = document.createElement("div");
        title.innerHTML = `<strong>Command Center</strong><span>Ask Agora, launch apps, or control surfaces</span>`;
        const close = button("command-center-widget__close", "×", "Close Command Center");
        close.addEventListener("click", () => this.close());
        header.append(title, close);

        const form = document.createElement("form");
        form.className = "command-center-widget__prompt";
        const input = document.createElement("input");
        input.type = "text";
        input.name = "prompt";
        input.placeholder = "Ask or type a command…";
        input.value = this.commandCenter.query ?? "";
        input.setAttribute("aria-label", "Ask Agora or type a command");
        const submit = button("command-center-widget__submit", "Ask", "Submit prompt");
        submit.type = "submit";
        form.append(input, submit);
        form.addEventListener("submit", (event) => {
            event.preventDefault();
            this.submitPrompt(input);
        });

        const suggestions = document.createElement("section");
        suggestions.className = "command-center-widget__suggestions";
        const suggestionsTitle = document.createElement("h3");
        suggestionsTitle.textContent = "Suggested";
        suggestions.append(suggestionsTitle, askRow(), ...this.surfaces.map((surface) => this.surfaceRow(surface)), disabledLaunchRow("Terminal"), disabledLaunchRow("Browser"));

        const transcript = this.transcriptNode();
        const errorText = this.commandCenter.error ?? this.localError;
        if (errorText) {
            const error = document.createElement("p");
            error.className = "command-center-widget__error";
            error.textContent = errorText;
            panel.append(header, form, error, suggestions, transcript);
        } else {
            panel.append(header, form, suggestions, transcript);
        }
        scrim.append(panel);
        this.append(scrim);
        setTimeout(() => input.focus(), 0);
    }

    private surfaceRow(surface: SurfaceEvent): HTMLButtonElement {
        const label = surfaceLabel(surface);
        const row = button("command-center-widget__row", `Focus: ${label}`, `Focus ${label}`);
        row.dataset.action = "surface.focus";
        row.dataset.surfaceId = surface.id;
        row.addEventListener("click", () => { void this.focusSurfaceRow(surface); });
        const meta = document.createElement("span");
        meta.className = "command-center-widget__row-meta";
        meta.textContent = surface.focused ? "focused" : surface.id;
        row.append(meta);
        return row;
    }

    private transcriptNode(): HTMLElement {
        const box = document.createElement("section");
        box.className = "command-center-widget__transcript";
        const title = document.createElement("h3");
        title.textContent = "Conversation";
        box.append(title);
        if (this.commandCenter.transcript.length === 0) {
            const empty = document.createElement("p");
            empty.className = "command-center-widget__empty";
            empty.textContent = "No Command Center turns yet.";
            box.append(empty);
            return box;
        }
        for (const entry of this.commandCenter.transcript.slice(0, 6)) {
            const item = document.createElement("article");
            item.className = `command-center-widget__turn command-center-widget__turn--${entry.status}`;
            const prompt = document.createElement("p");
            prompt.textContent = entry.prompt ? `You: ${entry.prompt}` : `Turn ${entry.turn_id}`;
            const response = document.createElement("p");
            response.textContent = entry.status === "pending" ? "Agora: thinking…" : `Agora: ${entry.response ?? "responded"}`;
            item.append(prompt, response);
            box.append(item);
        }
        return box;
    }
}

function askRow(): HTMLButtonElement {
    const row = button("command-center-widget__row command-center-widget__row--disabled", "Ask Agora about current task", "Ask Agora about current task");
    row.disabled = true;
    row.dataset.action = "conversation.prompt";
    const meta = document.createElement("span");
    meta.className = "command-center-widget__row-meta";
    meta.textContent = "type in the prompt box";
    row.append(meta);
    return row;
}

function disabledLaunchRow(name: string): HTMLButtonElement {
    const row = button("command-center-widget__row command-center-widget__row--disabled", `Launch: ${name}`, `Launch ${name} disabled`);
    row.disabled = true;
    row.dataset.action = "app.launch";
    const meta = document.createElement("span");
    meta.className = "command-center-widget__row-meta";
    meta.textContent = "not wired yet (#3024)";
    row.append(meta);
    return row;
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

function stableSessionID(): string {
    const key = "agora.desktop.command_center.session_id";
    const existing = globalThis.sessionStorage?.getItem(key);
    if (existing) {
        return existing;
    }
    const value = `desktop-shell:${Date.now().toString(36)}:${Math.random().toString(36).slice(2, 8)}`;
    globalThis.sessionStorage?.setItem(key, value);
    return value;
}

if (!customElements.get("agora-command-center")) {
    customElements.define("agora-command-center", CommandCenterWidget);
}
