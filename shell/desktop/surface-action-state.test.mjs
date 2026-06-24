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

applySurfaceActionEvent(state, {
  topic: "shell.action.completed",
  body: { action: "surface.raise", surface_id: "view-1", decision: "accepted", result_state: { stack: { is_top_in_stack: true, stack_index: 0, stack_count: 3 } }, surface: { surface: { id: "view-1", title: "Agora Desktop Shell", stack_index: 0, stack_count: 3, is_top_in_stack: true } } },
});
assert.equal(state.surfaces.find((surface) => surface.id === "view-1").is_top_in_stack, true, "raise completion updates stack readback");
assert.equal(state.surfaces.find((surface) => surface.id === "view-1").disabled, false, "raise completion clears disabled state");

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

state.surfaces.push({ id: "view-5", title: "Resizable", focused: false, geometry: { x: 10, y: 20, width: 400, height: 300 } });
applySurfaceActionEvent(state, {
  topic: "shell.action.completed",
  body: { action: "surface.resize", surface_id: "view-5", decision: "accepted", target_geometry: { x: 10, y: 20, width: 640, height: 480 }, result_geometry: { x: 10, y: 20, width: 641, height: 481 } },
});
assert.deepEqual(state.surfaces.find((surface) => surface.id === "view-5").geometry, { x: 10, y: 20, width: 641, height: 481 }, "resize completion updates geometry from readback result");
applySurfaceActionEvent(state, {
  topic: "shell.action.denied",
  body: { action: "surface.resize", surface_id: "view-5", decision: "denied", error: "resize target is below minimum" },
});
assert.equal(state.surfaces.find((surface) => surface.id === "view-5").disabled, true, "non-stale resize denial disables entry");

state.surfaces.push({ id: "view-6", title: "Tiled", focused: false, geometry: { x: 10, y: 20, width: 400, height: 300 } });
applySurfaceActionEvent(state, {
  topic: "shell.action.completed",
  body: { action: "surface.tile", surface_id: "view-6", decision: "accepted", target_geometry: { x: 960, y: 0, width: 960, height: 540 }, result_geometry: { x: 960, y: 0, width: 960, height: 540 } },
});
assert.deepEqual(state.surfaces.find((surface) => surface.id === "view-6").geometry, { x: 960, y: 0, width: 960, height: 540 }, "tile completion updates geometry from readback result");

state.surfaces.push({ id: "view-7", title: "Fullscreenable", focused: false, fullscreen: false });
applySurfaceActionEvent(state, {
  topic: "shell.action.completed",
  body: { action: "surface.fullscreen", surface_id: "view-7", decision: "accepted", target_state: { fullscreen: true }, result_state: { fullscreen: true }, fullscreen: true, surface: { surface: { id: "view-7", title: "Fullscreenable", fullscreen: true } } },
});
assert.equal(state.surfaces.find((surface) => surface.id === "view-7").fullscreen, true, "fullscreen completion updates state from authoritative readback");
applySurfaceActionEvent(state, {
  topic: "shell.action.denied",
  body: { action: "surface.fullscreen", surface_id: "view-7", decision: "denied", error: "surface view-7 is not a toplevel" },
});
assert.equal(state.surfaces.find((surface) => surface.id === "view-7").disabled, true, "non-stale fullscreen denial disables entry");

state.surfaces.push({ id: "view-8", title: "Maximizable", focused: false, maximized: false, tiled_edges: { bits: 0 } });
applySurfaceActionEvent(state, {
  topic: "shell.action.completed",
  body: { action: "surface.maximize", surface_id: "view-8", decision: "accepted", target_state: { maximized: true, tiled_edges: { bits: 15, edges: ["top", "bottom", "left", "right"] } }, result_state: { maximized: true, tiled_edges: { bits: 15, edges: ["top", "bottom", "left", "right"] } }, maximized: true, surface: { surface: { id: "view-8", title: "Maximizable", maximized: true, tiled_edges: { bits: 15, edges: ["top", "bottom", "left", "right"] } } } },
});
assert.equal(state.surfaces.find((surface) => surface.id === "view-8").maximized, true, "maximize completion updates state from authoritative readback");
assert.equal(state.surfaces.find((surface) => surface.id === "view-8").tiled_edges.bits, 15, "maximize completion updates tiled edge readback");
applySurfaceActionEvent(state, {
  topic: "shell.action.denied",
  body: { action: "surface.maximize", surface_id: "view-8", decision: "denied", error: "surface view-8 is not a toplevel" },
});
assert.equal(state.surfaces.find((surface) => surface.id === "view-8").disabled, true, "non-stale maximize denial disables entry");

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
