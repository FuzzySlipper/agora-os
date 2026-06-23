import type { BusEnvelope } from "../shared/types.js";

export interface ThemeBus {
    subscribe<TBody = unknown>(topic: string, handler: (envelope: BusEnvelope<TBody>) => void): void;
    publish<TBody = unknown>(topic: string, body: TBody): void;
}

export interface ThemeWallpaper {
    kind?: unknown;
    value?: unknown;
}

export interface ThemeManifest {
    schema?: unknown;
    id?: unknown;
    name?: unknown;
    description?: unknown;
    version?: unknown;
    author?: unknown;
    extends?: unknown;
    metadata?: unknown;
    allow_extensions?: unknown;
    tokens?: unknown;
    assets?: unknown;
    overrides?: unknown;
    visual_contracts?: unknown;
}

export interface ThemeApplyPayload {
    theme_id?: unknown;
    source?: unknown;
    properties?: unknown;
    tokens?: unknown;
    wallpaper?: unknown;
    wallpaper_url?: unknown;
    css_url?: unknown;
    persist?: unknown;
    allow_extensions?: unknown;
}

export interface ThemeSkippedEntry {
    key: string;
    reason: string;
}

export interface ThemeAppliedSummary {
    theme_id?: string;
    source?: string;
    properties: Record<string, string>;
    applied: Record<string, string>;
    css_properties: Record<string, string>;
    wallpaper_url?: string;
    css_url?: string;
    wallpaper?: { kind: string; value?: string };
    reset?: boolean;
    skipped: number;
    skipped_entries: ThemeSkippedEntry[];
    warnings: string[];
}

const THEME_LINK_ATTRIBUTE = "data-agora-theme-link";
const THEME_MANIFEST_SCHEMA = "agora-desktop-shell-theme/v0.1";
const THEME_ID_RE = /^[a-z0-9][a-z0-9_-]{0,63}$/;
const CUSTOM_PROPERTY_RE = /^(?:--)?[a-zA-Z][a-zA-Z0-9_-]*$/;
const TOKEN_PATH_RE = /^[a-zA-Z][a-zA-Z0-9]*(?:[._-][a-zA-Z0-9]+)*$/;
const CONTRACT_TOKEN_NAMESPACES = new Set(["global", "semantic", "component", "state", "motion", "density", "extension"]);
const KNOWN_TOKEN_PROPERTIES = new Set<string>([
    "--agora-component-agent-health-chip-background",
    "--agora-component-agent-health-chip-border",
    "--agora-component-agent-health-dot-size",
    "--agora-component-background-wallpaper",
    "--agora-component-clock-color",
    "--agora-component-command-center-control-background",
    "--agora-component-command-center-input-background",
    "--agora-component-command-center-panel-background",
    "--agora-component-command-center-panel-border",
    "--agora-component-command-center-scrim-background",
    "--agora-component-command-center-suggestion-background",
    "--agora-component-command-center-suggestion-border",
    "--agora-component-command-center-transcript-border",
    "--agora-component-notification-background",
    "--agora-component-notification-danger-border",
    "--agora-component-notification-info-border",
    "--agora-component-notification-radius",
    "--agora-component-notification-shadow",
    "--agora-component-notification-success-border",
    "--agora-component-notification-warning-border",
    "--agora-component-surface-button-background",
    "--agora-component-surface-button-border",
    "--agora-component-surface-button-minimized-opacity",
    "--agora-component-taskbar-background",
    "--agora-component-taskbar-border",
    "--agora-component-taskbar-height",
    "--agora-component-taskbar-launcher-background",
    "--agora-component-taskbar-launcher-color",
    "--agora-component-taskbar-radius",
    "--agora-component-taskbar-shadow",
    "--agora-component-widget-container-background",
    "--agora-component-widget-container-border",
    "--agora-component-widget-container-radius",
    "--agora-component-widget-container-shadow",
    "--agora-component-window-chrome-button-active-background",
    "--agora-component-window-chrome-button-background",
    "--agora-component-window-chrome-button-border",
    "--agora-component-window-chrome-frame-background",
    "--agora-component-window-chrome-surface-background",
    "--agora-component-window-chrome-surface-border",
    "--agora-component-window-chrome-surface-pending-border",
    "--agora-density-control-height-lg",
    "--agora-density-control-height-md",
    "--agora-density-control-height-sm",
    "--agora-density-panel-padding",
    "--agora-density-row-gap",
    "--agora-density-shell-gap",
    "--agora-density-taskbar-height",
    "--agora-global-blur-overlay",
    "--agora-global-blur-panel",
    "--agora-global-blur-scrim",
    "--agora-global-color-cyan-300",
    "--agora-global-color-danger-200",
    "--agora-global-color-danger-400",
    "--agora-global-color-neutral-0",
    "--agora-global-color-neutral-100",
    "--agora-global-color-neutral-400",
    "--agora-global-color-neutral-50",
    "--agora-global-color-neutral-500",
    "--agora-global-color-neutral-700",
    "--agora-global-color-neutral-800",
    "--agora-global-color-neutral-900",
    "--agora-global-color-sky-300",
    "--agora-global-color-success-400",
    "--agora-global-color-violet-300",
    "--agora-global-color-warning-400",
    "--agora-global-font-family-sans",
    "--agora-global-font-size-lg",
    "--agora-global-font-size-md",
    "--agora-global-font-size-sm",
    "--agora-global-font-size-xs",
    "--agora-global-font-weight-label",
    "--agora-global-font-weight-strong",
    "--agora-global-radius-control-lg",
    "--agora-global-radius-control-md",
    "--agora-global-radius-control-sm",
    "--agora-global-radius-overlay",
    "--agora-global-radius-panel",
    "--agora-global-radius-panel-lg",
    "--agora-global-radius-panel-sm",
    "--agora-global-radius-pill",
    "--agora-global-shadow-notification",
    "--agora-global-shadow-overlay",
    "--agora-global-shadow-panel",
    "--agora-global-shadow-widget",
    "--agora-global-space-1",
    "--agora-global-space-10",
    "--agora-global-space-12",
    "--agora-global-space-2",
    "--agora-global-space-3",
    "--agora-global-space-4",
    "--agora-global-space-5",
    "--agora-global-space-6",
    "--agora-global-space-7",
    "--agora-global-space-8",
    "--agora-motion-duration-fast",
    "--agora-motion-duration-normal",
    "--agora-motion-easing-standard",
    "--agora-semantic-backdrop-overlay",
    "--agora-semantic-backdrop-panel",
    "--agora-semantic-backdrop-scrim",
    "--agora-semantic-color-accent-primary",
    "--agora-semantic-color-accent-secondary",
    "--agora-semantic-color-background-canvas",
    "--agora-semantic-color-background-canvas-deep",
    "--agora-semantic-color-background-canvas-low",
    "--agora-semantic-color-background-panel",
    "--agora-semantic-color-background-panel-muted",
    "--agora-semantic-color-background-panel-overlay",
    "--agora-semantic-color-border-muted",
    "--agora-semantic-color-border-subtle",
    "--agora-semantic-color-danger",
    "--agora-semantic-color-danger-text",
    "--agora-semantic-color-success",
    "--agora-semantic-color-text-on-accent",
    "--agora-semantic-color-text-primary",
    "--agora-semantic-color-text-secondary",
    "--agora-semantic-color-warning",
    "--agora-semantic-elevation-overlay",
    "--agora-semantic-elevation-panel",
    "--agora-semantic-radius-control",
    "--agora-semantic-radius-panel",
    "--agora-state-accent-control-background",
    "--agora-state-accent-glow",
    "--agora-state-danger-control-background",
    "--agora-state-danger-glow",
    "--agora-state-disabled-cursor",
    "--agora-state-disabled-opacity",
    "--agora-state-error-background",
    "--agora-state-error-border",
    "--agora-state-error-color",
    "--agora-state-focus-border",
    "--agora-state-focus-ring",
    "--agora-state-hover-background",
    "--agora-state-hover-border",
    "--agora-state-pending-opacity",
    "--agora-state-success-color",
    "--agora-state-success-glow",
    "--agora-state-warning-color",
    "--agora-state-warning-glow"
]);
const COMPAT_CUSTOM_PROPERTIES = new Set<string>([
    "--shell-bg",
    "--shell-text",
    "--shell-muted",
    "--shell-accent",
    "--shell-border",
    "--taskbar-bg",
    "--taskbar-height",
    "--clock-color",
    "--notif-bg",
    "--health-dot-size",
]);

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

    async loadFromServer(url = "/api/shell/theme.json"): Promise<ThemeAppliedSummary | null> {
        if (typeof fetch !== "function") {
            return null;
        }
        try {
            const response = await fetch(url, { cache: "no-store" });
            if (!response.ok) {
                return null;
            }
            const manifest = await response.json() as ThemeManifest;
            return this.applyManifest(manifest, "config");
        } catch {
            return null;
        }
    }

    applyFromEvent(event: BusEnvelope<ThemeApplyPayload>): ThemeAppliedSummary {
        return this.applyTheme(event.body);
    }

    applyManifest(manifest: unknown, source = "manifest"): ThemeAppliedSummary {
        const summary = emptySummary(source);
        if (!manifest || typeof manifest !== "object" || Array.isArray(manifest)) {
            addSkipped(summary, "manifest", "manifest_not_object");
            this.publishApplied(summary);
            return summary;
        }
        const body = manifest as ThemeManifest;
        const themeID = typeof body.id === "string" ? body.id.trim() : "";
        if (body.schema !== THEME_MANIFEST_SCHEMA) {
            addSkipped(summary, "schema", "unsupported_schema");
        }
        if (!THEME_ID_RE.test(themeID)) {
            addSkipped(summary, "id", "invalid_theme_id");
        } else {
            summary.theme_id = themeID;
        }
        const allowExtensions = body.allow_extensions === true;
        const tokens = body.tokens;
        this.applyProperties(tokens, summary.properties, summary, { allowExtensions, manifestTokens: true });
        const assets = body.assets && typeof body.assets === "object" ? body.assets as { wallpaper?: unknown } : undefined;
        if (assets?.wallpaper !== undefined) {
            this.applyWallpaperValue(assets.wallpaper, summary, themeID || undefined);
        }
        const overrides = body.overrides && typeof body.overrides === "object" ? body.overrides as { css_path?: unknown; css_mode?: unknown } : undefined;
        if (overrides?.css_path !== undefined) {
            if (typeof overrides.css_path === "string" && safeRelativeAssetPath(overrides.css_path) && (overrides.css_mode === undefined || overrides.css_mode === "safe-visual-only") && themeID) {
                const cssURL = `/api/shell/theme/${encodeURIComponent(themeID)}/${overrides.css_path}`;
                this.appendThemeStylesheet(cssURL);
                summary.css_url = cssURL;
            } else {
                addSkipped(summary, "overrides.css_path", "invalid_or_unsafe_css_path");
            }
        }
        this.publishApplied(summary);
        return summary;
    }

    applyTheme(payload: unknown): ThemeAppliedSummary {
        const summary = emptySummary("session");
        if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
            addSkipped(summary, "payload", "payload_not_object");
            this.publishApplied(summary);
            return summary;
        }

        const body = payload as ThemeApplyPayload;
        if (typeof body.theme_id === "string" && THEME_ID_RE.test(body.theme_id.trim())) {
            summary.theme_id = body.theme_id.trim();
        } else if (body.theme_id !== undefined) {
            addSkipped(summary, "theme_id", "invalid_theme_id");
        }
        if (typeof body.source === "string" && body.source.trim() !== "") {
            summary.source = body.source.trim();
        }
        if (body.persist === true) {
            summary.warnings.push("persist=true ignored: persistent theme save/select is not implemented in v0.1");
        }
        const allowExtensions = body.allow_extensions === true;
        this.applyProperties(body.properties, summary.properties, summary, { allowExtensions, manifestTokens: false });
        this.applyProperties(body.tokens, summary.properties, summary, { allowExtensions, manifestTokens: true });

        if (body.wallpaper !== undefined) {
            this.applyWallpaperValue(body.wallpaper, summary, summary.theme_id);
        }
        if (typeof body.wallpaper_url === "string" && body.wallpaper_url.trim() !== "") {
            if (safeShellURL(body.wallpaper_url) && this.applyWallpaperURL(body.wallpaper_url)) {
                summary.wallpaper_url = body.wallpaper_url;
                summary.wallpaper = { kind: "image", value: body.wallpaper_url };
            } else {
                addSkipped(summary, "wallpaper_url", "invalid_or_unsafe_url");
            }
        } else if (body.wallpaper_url !== undefined) {
            addSkipped(summary, "wallpaper_url", "invalid_value");
        }

        if (typeof body.css_url === "string" && body.css_url.trim() !== "") {
            if (safeThemeCSSURL(body.css_url)) {
                this.appendThemeStylesheet(body.css_url);
                summary.css_url = body.css_url;
            } else {
                addSkipped(summary, "css_url", "invalid_or_unsafe_url");
            }
        } else if (body.css_url !== undefined) {
            addSkipped(summary, "css_url", "invalid_value");
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

        const summary = emptySummary("session");
        summary.reset = true;
        this.publishApplied(summary);
        return summary;
    }

    private applyProperties(rawProperties: unknown, applied: Record<string, string>, summary: ThemeAppliedSummary, options: { allowExtensions: boolean; manifestTokens: boolean }): number {
        if (rawProperties === undefined) {
            return 0;
        }
        if (!rawProperties || typeof rawProperties !== "object" || Array.isArray(rawProperties)) {
            addSkipped(summary, options.manifestTokens ? "tokens" : "properties", "not_object");
            return 0;
        }

        const rootStyle = this.documentRef.documentElement.style;
        for (const [rawKey, rawValue] of Object.entries(rawProperties as Record<string, unknown>)) {
            const normalized = normalizeThemeProperty(rawKey, { allowExtensions: options.allowExtensions, manifestTokens: options.manifestTokens });
            if (normalized.ok === false) {
                addSkipped(summary, rawKey, normalized.reason);
                continue;
            }
            const valueResult = normalizeThemeValue(normalized.propertyName, rawValue);
            if (valueResult.ok === false) {
                addSkipped(summary, rawKey, valueResult.reason);
                continue;
            }
            if (!this.previousProperties.has(normalized.propertyName)) {
                this.previousProperties.set(normalized.propertyName, rootStyle.getPropertyValue(normalized.propertyName));
            }
            rootStyle.setProperty(normalized.propertyName, valueResult.value);
            applied[normalized.propertyName] = valueResult.value;
            summary.applied[rawKey] = valueResult.value;
            summary.css_properties[normalized.propertyName] = valueResult.value;
        }
        return 0;
    }

    private applyWallpaperValue(rawWallpaper: unknown, summary: ThemeAppliedSummary, themeID?: string): void {
        if (!rawWallpaper || typeof rawWallpaper !== "object" || Array.isArray(rawWallpaper)) {
            addSkipped(summary, "wallpaper", "wallpaper_not_object");
            return;
        }
        const wallpaper = rawWallpaper as ThemeWallpaper;
        if (wallpaper.kind === "none") {
            if (this.applyWallpaperCSS("none")) {
                summary.wallpaper = { kind: "none" };
            } else {
                addSkipped(summary, "wallpaper", "background_layer_missing");
            }
            return;
        }
        if (wallpaper.kind === "css-gradient" && typeof wallpaper.value === "string" && safeGradient(wallpaper.value)) {
            if (this.applyWallpaperCSS(wallpaper.value)) {
                summary.wallpaper = { kind: "css-gradient" };
            } else {
                addSkipped(summary, "wallpaper", "background_layer_missing");
            }
            return;
        }
        if (wallpaper.kind === "image" && typeof wallpaper.value === "string") {
            const value = wallpaper.value.trim();
            const url = themeID && safeRelativeAssetPath(value) ? `/api/shell/theme/${encodeURIComponent(themeID)}/${value}` : value;
            if (safeShellURL(url) && this.applyWallpaperURL(url)) {
                summary.wallpaper_url = url;
                summary.wallpaper = { kind: "image", value: url };
            } else {
                addSkipped(summary, "wallpaper", "invalid_or_unsafe_image");
            }
            return;
        }
        addSkipped(summary, "wallpaper", "unsupported_wallpaper");
    }

    private applyWallpaperURL(wallpaperURL: string): boolean {
        return this.applyWallpaperCSS(`url("${escapeCSSURL(wallpaperURL)}")`);
    }

    private applyWallpaperCSS(backgroundImage: string): boolean {
        const background = this.findBackgroundLayer();
        if (!background) {
            return false;
        }
        if (this.previousBackgroundImage === null) {
            this.previousBackgroundImage = background.style.backgroundImage;
        }
        background.style.backgroundImage = backgroundImage;
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

function emptySummary(source: string): ThemeAppliedSummary {
    return { source, properties: {}, applied: {}, css_properties: {}, skipped: 0, skipped_entries: [], warnings: [] };
}

function addSkipped(summary: ThemeAppliedSummary, key: string, reason: string): void {
    summary.skipped_entries.push({ key, reason });
    summary.skipped = summary.skipped_entries.length;
}

function normalizeThemeProperty(rawKey: string, options: { allowExtensions: boolean; manifestTokens: boolean }): { ok: true; propertyName: string } | { ok: false; reason: string } {
    const trimmed = rawKey.trim();
    if (trimmed === "") {
        return { ok: false, reason: "empty_key" };
    }
    const propertyName = normalizeCustomPropertyName(trimmed);
    if (!propertyName) {
        return { ok: false, reason: "invalid_token_name" };
    }
    if (propertyName.startsWith("--agora-extension-") && !options.allowExtensions) {
        return { ok: false, reason: "extension_token_disabled" };
    }
    if (KNOWN_TOKEN_PROPERTIES.has(propertyName) || (options.allowExtensions && propertyName.startsWith("--agora-extension-"))) {
        return { ok: true, propertyName };
    }
    if (!options.manifestTokens && COMPAT_CUSTOM_PROPERTIES.has(propertyName)) {
        return { ok: true, propertyName };
    }
    return { ok: false, reason: "unknown_or_reserved_token" };
}

function normalizeCustomPropertyName(rawKey: string): string | null {
    const trimmed = rawKey.trim();
    if (CUSTOM_PROPERTY_RE.test(trimmed)) {
        return trimmed.startsWith("--") ? trimmed : `--${trimmed}`;
    }
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

function normalizeThemeValue(propertyName: string, value: unknown): { ok: true; value: string } | { ok: false; reason: string } {
    if (!((typeof value === "string" && value.trim() !== "") || typeof value === "number")) {
        return { ok: false, reason: "invalid_value" };
    }
    const normalized = String(value).trim();
    if (!safeCSSValue(normalized)) {
        return { ok: false, reason: "unsafe_value" };
    }
    if (propertyName.includes("color") || propertyName.includes("background") || COMPAT_CUSTOM_PROPERTIES.has(propertyName)) {
        return safeColorLikeValue(normalized) ? { ok: true, value: normalized } : { ok: false, reason: "invalid_color_value" };
    }
    if (propertyName.includes("radius") || propertyName.includes("space") || propertyName.includes("height") || propertyName.includes("size") || propertyName.includes("padding") || propertyName.includes("gap")) {
        return safeSizeValue(normalized) ? { ok: true, value: normalized } : { ok: false, reason: "invalid_size_value" };
    }
    if (propertyName.includes("shadow") || propertyName.includes("elevation") || propertyName.includes("glow") || propertyName.includes("ring")) {
        return normalized === "none" || !/url\s*\(/i.test(normalized) ? { ok: true, value: normalized } : { ok: false, reason: "invalid_shadow_value" };
    }
    if (propertyName.includes("blur") || propertyName.includes("backdrop")) {
        return safeFilterValue(normalized) ? { ok: true, value: normalized } : { ok: false, reason: "invalid_filter_value" };
    }
    if (propertyName.includes("duration")) {
        return /^\d+(?:\.\d+)?m?s$/.test(normalized) ? { ok: true, value: normalized } : { ok: false, reason: "invalid_motion_value" };
    }
    if (propertyName.includes("easing")) {
        return /^(ease|ease-in|ease-out|ease-in-out|linear|cubic-bezier\([0-9.,\s-]+\))$/.test(normalized) ? { ok: true, value: normalized } : { ok: false, reason: "invalid_easing_value" };
    }
    if (propertyName.includes("opacity")) {
        return /^(?:0(?:\.\d+)?|1(?:\.0+)?)$/.test(normalized) ? { ok: true, value: normalized } : { ok: false, reason: "invalid_opacity_value" };
    }
    if (propertyName.includes("cursor")) {
        return /^(default|pointer|progress|not-allowed|wait)$/.test(normalized) ? { ok: true, value: normalized } : { ok: false, reason: "invalid_cursor_value" };
    }
    return { ok: true, value: normalized };
}

function safeCSSValue(value: string): boolean {
    return !/[\\\u0000-\u001f\u007f]/.test(value) && !/(?:expression\s*\(|javascript:|data:text\/html|<\/style|url\s*\()/i.test(value);
}

function safeColorLikeValue(value: string): boolean {
    return /^(#[0-9a-fA-F]{3,8}|rgba?\([0-9.,%\s]+\)|hsla?\([0-9.,%\s]+\)|transparent|currentColor|var\(--agora-[A-Za-z0-9_-]+\))$/.test(value) || safeGradient(value);
}

function safeSizeValue(value: string): boolean {
    return /^(-?\d+(?:\.\d+)?(?:px|rem|em|%|vh|vw)|0|var\(--agora-[A-Za-z0-9_-]+\)|(clamp|min|max|calc)\([A-Za-z0-9_+\-*/.,%\s()]+\))$/.test(value);
}

function safeFilterValue(value: string): boolean {
    return /^none$|^(?:(?:blur|brightness|contrast|saturate|opacity)\([0-9.a-zA-Z%\s]+\)\s*)+$/.test(value);
}

function safeGradient(value: string): boolean {
    if (!safeCSSValue(value)) {
        return false;
    }
    const layers = splitTopLevelCommaList(value);
    return layers.length > 0 && layers.every((layer) => safeSingleGradient(layer.trim()));
}

function splitTopLevelCommaList(value: string): string[] {
    const layers: string[] = [];
    let depth = 0;
    let start = 0;
    for (let i = 0; i < value.length; i += 1) {
        const ch = value[i];
        if (ch === "(") {
            depth += 1;
        } else if (ch === ")") {
            depth -= 1;
            if (depth < 0) {
                return [];
            }
        } else if (ch === "," && depth === 0) {
            layers.push(value.slice(start, i));
            start = i + 1;
        }
    }
    if (depth !== 0) {
        return [];
    }
    layers.push(value.slice(start));
    return layers.filter((layer) => layer.trim() !== "");
}

function safeSingleGradient(layer: string): boolean {
    const match = /^(linear-gradient|radial-gradient)\((.*)\)$/s.exec(layer);
    if (!match) {
        return false;
    }
    const body = match[2].trim();
    if (body === "") {
        return false;
    }
    return balancedParentheses(body) && gradientBodyCharactersAreSafe(body);
}

function balancedParentheses(value: string): boolean {
    let depth = 0;
    for (const ch of value) {
        if (ch === "(") {
            depth += 1;
        } else if (ch === ")") {
            depth -= 1;
            if (depth < 0) {
                return false;
            }
        }
    }
    return depth === 0;
}

function gradientBodyCharactersAreSafe(value: string): boolean {
    return /^[#%.,\w\s()+-]+$/.test(value);
}

function safeShellURL(rawURL: string): boolean {
    const trimmed = rawURL.trim();
    return trimmed.startsWith("/api/shell/theme/") || trimmed.startsWith("/api/shell/theme.css") || trimmed.startsWith("/shell/user/");
}

function safeThemeCSSURL(rawURL: string): boolean {
    const trimmed = rawURL.trim();
    return trimmed === "/api/shell/theme.css" || trimmed.startsWith("/api/shell/theme/");
}

function safeRelativeAssetPath(value: string): boolean {
    const trimmed = value.trim();
    return trimmed !== "" && !trimmed.startsWith("/") && !trimmed.includes(":") && !trimmed.includes("\\") && !trimmed.split("/").some((part) => part === "" || part === "." || part === "..");
}

function escapeCSSURL(value: string): string {
    return value.replace(/\\/g, "\\\\").replace(/"/g, "\\\"");
}
