import assert from "node:assert/strict";
import { createWidgetController } from "../dist/desktop/widgets.js";

class FakeClassList {
  constructor(initial = []) { this.values = new Set(initial); }
  add(...names) { for (const name of names) this.values.add(name); }
  remove(...names) { for (const name of names) this.values.delete(name); }
  contains(name) { return this.values.has(name); }
}

class FakeStyle {
  constructor() { this.values = new Map(); }
  setProperty(name, value) { this.values.set(name, String(value)); this[name] = String(value); }
  removeProperty(name) { this.values.delete(name); delete this[name]; }
  getPropertyValue(name) { return this.values.get(name) ?? ""; }
}

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.parent = null;
    this.dataset = {};
    this.classList = new FakeClassList();
    this.style = new FakeStyle();
    this.attributes = new Map();
    this.hidden = false;
    if (tagName === "iframe") {
      this.contentWindow = { posted: [], postMessage(message, targetOrigin) { this.posted.push({ message, targetOrigin }); } };
    }
  }
  set className(value) {
    this._className = value;
    this.classList = new FakeClassList(String(value).split(/\s+/).filter(Boolean));
  }
  get className() { return this._className ?? ""; }
  append(child) { child.parent = this; this.children.push(child); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter((child) => child !== this); this.parent = null; }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
}

class FakeDocument {
  constructor() { this.grid = new FakeElement("section"); this.grid.className = "shell-grid"; }
  createElement(tagName) { return new FakeElement(tagName); }
  querySelector(selector) { return selector === ".shell-grid" ? this.grid : null; }
}

class FakeWindow {
  constructor() { this.handlers = new Map(); }
  addEventListener(type, handler) { this.handlers.set(type, [...(this.handlers.get(type) ?? []), handler]); }
  removeEventListener(type, handler) { this.handlers.set(type, (this.handlers.get(type) ?? []).filter((entry) => entry !== handler)); }
  postMessageFrom(source, data) { for (const handler of this.handlers.get("message") ?? []) handler({ source, data }); }
}

class FakeBus {
  constructor() { this.handlers = new Map(); this.published = []; }
  subscribe(topic, handler) { this.handlers.set(topic, [...(this.handlers.get(topic) ?? []), handler]); }
  publish(topic, body) { this.published.push({ topic, body }); }
  emit(topic, body) { for (const [subscription, handlers] of this.handlers.entries()) if (subscription === topic) for (const handler of handlers) handler({ topic, body, timestamp: "test" }); }
}

const bus = new FakeBus();
const documentRef = new FakeDocument();
const windowRef = new FakeWindow();
const controller = createWidgetController({
  bus,
  documentRef,
  windowRef,
  fetchManifest: async (url) => {
    assert.equal(url, "/api/shell/widget-proxy/weather/manifest.json");
    return new Response(JSON.stringify({
      name: "weather",
      title: "Weather",
      position: "top-right",
      size: { width: 200, height: 100 },
      bus_topics: ["weather.current", "bad topic"],
    }), { status: 200, headers: { "content-type": "application/json" } });
  },
});

assert.ok(bus.handlers.has("shell.widget.inject"), "subscribes to inject");
assert.ok(bus.handlers.has("shell.widget.remove"), "subscribes to remove");
await controller.injectFromPayload({ name: "weather" });
assert.equal(documentRef.grid.children.length, 1);
const container = documentRef.grid.children[0];
assert.ok(container.classList.contains("injected-widget-container"));
assert.ok(container.classList.contains("pos-top-right"));
assert.equal(container.dataset.widgetSlot, "weather");
const iframe = container.children[0];
assert.equal(iframe.tagName, "IFRAME");
assert.equal(iframe.name, "agora-widget-weather");
assert.equal(iframe.src, "/api/shell/widget-proxy/weather/index.html");
assert.equal(iframe.getAttribute("sandbox"), "allow-scripts allow-same-origin");
assert.equal(iframe.getAttribute("loading"), "lazy");
assert.equal(iframe.style.width, "200px");
assert.equal(iframe.style.height, "100px");
assert.ok(bus.handlers.has("weather.current"), "subscribes only to valid manifest topic");
assert.ok(!bus.handlers.has("bad topic"), "rejects invalid manifest topic");

windowRef.postMessageFrom(iframe.contentWindow, { type: "pub", topic: "current", body: { temp: 72 } });
assert.deepEqual(bus.published, [{ topic: "widget.weather.current", body: { temp: 72 } }]);
windowRef.postMessageFrom(iframe.contentWindow, { type: "pub", topic: "admin.escalation.requested", body: { nope: true } });
assert.equal(bus.published.at(-1).topic, "widget.weather.admin.escalation.requested", "prefixes spoofed topics");

bus.emit("weather.current", { temp: 73 });
assert.deepEqual(iframe.contentWindow.posted.at(-1), {
  message: { type: "event", topic: "weather.current", body: { temp: 73 } },
  targetOrigin: "*",
});

bus.emit("shell.widget.remove", { name: "weather" });
assert.equal(documentRef.grid.children.length, 0);
const forwardedCount = iframe.contentWindow.posted.length;
bus.emit("weather.current", { temp: 74 });
assert.equal(iframe.contentWindow.posted.length, forwardedCount, "removed widgets no longer receive forwarded events");

await controller.injectFromPayload({ name: "../../bad" });
assert.equal(documentRef.grid.children.length, 0, "invalid widget names are ignored");

console.log("widget iframe bridge tests passed");
