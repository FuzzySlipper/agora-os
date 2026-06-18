import type { BusEnvelope } from "../shared/types.js";
import { applyWidgetContainerLayout } from "./layout.js";

export interface WidgetBus {
    subscribe<TBody = unknown>(topic: string, handler: (envelope: BusEnvelope<TBody>) => void): void;
    publish<TBody = unknown>(topic: string, body: TBody): void;
}

export interface WidgetManifest {
    name: string;
    title?: string;
    position?: string;
    size?: {
        width?: number;
        height?: number;
    };
    bus_topics: string[];
}

export interface WidgetInjectPayload {
    name?: unknown;
    manifest?: unknown;
}

export interface WidgetRemovePayload {
    name?: unknown;
}

export interface WidgetControllerOptions {
    bus: WidgetBus;
    documentRef?: Document;
    windowRef?: Window;
    fetchManifest?: (url: string) => Promise<Response>;
}

interface InjectedWidget {
    name: string;
    manifest: WidgetManifest;
    container: HTMLElement;
    iframe: HTMLIFrameElement;
    active: boolean;
}

const VALID_NAME = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/;
const VALID_TOPIC = /^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$/;

export class WidgetController {
    private readonly bus: WidgetBus;
    private readonly documentRef: Document;
    private readonly windowRef: Window;
    private readonly fetchManifest: (url: string) => Promise<Response>;
    private readonly widgets = new Map<string, InjectedWidget>();
    private installed = false;
    private readonly messageHandler = (event: MessageEvent) => this.handleMessage(event);

    constructor(options: WidgetControllerOptions) {
        this.bus = options.bus;
        this.documentRef = options.documentRef ?? document;
        this.windowRef = options.windowRef ?? window;
        this.fetchManifest = options.fetchManifest ?? ((url) => fetch(url, { cache: "no-store" }));
    }

    install(): void {
        if (this.installed) {
            return;
        }
        this.bus.subscribe<WidgetInjectPayload>("shell.widget.inject", (event) => {
            void this.injectFromPayload(event.body);
        });
        this.bus.subscribe<WidgetRemovePayload>("shell.widget.remove", (event) => this.removeFromPayload(event.body));
        this.windowRef.addEventListener("message", this.messageHandler);
        this.installed = true;
    }

    dispose(): void {
        this.windowRef.removeEventListener("message", this.messageHandler);
        for (const name of Array.from(this.widgets.keys())) {
            this.removeWidget(name);
        }
        this.installed = false;
    }

    async injectFromPayload(payload: unknown): Promise<void> {
        const name = parseWidgetName(payload);
        if (!name) {
            return;
        }
        const manifest = normalizeManifest(readManifest(payload), name) ?? await this.loadManifest(name);
        if (!manifest) {
            return;
        }
        this.injectWidget(manifest);
    }

    injectWidget(manifest: WidgetManifest): void {
        const normalized = normalizeManifest(manifest, manifest.name);
        if (!normalized) {
            return;
        }
        this.removeWidget(normalized.name);
        const grid = this.documentRef.querySelector<HTMLElement>(".shell-grid");
        if (!grid) {
            return;
        }
        const container = this.documentRef.createElement("section");
        container.className = "shell-widget-container injected-widget-container";
        container.dataset.widgetSlot = normalized.name;
        container.setAttribute("aria-label", normalized.title ?? normalized.name);
        applyWidgetContainerLayout(container, normalized.position ?? "center", true);

        const iframe = this.documentRef.createElement("iframe");
        iframe.className = "injected-widget";
        iframe.name = `agora-widget-${normalized.name}`;
        iframe.src = `/api/shell/widget-proxy/${encodeURIComponent(normalized.name)}/index.html`;
        iframe.setAttribute("sandbox", "allow-scripts allow-same-origin");
        iframe.setAttribute("loading", "lazy");
        iframe.setAttribute("title", normalized.title ?? normalized.name);
        if (normalized.size?.width) {
            iframe.style.width = `${normalized.size.width}px`;
        }
        if (normalized.size?.height) {
            iframe.style.height = `${normalized.size.height}px`;
        }
        container.append(iframe);
        grid.append(container);

        const widget: InjectedWidget = { name: normalized.name, manifest: normalized, container, iframe, active: true };
        this.widgets.set(normalized.name, widget);
        this.subscribeWidgetTopics(widget);
    }

    removeWidget(name: string): void {
        const widget = this.widgets.get(name);
        if (!widget) {
            return;
        }
        widget.active = false;
        widget.container.remove();
        this.widgets.delete(name);
    }

    private removeFromPayload(payload: unknown): void {
        const name = parseWidgetName(payload);
        if (name) {
            this.removeWidget(name);
        }
    }

    private async loadManifest(name: string): Promise<WidgetManifest | null> {
        try {
            const response = await this.fetchManifest(`/api/shell/widget-proxy/${encodeURIComponent(name)}/manifest.json`);
            if (!response.ok) {
                return normalizeManifest({ name, bus_topics: [] }, name);
            }
            return normalizeManifest(await response.json(), name);
        } catch {
            return normalizeManifest({ name, bus_topics: [] }, name);
        }
    }

    private subscribeWidgetTopics(widget: InjectedWidget): void {
        for (const topic of widget.manifest.bus_topics) {
            this.bus.subscribe(topic, (event) => {
                if (!widget.active || !this.widgets.has(widget.name)) {
                    return;
                }
                widget.iframe.contentWindow?.postMessage({ type: "event", topic: event.topic, body: event.body }, "*");
            });
        }
    }

    private handleMessage(event: MessageEvent): void {
        const widget = this.widgetForSource(event.source);
        if (!widget || !isPubMessage(event.data)) {
            return;
        }
        const rawTopic = event.data.topic.trim().replace(/^\.+/, "");
        if (!rawTopic || !VALID_TOPIC.test(rawTopic)) {
            return;
        }
        this.bus.publish(`widget.${widget.name}.${rawTopic}`, event.data.body);
    }

    private widgetForSource(source: MessageEventSource | null): InjectedWidget | undefined {
        for (const widget of this.widgets.values()) {
            if (widget.active && widget.iframe.contentWindow === source) {
                return widget;
            }
        }
        return undefined;
    }
}

export function createWidgetController(options: WidgetControllerOptions): WidgetController {
    const controller = new WidgetController(options);
    controller.install();
    return controller;
}

function parseWidgetName(payload: unknown): string | null {
    if (!payload || typeof payload !== "object") {
        return null;
    }
    const name = (payload as { name?: unknown }).name;
    return typeof name === "string" && VALID_NAME.test(name) ? name : null;
}

function readManifest(payload: unknown): unknown {
    return payload && typeof payload === "object" ? (payload as { manifest?: unknown }).manifest : undefined;
}

function normalizeManifest(raw: unknown, fallbackName: string): WidgetManifest | null {
    if (!VALID_NAME.test(fallbackName) || !raw || typeof raw !== "object" || Array.isArray(raw)) {
        return null;
    }
    const source = raw as Record<string, unknown>;
    const name = typeof source.name === "string" && VALID_NAME.test(source.name) ? source.name : fallbackName;
    if (name !== fallbackName) {
        return null;
    }
    const busTopics = Array.isArray(source.bus_topics)
        ? source.bus_topics.filter((topic): topic is string => typeof topic === "string" && VALID_TOPIC.test(topic))
        : [];
    return {
        name,
        title: typeof source.title === "string" ? source.title : undefined,
        position: typeof source.position === "string" ? source.position : "center",
        size: normalizeSize(source.size),
        bus_topics: busTopics,
    };
}

function normalizeSize(raw: unknown): WidgetManifest["size"] {
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
        return undefined;
    }
    const size = raw as Record<string, unknown>;
    const width = typeof size.width === "number" && Number.isFinite(size.width) && size.width > 0 ? size.width : undefined;
    const height = typeof size.height === "number" && Number.isFinite(size.height) && size.height > 0 ? size.height : undefined;
    return width || height ? { width, height } : undefined;
}

function isPubMessage(data: unknown): data is { type: "pub"; topic: string; body?: unknown } {
    return !!data
        && typeof data === "object"
        && (data as { type?: unknown }).type === "pub"
        && typeof (data as { topic?: unknown }).topic === "string";
}
