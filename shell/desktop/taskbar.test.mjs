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
  click() { for (const handler of this.handlers.get("click") ?? []) handler({ currentTarget: this }); }
  querySelectorAll(selector) {
    const matches = [];
    const visit = (node) => {
      if (selector === "button" && node.tagName === "BUTTON") matches.push(node);
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

const { TaskbarWidget } = await import("../dist/desktop/widgets/taskbar.js");

const published = [];
const focusResults = [];
const focusCalls = [];
let commandCenterOpenCount = 0;
const widget = new TaskbarWidget({
  publish: (topic, body) => published.push({ topic, body }),
  onOpenCommandCenter: () => { commandCenterOpenCount += 1; },
  onFocusResult: (result) => focusResults.push(result),
  focusSurface: async (surfaceId) => {
    focusCalls.push(surfaceId);
    return { action: "surface.focus", surface_id: surfaceId, decision: "accepted", focused_surface_id: surfaceId };
  },
});
const container = new FakeElement("nav");
widget.mount(container);
widget.update({
  surfaces: [
    { id: "view-1", title: "ASHA Studio", app_id: "asha", focused: false },
    { id: "view-2", title: "ASHA Studio", app_id: "asha-copy", focused: true },
  ],
  agents: [],
  notifications: [],
  config: {},
});

const buttons = widget.querySelectorAll("button");
assert.equal(buttons.length, 3, "launcher plus two surface buttons");
assert.equal(buttons[0].getAttribute("aria-label"), "Open Command Center");
buttons[0].click();
assert.equal(commandCenterOpenCount, 1, "launcher opens local Command Center state");
assert.equal(published.some((entry) => entry.topic === "conversation.turn.requested" && entry.body?.prompt === "Open launcher"), false, "launcher no longer publishes silent Open launcher placeholder");
const view1Button = widget.querySelectorAll('[data-surface-id="view-1"]')[0];
const view2Button = widget.querySelectorAll('[data-surface-id="view-2"]')[0];
assert.equal(view1Button.dataset.action, "surface.focus");
assert.equal(view1Button.getAttribute("aria-label"), "Focus ASHA Studio · asha");
assert.ok(view1Button.textContent.includes("ASHA Studio · asha"), "duplicate taskbar labels include app id");
assert.ok(view2Button.textContent.includes("ASHA Studio · asha-copy"), "second duplicate taskbar label is also disambiguated");
assert.ok(view1Button.title.includes("view-1"), "full title includes surface id for disambiguation");
view1Button.click();
await Promise.resolve();
assert.deepEqual(focusCalls, ["view-1"]);
assert.equal(focusResults.at(-1).action, "surface.focus");
assert.equal(published.some((entry) => entry.topic === "shell.action.completed" || entry.topic === "shell.action.denied"), false, "taskbar does not spoof authoritative shell.action results");
assert.equal(published.some((entry) => entry.topic === "compositor.advisory.surface.highlight_requested"), false, "taskbar no longer publishes advisory highlight only");

const staleWidget = new TaskbarWidget({
  publish: (topic, body) => published.push({ topic, body }),
  onFocusResult: (result) => focusResults.push(result),
  focusSurface: async () => { throw new Error("surface view-stale is unmapped/stale"); },
});
staleWidget.mount(new FakeElement("nav"));
staleWidget.update({ surfaces: [{ id: "view-stale", title: "Gone" }], agents: [], notifications: [], config: {} });
staleWidget.querySelectorAll("button")[1].click();
await Promise.resolve();
assert.equal(published.some((entry) => entry.topic === "shell.action.denied"), false, "taskbar errors stay local unless backend returns an action result");

console.log("taskbar surface focus tests passed");
