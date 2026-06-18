import assert from "node:assert/strict";
import { createLayoutController } from "../dist/desktop/layout.js";

class FakeStyle {
  constructor() {
    this.values = new Map();
    this.display = "";
  }

  setProperty(name, value) {
    this.values.set(name, String(value));
    if (name === "display") {
      this.display = String(value);
    }
  }

  getPropertyValue(name) {
    return this.values.get(name) ?? "";
  }

  removeProperty(name) {
    this.values.delete(name);
    if (name === "display") {
      this.display = "";
    }
  }
}

class FakeClassList {
  constructor(initial = []) {
    this.values = new Set(initial);
  }

  add(...names) {
    for (const name of names) {
      this.values.add(name);
    }
  }

  remove(...names) {
    for (const name of names) {
      this.values.delete(name);
    }
  }

  contains(name) {
    return this.values.has(name);
  }
}

class FakeElement {
  constructor(slot, classes = []) {
    this.dataset = { widgetSlot: slot };
    this.classList = new FakeClassList(classes);
    this.style = new FakeStyle();
    this.hidden = false;
  }
}

class FakeDocument {
  constructor(elements) {
    this.elements = elements;
  }

  querySelectorAll(selector) {
    return selector === "[data-widget-slot]" ? this.elements : [];
  }
}

class FakeBus {
  constructor() {
    this.handlers = new Map();
  }

  subscribe(topic, handler) {
    const handlers = this.handlers.get(topic) ?? [];
    handlers.push(handler);
    this.handlers.set(topic, handlers);
  }

  emit(topic, body) {
    for (const handler of this.handlers.get(topic) ?? []) {
      handler({ topic, body, timestamp: "test" });
    }
  }
}

const elements = {
  clock: new FakeElement("clock", ["pos-top-right"]),
  taskbar: new FakeElement("taskbar", ["pos-bottom"]),
  notifications: new FakeElement("notifications", ["pos-bottom-right"]),
  agent: new FakeElement("agent-health", ["pos-top-left"]),
};
const documentRef = new FakeDocument(Object.values(elements));
const bus = new FakeBus();
const themes = [];
let fetchCount = 0;
const controller = createLayoutController({
  bus,
  documentRef,
  fetchLayout: async (url) => {
    fetchCount++;
    assert.equal(url, "/api/shell/layout.json");
    return new Response(JSON.stringify({
      widgets: {
        clock: { visible: true, position: "bottom-right", order: 2 },
        taskbar: { visible: false, position: "bottom", order: 1 },
        notifications: { visible: true, position: "elsewhere" },
      },
      theme: { properties: { "--taskbar-bg": "#222" } },
    }), { status: 200, headers: { "content-type": "application/json" } });
  },
  onTheme: (theme) => themes.push(theme),
});

assert.ok(bus.handlers.has("shell.layout_updated"), "controller subscribes to shell.layout_updated");
await controller.loadFromServer();
assert.equal(fetchCount, 1);
assert.ok(elements.clock.classList.contains("pos-bottom-right"));
assert.equal(elements.clock.style.getPropertyValue("order"), "2");
assert.equal(elements.taskbar.hidden, true);
assert.equal(elements.taskbar.style.display, "none");
assert.ok(elements.notifications.classList.contains("pos-center"), "unknown positions default to center");
assert.deepEqual(themes, [{ properties: { "--taskbar-bg": "#222" } }]);

bus.emit("shell.layout_updated", {
  widgets: {
    clock: { visible: false, position: "top-left" },
    taskbar: { visible: true, position: "bottom", order: 5 },
  },
});
assert.ok(elements.clock.classList.contains("pos-top-left"));
assert.equal(elements.clock.hidden, true);
assert.equal(elements.clock.style.display, "none");
assert.equal(elements.taskbar.hidden, false);
assert.equal(elements.taskbar.style.display, "");
assert.equal(elements.taskbar.style.getPropertyValue("order"), "5");

const fallbackBus = new FakeBus();
const fallbackClock = new FakeElement("clock", ["pos-center"]);
const fallbackTaskbar = new FakeElement("taskbar", ["pos-top-right"]);
const fallback = createLayoutController({
  bus: fallbackBus,
  documentRef: new FakeDocument([fallbackClock, fallbackTaskbar]),
  fetchLayout: async () => new Response("missing", { status: 404 }),
});
await fallback.loadFromServer();
assert.ok(fallbackClock.classList.contains("pos-top-right"), "missing layout restores default clock position");
assert.ok(fallbackTaskbar.classList.contains("pos-bottom"), "missing layout restores default taskbar position");
assert.equal(fallbackClock.hidden, false);
assert.equal(fallbackTaskbar.hidden, false);

fallbackBus.emit("shell.layout_updated", {});
assert.ok(fallbackClock.classList.contains("pos-top-right"), "empty update refetches and falls back safely");

console.log("layout handler DOM tests passed");
