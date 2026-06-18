import type { BusEnvelope } from "../shared/types.js";

export interface LayoutBus {
    subscribe<TBody = unknown>(topic: string, handler: (envelope: BusEnvelope<TBody>) => void): void;
}

export interface WidgetLayoutConfig {
    visible?: unknown;
    position?: unknown;
    order?: unknown;
}

export interface ShellLayoutConfig {
    widgets?: unknown;
    theme?: unknown;
}

export interface LayoutControllerOptions {
    bus: LayoutBus;
    documentRef?: Document;
    fetchLayout?: (url: string) => Promise<Response>;
    layoutURL?: string;
    onTheme?: (theme: unknown) => void;
}

const DEFAULT_LAYOUT_URL = "/api/shell/layout.json";
const POSITION_CLASSES = ["pos-top-left", "pos-top-right", "pos-bottom", "pos-bottom-right", "pos-center"];
const VALID_POSITIONS = new Set(["top-left", "top-right", "bottom", "bottom-right", "center"]);

const DEFAULT_POSITIONS: Record<string, string> = {
    "agent-health": "top-left",
    clock: "top-right",
    center: "center",
    notifications: "bottom-right",
    taskbar: "bottom",
};

export class LayoutController {
    private readonly bus: LayoutBus;
    private readonly documentRef: Document;
    private readonly fetchLayout: (url: string) => Promise<Response>;
    private readonly layoutURL: string;
    private readonly onTheme?: (theme: unknown) => void;
    private installed = false;

    constructor(options: LayoutControllerOptions) {
        this.bus = options.bus;
        this.documentRef = options.documentRef ?? document;
        this.fetchLayout = options.fetchLayout ?? ((url) => fetch(url, { cache: "no-store" }));
        this.layoutURL = options.layoutURL ?? DEFAULT_LAYOUT_URL;
        this.onTheme = options.onTheme;
    }

    install(): void {
        if (this.installed) {
            return;
        }
        this.bus.subscribe<ShellLayoutConfig>("shell.layout_updated", (event) => {
            if (event.body && typeof event.body === "object" && "widgets" in event.body) {
                this.applyLayout(event.body);
                return;
            }
            void this.loadFromServer();
        });
        this.installed = true;
    }

    async loadFromServer(): Promise<void> {
        try {
            const response = await this.fetchLayout(this.layoutURL);
            if (!response.ok) {
                this.applyLayout(undefined);
                return;
            }
            this.applyLayout(await response.json() as ShellLayoutConfig);
        } catch {
            this.applyLayout(undefined);
        }
    }

    applyLayout(layout: unknown): void {
        const config = normalizeLayout(layout);
        for (const container of this.widgetContainers()) {
            const widgetID = container.dataset.widgetSlot ?? "";
            const widgetConfig = config.widgets[widgetID];
            const defaultPosition = DEFAULT_POSITIONS[widgetID] ?? "center";
            const position = widgetConfig?.position === undefined
                ? defaultPosition
                : normalizePosition(widgetConfig.position, "center");
            const visible = widgetConfig?.visible !== false;
            const order = typeof widgetConfig?.order === "number" ? widgetConfig.order : undefined;
            applyWidgetContainerLayout(container, position, visible, order);
        }
        if (config.theme !== undefined) {
            this.onTheme?.(config.theme);
        }
    }

    private widgetContainers(): HTMLElement[] {
        return Array.from(this.documentRef.querySelectorAll<HTMLElement>("[data-widget-slot]"));
    }
}

export function createLayoutController(options: LayoutControllerOptions): LayoutController {
    const controller = new LayoutController(options);
    controller.install();
    return controller;
}

export function applyWidgetContainerLayout(container: HTMLElement, position: string, visible: boolean, order?: number): void {
    for (const className of POSITION_CLASSES) {
        container.classList.remove(className);
    }
    container.classList.add(`pos-${normalizePosition(position, "center")}`);
    container.hidden = !visible;
    container.style.display = visible ? "" : "none";
    if (order === undefined) {
        container.style.removeProperty("order");
    } else {
        container.style.setProperty("order", String(order));
    }
}

function normalizeLayout(layout: unknown): { widgets: Record<string, { visible?: boolean; position?: string; order?: number }>; theme?: unknown } {
    if (!layout || typeof layout !== "object") {
        return { widgets: {} };
    }
    const raw = layout as ShellLayoutConfig;
    const widgets: Record<string, { visible?: boolean; position?: string; order?: number }> = {};
    if (raw.widgets && typeof raw.widgets === "object" && !Array.isArray(raw.widgets)) {
        for (const [id, value] of Object.entries(raw.widgets as Record<string, WidgetLayoutConfig>)) {
            if (!value || typeof value !== "object" || Array.isArray(value)) {
                continue;
            }
            widgets[id] = {
                visible: typeof value.visible === "boolean" ? value.visible : undefined,
                position: typeof value.position === "string" ? value.position : undefined,
                order: typeof value.order === "number" && Number.isFinite(value.order) ? value.order : undefined,
            };
        }
    }
    return { widgets, theme: raw.theme };
}

function normalizePosition(position: unknown, fallback: string): string {
    if (typeof position === "string" && VALID_POSITIONS.has(position)) {
        return position;
    }
    return VALID_POSITIONS.has(fallback) ? fallback : "center";
}
