import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { visualID } from "../dist/desktop/visual-markers.js";

const appSource = readFileSync(new URL("./app.ts", import.meta.url), "utf8");
const notificationSource = readFileSync(new URL("./widgets/notification-center.ts", import.meta.url), "utf8");
const widgetSource = readFileSync(new URL("./widgets.ts", import.meta.url), "utf8");

assert.equal(visualID("surface_button", "view:1/demo"), "surface_button_view_1_demo");
assert.equal(visualID("widget", "weather"), "widget_weather");

assert.ok(appSource.includes('setAttribute("data-visual-id", "agora_desktop_shell")'), "root scene has canonical agora_desktop_shell marker");
assert.ok(appSource.includes('setAttribute("data-testid", "agora_desktop_shell")'), "root scene exposes data-testid for visual collection");
assert.ok(appSource.includes('data-visual-id="zone_bottom"'), "taskbar layout zone has a stable marker");
assert.ok(notificationSource.includes('applyVisualMarker(this, "notification_stack", "notification_stack")'), "notification widget uses canonical notification_stack marker");
assert.ok(widgetSource.includes('visualID("widget", normalized.name)'), "injected widget container uses widget_<name> marker vocabulary");
assert.ok(widgetSource.includes('visualID("widget_frame", normalized.name)'), "injected widget iframe uses a distinct widget_frame_<name> marker");

console.log("visual marker contract tests passed");
