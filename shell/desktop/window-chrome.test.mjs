import assert from "node:assert/strict";

class FakeClassList {
  constructor(initial = []) { this.values = new Set(initial); }
  add(...names) { for (const name of names) this.values.add(name); }
  remove(...names) { for (const name of names) this.values.delete(name); }
  contains(name) { return this.values.has(name); }
}

class FakeElement {
  constructor(tagName = "div") {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.parent = null;
    this.dataset = {};
    this.attributes = new Map();
    this.handlers = new Map();
    this.disabled = false;
    this._textContent = "";
    this.classList = new FakeClassList();
  }
  set className(value) {
    this._className = String(value);
    this.classList = new FakeClassList(this._className.split(/\s+/).filter(Boolean));
  }
  get className() { return this._className ?? ""; }
  set textContent(value) { this._textContent = String(value); }
  get textContent() { return [this._textContent, ...this.children.map((child) => child.textContent)].join(""); }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  append(...children) { for (const child of children) { child.parent = this; this.children.push(child); } }
  replaceChildren(...children) { this.children = []; this.append(...children); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter((child) => child !== this); this.parent = null; }
  addEventListener(type, handler) { this.handlers.set(type, [...(this.handlers.get(type) ?? []), handler]); }
  click() {
    const event = { currentTarget: this, stopPropagation() { this.stopped = true; } };
    for (const handler of this.handlers.get("click") ?? []) handler(event);
  }
  querySelectorAll(selector) {
    const matches = [];
    const visit = (node) => {
      if (selector === "button" && node.tagName === "BUTTON") matches.push(node);
      if (selector.startsWith("[data-action=") && node.dataset?.action === selector.match(/\"([^\"]+)\"/)?.[1]) matches.push(node);
      if (selector.startsWith("[data-surface-id=") && node.dataset?.surfaceId === selector.match(/\"([^\"]+)\"/)?.[1]) matches.push(node);
      for (const child of node.children ?? []) visit(child);
    };
    visit(this);
    return matches;
  }
}

class FakeHTMLElement extends FakeElement {
  constructor() { super("custom"); }
}

const registry = new Map();
globalThis.HTMLElement = FakeHTMLElement;
globalThis.document = { createElement: (tagName) => new FakeElement(tagName) };
globalThis.customElements = {
  get: (name) => registry.get(name),
  define: (name, ctor) => registry.set(name, ctor),
};
globalThis.location = { hash: "" };
globalThis.sessionStorage = { getItem: () => null };
globalThis.localStorage = { getItem: () => null };

const { WindowChromeWidget } = await import("../dist/desktop/widgets/window-chrome.js");

const results = [];
const focusCalls = [];
const closeCalls = [];
const moveCalls = [];
const widget = new WindowChromeWidget({
  onActionResult: (result) => results.push(result),
  focusSurface: async (surfaceId) => {
    focusCalls.push(surfaceId);
    return { action: "surface.focus", surface_id: surfaceId, decision: "accepted", focused_surface_id: surfaceId };
  },
  closeSurface: async (surfaceId) => {
    closeCalls.push(surfaceId);
    return { action: "surface.close", surface_id: surfaceId, decision: "accepted", closed_surface_id: surfaceId, queued: true };
  },
  moveSurface: async (surfaceId, geometry) => {
    moveCalls.push({ surfaceId, geometry });
    return { action: "surface.move", surface_id: surfaceId, decision: "accepted", target_geometry: geometry, result_geometry: geometry, surface: { surface: { id: surfaceId, title: "ASHA Studio", geometry } } };
  },
});
widget.mount(new FakeElement("section"));
widget.update({
  surfaces: [
    { id: "view-shell", title: "Agora Desktop Shell", app_id: "agora-shell", role: "toplevel" },
    { id: "view-1", title: "ASHA Studio", app_id: "asha", role: "toplevel", focused: false, geometry: { x: 1, y: 2, width: 800, height: 600 } },
  ],
  agents: [],
  notifications: [],
  config: {},
});

assert.equal(widget.querySelectorAll("[data-surface-id=\"view-shell\"]").length, 0, "shell surface is filtered from work-surface chrome");
assert.equal(widget.querySelectorAll("[data-surface-id=\"view-1\"]").length, 1, "work surface has chrome");
assert.ok(widget.textContent.includes("ASHA Studio"), "chrome shows readable title");
assert.ok(widget.textContent.includes("800×600"), "chrome shows geometry readback");

widget.querySelectorAll("[data-action=\"surface.focus\"]")[0].click();
await Promise.resolve();
assert.deepEqual(focusCalls, ["view-1"]);
assert.equal(results.at(-1).action, "surface.focus");

widget.querySelectorAll("[data-action=\"surface.move\"]").find((button) => button.dataset.dx === "32").click();
await Promise.resolve();
assert.deepEqual(moveCalls, [{ surfaceId: "view-1", geometry: { x: 33, y: 2, width: 800, height: 600 } }]);
assert.equal(results.at(-1).action, "surface.move");
assert.equal(results.at(-1).result_geometry.x, 33);

widget.querySelectorAll("[data-action=\"surface.close\"]")[0].click();
await Promise.resolve();
assert.deepEqual(closeCalls, ["view-1"]);
assert.equal(results.at(-1).action, "surface.close");
assert.equal(results.at(-1).queued, true);
widget.update({
  surfaces: [{ id: "view-1", title: "ASHA Studio", app_id: "asha", role: "toplevel", status: "closing" }],
  agents: [],
  notifications: [],
  config: {},
});
assert.equal(widget.querySelectorAll('[data-surface-id="view-1"]').length, 1, "queued close keeps chrome until authoritative unmap");
assert.ok(widget.textContent.includes("Close requested"), "chrome shows close pending/readback state");

console.log("window chrome widget tests passed");
