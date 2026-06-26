import assert from "node:assert/strict";

class FakeClassList {
  constructor(initial = []) { this.values = new Set(initial); }
  add(...names) { for (const name of names) this.values.add(name); }
  remove(...names) { for (const name of names) this.values.delete(name); }
  contains(name) { return this.values.has(name); }
}

class FakeStyle {
  constructor() { this.values = new Map(); }
  setProperty(name, value) { this.values.set(name, String(value)); }
  getPropertyValue(name) { return this.values.get(name) ?? ""; }
}

class FakeElement {
  constructor(tagName = "div") {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.parent = null;
    this.dataset = {};
    this.attributes = new Map();
    this.handlers = new Map();
    this.classList = new FakeClassList();
    this.style = new FakeStyle();
    this._innerHTML = "";
  }
  set className(value) { this._className = String(value); this.classList = new FakeClassList(this._className.split(/\s+/).filter(Boolean)); }
  get className() { return this._className ?? ""; }
  set textContent(value) { this._textContent = String(value); }
  get textContent() { return [this._textContent ?? "", ...this.children.map((child) => child.textContent)].join(""); }
  set innerHTML(value) {
    this._innerHTML = String(value);
    this.children = [];
    const slotRe = /<([a-z0-9-]+)[^>]*data-widget-slot="([^"]+)"[^>]*>/gi;
    for (const match of this._innerHTML.matchAll(slotRe)) {
      const child = new FakeElement(match[1]);
      child.dataset.widgetSlot = match[2];
      child.parent = this;
      this.children.push(child);
    }
  }
  get innerHTML() { return this._innerHTML; }
  append(...children) { for (const child of children) { child.parent = this; this.children.push(child); } }
  replaceChildren(...children) { this.children = []; this.append(...children); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter((child) => child !== this); this.parent = null; }
  setAttribute(name, value) {
    this.attributes.set(name, String(value));
    if (name.startsWith("data-")) {
      const key = name.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());
      this.dataset[key] = String(value);
    }
  }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  addEventListener(type, handler) { this.handlers.set(type, [...(this.handlers.get(type) ?? []), handler]); }
  click() { for (const handler of this.handlers.get("click") ?? []) handler({ currentTarget: this, target: this, preventDefault() {} }); }
  focus() { this.focused = true; }
  querySelector(selector) { return this.querySelectorAll(selector)[0] ?? null; }
  querySelectorAll(selector) {
    const matches = [];
    const match = (node) => {
      if (selector.startsWith("[data-widget-slot=")) return node.dataset?.widgetSlot === selector.match(/\"([^\"]+)\"/)?.[1];
      if (selector === "button") return node.tagName === "BUTTON";
      if (selector === "[data-surface-count]") return node.dataset?.surfaceCount !== undefined;
      if (selector === "[data-agent-count]") return node.dataset?.agentCount !== undefined;
      return false;
    };
    const visit = (node) => {
      if (match(node)) matches.push(node);
      for (const child of node.children ?? []) visit(child);
    };
    visit(this);
    return matches;
  }
}

class FakeHTMLElement extends FakeElement { constructor() { super("custom"); } }
class FakeBus {
  constructor() { this.subscriptions = []; this.published = []; this.connected = false; }
  subscribe(topic, handler) { this.subscriptions.push({ topic, handler }); }
  publish(topic, body) { this.published.push({ topic, body }); }
  connect() { this.connected = true; }
  disconnect() { this.connected = false; }
}

const registry = new Map();
globalThis.HTMLElement = FakeHTMLElement;
globalThis.KeyboardEvent = class KeyboardEvent {};
globalThis.customElements = { get: (name) => registry.get(name), define: (name, ctor) => registry.set(name, ctor) };
globalThis.document = { documentElement: new FakeElement("html"), createElement: (tagName) => new FakeElement(tagName), getElementById: () => null, querySelectorAll: () => [], querySelector: () => null };
globalThis.window = { location: { search: "", hash: "" }, addEventListener() {}, removeEventListener() {}, setInterval: globalThis.setInterval, clearInterval: globalThis.clearInterval };
globalThis.location = globalThis.window.location;
globalThis.sessionStorage = { getItem: () => null, setItem() {} };
globalThis.localStorage = { getItem: () => null };
globalThis.fetch = async (url) => new Response(JSON.stringify(url.endsWith("/api/shell/state") ? {} : { token: "test-token" }), { status: 200, headers: { "content-type": "application/json" } });

const { ShellApp, resolveShellSurfaceMode } = await import("../dist/desktop/app.js");

assert.equal(resolveShellSurfaceMode({ search: "", hash: "" }), "full");
assert.equal(resolveShellSurfaceMode({ search: "?surface=background", hash: "" }), "background");
assert.equal(resolveShellSurfaceMode({ search: "?surface=dock", hash: "" }), "dock");
assert.equal(resolveShellSurfaceMode({ search: "", hash: "#surface=overlay" }), "overlay");
assert.equal(resolveShellSurfaceMode({ search: "?surface=toplevel", hash: "" }), "full");
assert.equal(resolveShellSurfaceMode({ search: "?surface=unknown", hash: "#shell_surface=dock" }), "dock");

const expectedWidgets = {
  background: [],
  dock: ["agent-health", "clock", "window-chrome", "taskbar"],
  overlay: ["command-center"],
  full: ["agent-health", "clock", "notifications", "window-chrome", "command-center", "taskbar"],
};

for (const [mode, widgetIds] of Object.entries(expectedWidgets)) {
  const bus = new FakeBus();
  const app = new ShellApp(bus, { mode });
  const root = new FakeElement("div");
  app.mount(root);
  assert.equal(app.mode, mode);
  assert.ok(root.classList.contains("desktop-shell"));
  assert.ok(root.classList.contains(`desktop-shell--${mode}`));
  assert.equal(root.dataset.surfaceMode, mode);
  assert.ok(root.innerHTML.includes(`surface_mode_${mode}_marker`) || mode === "full", `${mode} layout has mode marker or is full fallback`);
  for (const id of widgetIds) {
    assert.ok(app.getWidget(id), `${mode} registers ${id}`);
  }
  for (const id of ["agent-health", "clock", "notifications", "window-chrome", "command-center", "taskbar"].filter((id) => !widgetIds.includes(id))) {
    assert.equal(app.getWidget(id), undefined, `${mode} omits ${id}`);
  }
  assert.equal(bus.connected, true, `${mode} connects the shared bus`);
  if (mode === "dock") {
    root.querySelector("button")?.click();
    assert.equal(bus.published.at(-1)?.topic, "shell.overlay.requested", "dock Command Center trigger requests overlay surface");
    assert.equal(bus.published.at(-1)?.body?.surface, "overlay");
  }
  app.unmount();
}

console.log("desktop shell surface mode tests passed");
