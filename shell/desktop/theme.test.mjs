import assert from "node:assert/strict";
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
  css_url: "/shell/user/theme.css",
  unknown_field: { ignored: true },
});

assert.equal(documentRef.documentElement.style.getPropertyValue("--taskbar-bg"), "#222222");
assert.equal(documentRef.documentElement.style.getPropertyValue("--clock-color"), "#88ccff");
assert.equal(documentRef.documentElement.style.getPropertyValue("--shell-accent"), "42");
assert.equal(documentRef.documentElement.style.getPropertyValue("--agora-component-taskbar-background"), "#101827");
assert.equal(documentRef.documentElement.style.getPropertyValue("--agora-semantic-color-text-primary"), "#f0f7ff");
assert.equal(documentRef.documentElement.style.getPropertyValue("--agora-component-taskbar-launcher-background"), "#76e4f7");
assert.equal(documentRef.documentElement.style.getPropertyValue("--agora-unknown-namespace-value"), "");
assert.equal(documentRef.documentElement.style.getPropertyValue("--bad property"), "");
assert.equal(documentRef.documentElement.style.getPropertyValue("--empty-value"), "");
assert.equal(documentRef.background.style.backgroundImage, 'url("/shell/user/wallpaper.png")');
assert.equal(documentRef.head.children.length, 1);
assert.equal(documentRef.head.children[0].getAttribute("rel"), "stylesheet");
assert.equal(documentRef.head.children[0].getAttribute("href"), "/shell/user/theme.css");

const applied = bus.published.at(-1);
assert.equal(applied.topic, "shell.theme_applied");
assert.deepEqual(applied.body.properties, {
  "--taskbar-bg": "#222222",
  "--clock-color": "#88ccff",
  "--shell-accent": "42",
  "--agora-component-taskbar-background": "#101827",
  "--agora-semantic-color-text-primary": "#f0f7ff",
  "--agora-component-taskbar-launcher-background": "#76e4f7",
});
assert.equal(applied.body.wallpaper_url, "/shell/user/wallpaper.png");
assert.equal(applied.body.css_url, "/shell/user/theme.css");
assert.equal(applied.body.skipped, 3);

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

console.log("theme handler DOM tests passed");
