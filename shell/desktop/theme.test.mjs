import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createThemeController } from "../dist/desktop/theme.js";

class FakeStyle {
  constructor() {
    this.values = new Map();
    this.backgroundImage = "";
  }

  setProperty(name, value) {
    this.values.set(name, String(value));
  }

  getPropertyValue(name) {
    return this.values.get(name) ?? "";
  }

  removeProperty(name) {
    this.values.delete(name);
  }
}

class FakeElement {
  constructor(tagName = "div") {
    this.tagName = tagName.toUpperCase();
    this.style = new FakeStyle();
    this.attributes = new Map();
    this.children = [];
    this.parent = null;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
    this[name] = String(value);
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }

  appendChild(child) {
    child.parent = this;
    this.children.push(child);
    return child;
  }

  remove() {
    if (!this.parent) {
      return;
    }
    this.parent.children = this.parent.children.filter((child) => child !== this);
    this.parent = null;
  }
}

class FakeDocument {
  constructor() {
    this.documentElement = new FakeElement("html");
    this.head = new FakeElement("head");
    this.background = new FakeElement("section");
  }

  createElement(tagName) {
    return new FakeElement(tagName);
  }

  querySelector(selector) {
    return selector === ".shell-background" ? this.background : null;
  }

  querySelectorAll(selector) {
    if (selector === 'link[data-agora-theme-link="true"]') {
      return this.head.children.filter((child) => child.tagName === "LINK" && child.getAttribute("data-agora-theme-link") === "true");
    }
    return [];
  }
}

class FakeBus {
  constructor() {
    this.handlers = new Map();
    this.published = [];
  }

  subscribe(topic, handler) {
    const handlers = this.handlers.get(topic) ?? [];
    handlers.push(handler);
    this.handlers.set(topic, handlers);
  }

  publish(topic, body) {
    this.published.push({ topic, body });
  }

  emit(topic, body) {
    for (const handler of this.handlers.get(topic) ?? []) {
      handler({ topic, body, timestamp: "test" });
    }
  }
}

const documentRef = new FakeDocument();
const bus = new FakeBus();
createThemeController(bus, documentRef);

assert.ok(bus.handlers.has("shell.apply_theme"), "controller subscribes to shell.apply_theme");
assert.ok(bus.handlers.has("shell.reset_theme"), "controller subscribes to shell.reset_theme");

documentRef.documentElement.style.setProperty("--shell-accent", "#old");
documentRef.background.style.backgroundImage = "linear-gradient(#000, #111)";

bus.emit("shell.apply_theme", {
  properties: {
    "taskbar-bg": "#222222",
    "--clock-color": "#88ccff",
    "shell-accent": 42,
    "component.taskbar.background": "#101827",
    "semantic.color.text.primary": "#f0f7ff",
    "agora.component.taskbar.launcher.background": "#76e4f7",
    "unknown.namespace.value": "ignored",
    "bad property": "ignored",
    "empty-value": "",
  },
  wallpaper_url: "/shell/user/wallpaper.png",
  css_url: "/api/shell/theme.css",
  unknown_field: { ignored: true },
});

assert.equal(documentRef.documentElement.style.getPropertyValue("--taskbar-bg"), "#222222");
assert.equal(documentRef.documentElement.style.getPropertyValue("--clock-color"), "#88ccff");
assert.equal(documentRef.documentElement.style.getPropertyValue("--shell-accent"), "#old");
assert.equal(documentRef.documentElement.style.getPropertyValue("--agora-component-taskbar-background"), "#101827");
assert.equal(documentRef.documentElement.style.getPropertyValue("--agora-semantic-color-text-primary"), "#f0f7ff");
assert.equal(documentRef.documentElement.style.getPropertyValue("--agora-component-taskbar-launcher-background"), "#76e4f7");
assert.equal(documentRef.documentElement.style.getPropertyValue("--agora-unknown-namespace-value"), "");
assert.equal(documentRef.documentElement.style.getPropertyValue("--bad property"), "");
assert.equal(documentRef.documentElement.style.getPropertyValue("--empty-value"), "");
assert.equal(documentRef.background.style.backgroundImage, 'url("/shell/user/wallpaper.png")');
assert.equal(documentRef.head.children.length, 1);
assert.equal(documentRef.head.children[0].getAttribute("rel"), "stylesheet");
assert.equal(documentRef.head.children[0].getAttribute("href"), "/api/shell/theme.css");

const applied = bus.published.at(-1);
assert.equal(applied.topic, "shell.theme_applied");
assert.deepEqual(applied.body.properties, {
  "--taskbar-bg": "#222222",
  "--clock-color": "#88ccff",
  "--agora-component-taskbar-background": "#101827",
  "--agora-semantic-color-text-primary": "#f0f7ff",
  "--agora-component-taskbar-launcher-background": "#76e4f7",
});
assert.equal(applied.body.wallpaper_url, "/shell/user/wallpaper.png");
assert.equal(applied.body.css_url, "/api/shell/theme.css");
assert.equal(applied.body.skipped, 4);
assert.deepEqual(applied.body.skipped_entries.map((entry) => entry.reason), [
  "invalid_color_value",
  "invalid_token_name",
  "invalid_token_name",
  "unknown_or_reserved_token",
]);

bus.emit("shell.reset_theme", {});
assert.equal(documentRef.documentElement.style.getPropertyValue("--taskbar-bg"), "");
assert.equal(documentRef.documentElement.style.getPropertyValue("--clock-color"), "");
assert.equal(documentRef.documentElement.style.getPropertyValue("--shell-accent"), "#old");
assert.equal(documentRef.background.style.backgroundImage, "linear-gradient(#000, #111)");
assert.equal(documentRef.head.children.length, 0);
const reset = bus.published.at(-1);
assert.equal(reset.topic, "shell.theme_applied");
assert.equal(reset.body.reset, true);

bus.emit("shell.apply_theme", { properties: ["bad"], wallpaper_url: 7, css_url: null });
const malformed = bus.published.at(-1);
assert.equal(malformed.topic, "shell.theme_applied");
assert.equal(malformed.body.skipped, 3);

const manifestDocument = new FakeDocument();
const manifestBus = new FakeBus();
const manifestController = createThemeController(manifestBus, manifestDocument);
const manifestSummary = manifestController.applyManifest({
  schema: "agora-desktop-shell-theme/v0.1",
  id: "agora-default",
  tokens: {
    "component.taskbar.background": "rgba(10, 14, 24, 0.88)",
    "component.taskbar.position": "fixed",
    "extension.agent.experimental": "#fff",
  },
  assets: {
    wallpaper: {
      kind: "css-gradient",
      value: "linear-gradient(135deg, #05070d, #080b12)",
    },
  },
  overrides: {
    css_path: "theme.css",
    css_mode: "safe-visual-only",
  },
}, "config");
assert.equal(manifestSummary.theme_id, "agora-default");
assert.equal(manifestDocument.documentElement.style.getPropertyValue("--agora-component-taskbar-background"), "rgba(10, 14, 24, 0.88)");
assert.equal(manifestDocument.documentElement.style.getPropertyValue("--agora-component-taskbar-position"), "");
assert.equal(manifestDocument.documentElement.style.getPropertyValue("--agora-extension-agent-experimental"), "");
assert.equal(manifestDocument.background.style.backgroundImage, "linear-gradient(135deg, #05070d, #080b12)");
assert.equal(manifestDocument.head.children[0].getAttribute("href"), "/api/shell/theme/agora-default/theme.css");
assert.equal(manifestSummary.skipped, 2);
assert.deepEqual(manifestSummary.skipped_entries.map((entry) => entry.reason), ["unknown_or_reserved_token", "extension_token_disabled"]);

const defaultManifest = JSON.parse(await readFile(new URL("./themes/agora-default/theme.json", import.meta.url), "utf8"));
const defaultDocument = new FakeDocument();
const defaultBus = new FakeBus();
const defaultController = createThemeController(defaultBus, defaultDocument);
const defaultSummary = defaultController.applyManifest(defaultManifest, "config");
assert.equal(defaultSummary.theme_id, "agora-default");
assert.equal(defaultManifest.name, "Agora Observatory");
assert.equal(defaultSummary.skipped, 0);
assert.equal(defaultManifest.tokens["semantic.color.accent.primary"], "#6AD7D2");
assert.equal(defaultManifest.tokens["semantic.color.accent.secondary"], "#D8A657");
assert.equal(defaultManifest.tokens["component.command_center.panel.border"], "rgba(124, 227, 220, 0.32)");
assert.equal(defaultManifest.tokens["global.shadow.overlay"].includes("rgba(106, 215, 210"), true);
assert.equal(JSON.stringify(defaultManifest.tokens).includes("#F472B6"), false);
assert.equal(JSON.stringify(defaultManifest.tokens).includes("244, 114, 182"), false);
assert.equal(
  defaultDocument.documentElement.style.getPropertyValue("--agora-component-command-center-panel-background"),
  defaultManifest.tokens["component.command_center.panel.background"],
);
assert.equal(defaultDocument.documentElement.style.getPropertyValue("--agora-semantic-color-accent-secondary"), "#D8A657");
assert.equal(defaultDocument.documentElement.style.getPropertyValue("--agora-state-danger-control-background"), "rgba(255, 107, 95, 0.16)");
assert.equal(defaultDocument.background.style.backgroundImage, defaultManifest.assets.wallpaper.value);

const unsafeGradientDocument = new FakeDocument();
const unsafeGradientBus = new FakeBus();
const unsafeGradientController = createThemeController(unsafeGradientBus, unsafeGradientDocument);
const unsafeGradientSummary = unsafeGradientController.applyManifest({
  schema: "agora-desktop-shell-theme/v0.1",
  id: "unsafe-gradient",
  tokens: {
    "component.command_center.panel.background": "linear-gradient(90deg, url(https://example.invalid/a), #fff)",
    "semantic.color.background.canvas": "linear-gradient(90deg, javascript:alert(1), #fff)",
    "semantic.color.background.panel": "linear-gradient(90deg, #fff\\0, #000)",
  },
  assets: {
    wallpaper: {
      kind: "css-gradient",
      value: "linear-gradient(90deg, url(https://example.invalid/a), #fff)",
    },
  },
}, "config");
assert.equal(unsafeGradientSummary.skipped, 4);
assert.deepEqual(unsafeGradientSummary.skipped_entries.map((entry) => entry.reason), [
  "unsafe_value",
  "unsafe_value",
  "unsafe_value",
  "unsupported_wallpaper",
]);

let fetchedURL = "";
globalThis.fetch = async (url) => {
  fetchedURL = String(url);
  return {
    ok: true,
    async json() {
      return {
        schema: "agora-desktop-shell-theme/v0.1",
        id: "runtime-calm",
        tokens: { "semantic.color.text.primary": "#f8fafc" },
      };
    },
  };
};
const loadDocument = new FakeDocument();
const loadBus = new FakeBus();
const loadController = createThemeController(loadBus, loadDocument);
await loadController.loadFromServer();
assert.equal(fetchedURL, "/api/shell/theme.json");
assert.equal(loadDocument.documentElement.style.getPropertyValue("--agora-semantic-color-text-primary"), "#f8fafc");

console.log("theme handler DOM tests passed");
