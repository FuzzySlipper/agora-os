import assert from "node:assert/strict";

globalThis.HTMLElement = class {};
globalThis.document = {
  documentElement: { style: { setProperty() {} } },
  getElementById: () => null,
  createElement: () => ({ setAttribute() {}, addEventListener() {}, append() {}, replaceChildren() {}, classList: { add() {} } }),
};
globalThis.customElements = { get: () => undefined, define: () => undefined };
globalThis.location = { hash: "" };
globalThis.sessionStorage = { getItem: () => null, setItem() {} };
globalThis.localStorage = { getItem: () => null };

const { applyShellStateSnapshot, applySurfaceActionEvent } = await import("../dist/desktop/app.js");

const state = {
  surfaces: [
    { id: "view-1", title: "Agora Desktop Shell", focused: true },
    { id: "view-2", title: "ASHA Studio", focused: false },
  ],
  agents: [],
  notifications: [],
  config: {},
};

applySurfaceActionEvent(state, {
  topic: "shell.action.completed",
  body: {
    action: "surface.focus",
    surface_id: "view-2",
    decision: "accepted",
    focused_surface_id: "view-2",
    surface: { surface: { id: "view-2", title: "ASHA Studio", app_id: "asha" }, focused: true, visible: true },
  },
});
assert.equal(state.surfaces.find((surface) => surface.id === "view-1").focused, false, "previous surface is no longer focused");
assert.equal(state.surfaces.find((surface) => surface.id === "view-2").focused, true, "focused state comes from action/readback");
assert.equal(state.surfaces.find((surface) => surface.id === "view-2").app_id, "asha", "readback merges into surface state");

applySurfaceActionEvent(state, {
  topic: "shell.action.denied",
  body: { action: "surface.focus", surface_id: "view-2", decision: "denied", error: "surface view-2 is unmapped/stale" },
});
assert.equal(state.surfaces.some((surface) => surface.id === "view-2"), false, "stale focus denial removes stale taskbar entry");

applySurfaceActionEvent(state, {
  topic: "shell.action.denied",
  body: { action: "surface.focus", surface_id: "view-1", decision: "denied", error: "focus not confirmed" },
});
const disabled = state.surfaces.find((surface) => surface.id === "view-1");
assert.equal(disabled.disabled, true, "non-stale denial disables entry");
assert.equal(disabled.action_error, "focus not confirmed");

state.surfaces.push({ id: "view-3", title: "Throwaway", focused: false });
applySurfaceActionEvent(state, {
  topic: "shell.action.completed",
  body: { action: "surface.close", surface_id: "view-3", decision: "accepted", closed_surface_id: "view-3", queued: true },
});
const closing = state.surfaces.find((surface) => surface.id === "view-3");
assert.equal(closing.status, "closing", "queued close marks surface closing");
assert.equal(state.surfaces.some((surface) => surface.id === "view-3"), true, "queued close keeps surface until compositor unmap/readback");

state.surfaces.push({ id: "view-4", title: "Movable", focused: false, geometry: { x: 0, y: 0, width: 400, height: 300 } });
applySurfaceActionEvent(state, {
  topic: "shell.action.completed",
  body: { action: "surface.move", surface_id: "view-4", decision: "accepted", target_geometry: { x: 64, y: 96, width: 400, height: 300 }, result_geometry: { x: 65, y: 97, width: 400, height: 300 } },
});
assert.deepEqual(state.surfaces.find((surface) => surface.id === "view-4").geometry, { x: 65, y: 97, width: 400, height: 300 }, "move completion updates geometry from readback result");
applySurfaceActionEvent(state, {
  topic: "shell.action.denied",
  body: { action: "surface.move", surface_id: "view-4", decision: "denied", error: "surface view-4 is unmapped/stale" },
});
assert.equal(state.surfaces.some((surface) => surface.id === "view-4"), false, "stale move denial removes stale surface");

const snapshotState = {
  surfaces: [
    { id: "view-old", title: "Stale" },
    { id: "view-keep", title: "Old Title" },
  ],
  agents: [],
  notifications: [],
  config: {},
};
applyShellStateSnapshot(snapshotState, {
  surfaces: [
    { surface: { id: "view-keep", title: "Updated", app_id: "demo" }, focused: true },
    { surface: { id: "view-new", title: "New Surface" }, focused: false },
  ],
  agents: [{ identity: "agent-a", status: "available" }],
});
assert.deepEqual(snapshotState.surfaces.map((surface) => surface.id).sort(), ["view-keep", "view-new"], "shell state snapshot replaces stale taskbar surfaces with live readback");
assert.equal(snapshotState.surfaces.find((surface) => surface.id === "view-keep").focused, true, "snapshot preserves focused readback");
assert.equal(snapshotState.agents[0].identity, "agent-a", "snapshot updates agent readback");

console.log("surface action state tests passed");
