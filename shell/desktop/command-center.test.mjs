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
    this.name = "";
    this.type = "";
    this.value = "";
    this._textContent = "";
    this.classList = new FakeClassList();
  }
  set className(value) { this._className = String(value); this.classList = new FakeClassList(this._className.split(/\s+/).filter(Boolean)); }
  get className() { return this._className ?? ""; }
  set textContent(value) { this._textContent = String(value); }
  get textContent() { return [this._textContent, ...this.children.map((child) => child.textContent)].join(""); }
  set innerHTML(value) { this._textContent = String(value).replace(/<[^>]+>/g, ""); }
  get innerHTML() { return this._textContent; }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  append(...children) { for (const child of children) { child.parent = this; this.children.push(child); } }
  replaceChildren(...children) { this.children = []; this.append(...children); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter((child) => child !== this); this.parent = null; }
  focus() { this.focused = true; }
  addEventListener(type, handler) { this.handlers.set(type, [...(this.handlers.get(type) ?? []), handler]); }
  click() { if (this.disabled) return; for (const handler of this.handlers.get("click") ?? []) handler({ currentTarget: this, target: this, preventDefault() {} }); }
  submit() { for (const handler of this.handlers.get("submit") ?? []) handler({ currentTarget: this, target: this, preventDefault() { this.defaultPrevented = true; } }); }
  querySelectorAll(selector) {
    const matches = [];
    const match = (node) => {
      if (selector === "button" && node.tagName === "BUTTON") return true;
      if (selector === "input" && node.tagName === "INPUT") return true;
      if (selector === "form" && node.tagName === "FORM") return true;
      if (selector.startsWith("[data-action=") && node.dataset?.action === selector.match(/\"([^\"]+)\"/)?.[1]) return true;
      if (selector.startsWith("[data-surface-id=") && node.dataset?.surfaceId === selector.match(/\"([^\"]+)\"/)?.[1]) return true;
      if (selector.startsWith("[data-catalog-id=") && node.dataset?.catalogId === selector.match(/\"([^\"]+)\"/)?.[1]) return true;
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
const registry = new Map();
globalThis.HTMLElement = FakeHTMLElement;
globalThis.KeyboardEvent = class KeyboardEvent {};
globalThis.document = { createElement: (tagName) => new FakeElement(tagName) };
globalThis.customElements = { get: (name) => registry.get(name), define: (name, ctor) => registry.set(name, ctor) };
globalThis.location = { hash: "" };
globalThis.sessionStorage = { getItem: () => null, setItem() {} };
globalThis.localStorage = { getItem: () => null };

const { CommandCenterWidget } = await import("../dist/desktop/widgets/command-center.js");
const { SurfaceFocusError } = await import("../dist/desktop/widgets/taskbar.js");

const published = [];
const focusCalls = [];
const focusResults = [];
const appLaunchResults = [];
const promptRequests = [];
const launchedApps = [];
let closed = 0;
const widget = new CommandCenterWidget({
  publish: (topic, body) => published.push({ topic, body }),
  sessionId: "desktop-shell:test-session",
  turnIdFactory: () => "turn:test-1",
  onClose: () => { closed += 1; },
  onPromptSubmit: (request) => promptRequests.push(request),
  onFocusResult: (result) => focusResults.push(result),
  onAppLaunchResult: (result) => appLaunchResults.push(result),
  loadApps: async () => [
    { id: "terminal", label: "Terminal", state: "ready", reason: "default Agora shell tool" },
    { id: "browser", label: "Browser", state: "disabled", reason: "not installed/allowlisted on this host (#3024)" },
  ],
  launchApp: async (catalogId) => {
    launchedApps.push(catalogId);
    return { action: "app.launch", catalog_id: catalogId, decision: "accepted", launch_id: "launch-test", pid: 1234 };
  },
  focusSurface: async (surfaceId) => {
    focusCalls.push(surfaceId);
    return { action: "surface.focus", surface_id: surfaceId, decision: "accepted", focused_surface_id: surfaceId };
  },
});
const container = new FakeElement("section");
widget.mount(container);
widget.update({
  surfaces: [
    { id: "view-1", title: "Agora Desktop Shell", focused: true },
    { id: "view-2", title: "ASHA Studio", focused: false },
  ],
  agents: [],
  notifications: [],
  config: {},
  commandCenter: { open: true, transcript: [] },
});
for (let i = 0; i < 5 && !widget.textContent.includes("Launch: Terminal"); i += 1) {
  await Promise.resolve();
}
assert.ok(widget.textContent.includes("Command Center"), "open state renders visible overlay");
assert.equal(published.length, 0, "opening Command Center publishes no invisible conversation placeholder");
assert.ok(widget.textContent.includes("Launch: Terminal"), "ready app launch row is visible");
assert.ok(widget.textContent.includes("terminal"), "ready app launch row shows catalog id, not raw command");
assert.ok(widget.textContent.includes("Launch: Browser"), "disabled app launch row is visible");
assert.ok(widget.textContent.includes("not installed/allowlisted on this host (#3024)"), "disabled app launch row includes reason and task link");

const terminalRow = widget.querySelectorAll('[data-catalog-id="terminal"]')[0];
terminalRow.click();
await Promise.resolve();
assert.deepEqual(launchedApps, ["terminal"], "ready app row launches by catalog id only");
assert.equal(appLaunchResults.at(-1).action, "app.launch");
assert.equal(appLaunchResults.at(-1).launch_id, "launch-test");

const surfaceRow = widget.querySelectorAll('[data-surface-id="view-2"]')[0];
surfaceRow.click();
await Promise.resolve();
assert.deepEqual(focusCalls, ["view-2"], "surface row invokes canonical focus action");
assert.equal(focusResults.at(-1).action, "surface.focus");

const deniedResults = [];
const staleResult = { action: "surface.focus", surface_id: "view-stale", decision: "denied", reason: "surface_stale", error: "surface is stale" };
const deniedWidget = new CommandCenterWidget({
  onFocusResult: (result) => deniedResults.push(result),
  focusSurface: async () => { throw new SurfaceFocusError(staleResult); },
});
deniedWidget.mount(new FakeElement("section"));
deniedWidget.update({
  surfaces: [{ id: "view-stale", title: "Stale Surface" }],
  agents: [],
  notifications: [],
  config: {},
  commandCenter: { open: true, transcript: [] },
});
deniedWidget.querySelectorAll('[data-surface-id="view-stale"]')[0].click();
await Promise.resolve();
assert.deepEqual(deniedResults, [staleResult], "denied canonical focus result is delivered for shell readback reconciliation");
assert.ok(deniedWidget.textContent.includes("surface is stale"), "denied focus also remains visible as a local error");

const input = widget.querySelectorAll("input")[0];
input.value = "What is on this desktop?";
widget.querySelectorAll("form")[0].submit();
assert.equal(promptRequests.length, 1, "prompt submit emits one structured request");
assert.equal(promptRequests[0].session_id, "desktop-shell:test-session");
assert.equal(promptRequests[0].turn_id, "turn:test-1");
assert.equal(promptRequests[0].prompt, "What is on this desktop?");
assert.equal(promptRequests[0].context.source, "desktop-command-center");
assert.equal(promptRequests[0].context.focused_surface_id, "view-1");
assert.deepEqual(promptRequests[0].context.visible_surface_ids, ["view-1", "view-2"]);

widget.update({
  surfaces: [{ id: "view-1", title: "Agora Desktop Shell", focused: true }],
  agents: [],
  notifications: [],
  config: {},
  commandCenter: {
    open: true,
    pendingTurnID: "turn:test-1",
    transcript: [{ turn_id: "turn:test-1", prompt: "What is on this desktop?", response: "Two surfaces are visible.", status: "responded" }],
  },
});
assert.ok(widget.textContent.includes("You: What is on this desktop?"), "transcript shows submitted prompt");
assert.ok(widget.textContent.includes("Agora: Two surfaces are visible."), "transcript shows conversation response");

widget.querySelectorAll("button")[0].click();
assert.equal(closed, 1, "close button requests local close state");
console.log("command center widget tests passed");
