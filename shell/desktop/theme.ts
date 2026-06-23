import type { BusEnvelope } from "../shared/types.js";

export interface ThemeBus {
    subscribe<TBody = unknown>(topic: string, handler: (envelope: BusEnvelope<TBody>) => void): void;
    publish<TBody = unknown>(topic: string, body: TBody): void;
}

export interface ThemeApplyPayload {
    properties?: unknown;
    wallpaper_url?: unknown;
    css_url?: unknown;
}

export interface ThemeAppliedSummary {
    properties: Record<string, string>;
    wallpaper_url?: string;
    css_url?: string;
    reset?: boolean;
    skipped: number;
}

const THEME_LINK_ATTRIBUTE = "data-agora-theme-link";
const CUSTOM_PROPERTY_RE = /^(?:--)?[a-zA-Z][a-zA-Z0-9_-]*$/;
const TOKEN_PATH_RE = /^[a-zA-Z][a-zA-Z0-9]*(?:[._-][a-zA-Z0-9]+)*$/;
const CONTRACT_TOKEN_NAMESPACES = new Set(["global", "semantic", "component", "state", "motion", "density", "extension"]);

export class ThemeController {
    private readonly bus: ThemeBus;
    private readonly documentRef: Document;
    private readonly backgroundSelector: string;
    private readonly previousProperties = new Map<string, string>();
    private previousBackgroundImage: string | null = null;
    private installed = false;

    constructor(bus: ThemeBus, documentRef: Document = document, backgroundSelector = ".shell-background") {
        this.bus = bus;
        this.documentRef = documentRef;
        this.backgroundSelector = backgroundSelector;
    }

    install(): void {
        if (this.installed) {
            return;
        }
        this.bus.subscribe<ThemeApplyPayload>("shell.apply_theme", (event) => this.applyFromEvent(event));
        this.bus.subscribe("shell.reset_theme", () => this.resetTheme());
        this.installed = true;
    }

    applyFromEvent(event: BusEnvelope<ThemeApplyPayload>): ThemeAppliedSummary {
        return this.applyTheme(event.body);
    }

    applyTheme(payload: unknown): ThemeAppliedSummary {
        const summary: ThemeAppliedSummary = { properties: {}, skipped: 0 };
        if (!payload || typeof payload !== "object") {
            summary.skipped++;
            this.publishApplied(summary);
            return summary;
        }

        const body = payload as ThemeApplyPayload;
        summary.skipped += this.applyProperties(body.properties, summary.properties);

        if (typeof body.wallpaper_url === "string" && body.wallpaper_url.trim() !== "") {
            if (this.applyWallpaper(body.wallpaper_url)) {
                summary.wallpaper_url = body.wallpaper_url;
            } else {
                summary.skipped++;
            }
        } else if (body.wallpaper_url !== undefined) {
            summary.skipped++;
        }

        if (typeof body.css_url === "string" && body.css_url.trim() !== "") {
            this.appendThemeStylesheet(body.css_url);
            summary.css_url = body.css_url;
        } else if (body.css_url !== undefined) {
            summary.skipped++;
        }

        this.publishApplied(summary);
        return summary;
    }

    resetTheme(): ThemeAppliedSummary {
        const rootStyle = this.documentRef.documentElement.style;
        for (const [property, previousValue] of this.previousProperties.entries()) {
            if (previousValue) {
                rootStyle.setProperty(property, previousValue);
            } else {
                rootStyle.removeProperty(property);
            }
        }
        this.previousProperties.clear();

        const background = this.findBackgroundLayer();
        if (background && this.previousBackgroundImage !== null) {
            background.style.backgroundImage = this.previousBackgroundImage;
            this.previousBackgroundImage = null;
        }

        for (const link of this.findThemeLinks()) {
            link.remove();
        }

        const summary: ThemeAppliedSummary = { properties: {}, reset: true, skipped: 0 };
        this.publishApplied(summary);
        return summary;
    }

    private applyProperties(rawProperties: unknown, applied: Record<string, string>): number {
        if (rawProperties === undefined) {
            return 0;
        }
        if (!rawProperties || typeof rawProperties !== "object" || Array.isArray(rawProperties)) {
            return 1;
        }

        let skipped = 0;
        const rootStyle = this.documentRef.documentElement.style;
        for (const [rawKey, rawValue] of Object.entries(rawProperties as Record<string, unknown>)) {
            const propertyName = normalizeCustomPropertyName(rawKey);
            if (!propertyName || !isThemeValue(rawValue)) {
                skipped++;
                continue;
            }
            if (!this.previousProperties.has(propertyName)) {
                this.previousProperties.set(propertyName, rootStyle.getPropertyValue(propertyName));
            }
            const value = String(rawValue);
            rootStyle.setProperty(propertyName, value);
            applied[propertyName] = value;
        }
        return skipped;
    }

    private applyWallpaper(wallpaperURL: string): boolean {
        const background = this.findBackgroundLayer();
        if (!background) {
            return false;
        }
        if (this.previousBackgroundImage === null) {
            this.previousBackgroundImage = background.style.backgroundImage;
        }
        background.style.backgroundImage = `url("${escapeCSSURL(wallpaperURL)}")`;
        return true;
    }

    private appendThemeStylesheet(cssURL: string): void {
        const link = this.documentRef.createElement("link");
        link.setAttribute("rel", "stylesheet");
        link.setAttribute("href", cssURL);
        link.setAttribute(THEME_LINK_ATTRIBUTE, "true");
        this.documentRef.head.appendChild(link);
    }

    private findBackgroundLayer(): HTMLElement | null {
        return this.documentRef.querySelector<HTMLElement>(this.backgroundSelector);
    }

    private findThemeLinks(): HTMLElement[] {
        return Array.from(this.documentRef.querySelectorAll<HTMLElement>(`link[${THEME_LINK_ATTRIBUTE}="true"]`));
    }

    private publishApplied(summary: ThemeAppliedSummary): void {
        this.bus.publish("shell.theme_applied", summary);
    }
}

export function createThemeController(bus: ThemeBus, documentRef: Document = document): ThemeController {
    const controller = new ThemeController(bus, documentRef);
    controller.install();
    return controller;
}

function normalizeCustomPropertyName(rawKey: string): string | null {
    const trimmed = rawKey.trim();
    if (CUSTOM_PROPERTY_RE.test(trimmed)) {
        return trimmed.startsWith("--") ? trimmed : `--${trimmed}`;
    }
    // Theme manifests send contract token paths such as
    // "component.taskbar.background". Map supported namespaces to the canonical
    // CSS token namespace used in styles.css, while accepting a leading
    // "agora." prefix as compatibility input only.
    if (TOKEN_PATH_RE.test(trimmed) && trimmed.includes(".")) {
        const normalized = trimmed.startsWith("agora.") ? trimmed.slice("agora.".length) : trimmed;
        const namespace = normalized.split(/[._-]/, 1)[0];
        if (!CONTRACT_TOKEN_NAMESPACES.has(namespace)) {
            return null;
        }
        return `--agora-${normalized.replace(/[._]+/g, "-")}`;
    }
    return null;
}

function isThemeValue(value: unknown): value is string | number {
    return (typeof value === "string" && value.trim() !== "") || typeof value === "number";
}

function escapeCSSURL(value: string): string {
    return value.replace(/\\/g, "\\\\").replace(/"/g, "\\\"");
}
