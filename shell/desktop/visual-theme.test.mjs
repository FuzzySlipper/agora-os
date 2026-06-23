import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const cssPath = path.join(root, "desktop", "styles.css");
const manifestPath = path.join(root, "desktop", "themes", "agora-default", "theme.json");
const themeTsPath = path.join(root, "desktop", "theme.ts");

const css = fs.readFileSync(cssPath, "utf8");
const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
const themeTs = fs.readFileSync(themeTsPath, "utf8");

function tokenToProperty(token) {
  return `--agora-${token.replace(/[._]/g, "-")}`;
}

const cssDefinitions = new Set([...css.matchAll(/(^|\n)\s*(--agora-[a-z0-9-]+)\s*:/g)].map((match) => match[2]));
const usedVars = new Set([...css.matchAll(/var\((--agora-[a-z0-9-]+)/g)].map((match) => match[1]));

for (const [token, value] of Object.entries(manifest.tokens)) {
  const property = tokenToProperty(token);
  assert.ok(cssDefinitions.has(property), `${property} from default manifest is defined in styles.css`);
  assert.ok(themeTs.includes(`"${property}"`), `${property} from default manifest is allowlisted by ThemeController`);
  assert.equal(typeof value, "string", `${token} has a string value`);
}

for (const property of usedVars) {
  assert.ok(cssDefinitions.has(property), `${property} used by styles.css is defined in :root`);
}

for (const requiredSelector of [
  ".agent-health-widget__chip",
  ".notification-center__severity",
  ".notification-center__item--warning",
  ".notification-center__item--error",
  ".window-chrome-widget__surface--pending",
  ".taskbar-widget__surface--minimized",
  ".command-center-widget__row[data-action=\"app.launch\"]",
]) {
  assert.ok(css.includes(requiredSelector), `${requiredSelector} visual-state selector exists`);
}

assert.equal(css.includes("#F472B6"), false, "runtime CSS default path does not use old pink accent literal");
assert.equal(JSON.stringify(manifest.tokens).includes("#F472B6"), false, "default manifest does not use old pink accent literal");

console.log("visual theme token/component tests passed");
