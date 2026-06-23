#!/usr/bin/env node
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { mkdir, readFile, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "../../../..");
const defaultCollector = path.resolve(repoRoot, "../den-services/visual-contract/tools/browser-evidence-collector.mjs");
const defaultRootSelector = "[data-visual-id='agora_desktop_shell']";

function usage() {
  console.error(`usage: collect-scene.mjs --scene-id ID --url URL --out-dir DIR [options]\n\nOptions:\n  --collector PATH       browser-evidence-collector.mjs path (default: ../den-services/visual-contract/tools/browser-evidence-collector.mjs)\n  --root-selector CSS    root selector (default: ${defaultRootSelector})\n  --width PX             viewport width (default: 1920)\n  --height PX            viewport height (default: 1080)\n  --task-id ID           task id for manifest metadata (default: none)\n\nConvenience URL values:\n  --url smoke            uses the checked-in local empty-desktop smoke fixture\n  --url file:PATH        converts PATH to a file:// URL\n`);
}

function arg(name, fallback = "") {
  const index = process.argv.indexOf(name);
  if (index === -1) return fallback;
  return process.argv[index + 1] ?? fallback;
}

const sceneID = arg("--scene-id");
let url = arg("--url");
const outDir = arg("--out-dir");
const collector = path.resolve(arg("--collector", defaultCollector));
const rootSelector = arg("--root-selector", defaultRootSelector);
const width = arg("--width", "1920");
const height = arg("--height", "1080");
const taskID = arg("--task-id", "");

if (!sceneID || !url || !outDir) {
  usage();
  process.exit(2);
}

if (url === "smoke") {
  url = pathToFileURL(path.resolve(__dirname, "../fixtures/empty-desktop-smoke.html")).href;
} else if (url.startsWith("file:")) {
  url = pathToFileURL(path.resolve(url.slice("file:".length))).href;
}

await stat(collector).catch((error) => {
  throw new Error(`collector not found at ${collector}: ${error.message}`);
});

const resolvedOutDir = path.resolve(outDir);
await mkdir(resolvedOutDir, { recursive: true });
const screenshot = path.join(resolvedOutDir, "screenshot.png");
const evidence = path.join(resolvedOutDir, "web-evidence.json");
const manifestPath = path.join(resolvedOutDir, "artifact-manifest.json");
const shaPath = path.join(resolvedOutDir, "SHA256SUMS");

const result = spawnSync(process.execPath, [
  collector,
  "--url", url,
  "--scene-id", sceneID,
  "--capture-mode", "viewport-clipped",
  "--root-selector", rootSelector,
  "--width", width,
  "--height", height,
  "--screenshot", screenshot,
  "--out", evidence,
], { stdio: "inherit" });

if (result.status !== 0) {
  process.exit(result.status ?? 1);
}

async function sha256(filePath) {
  const data = await readFile(filePath);
  return createHash("sha256").update(data).digest("hex");
}

const artifacts = [];
for (const filePath of [screenshot, evidence]) {
  const info = await stat(filePath);
  artifacts.push({
    name: path.basename(filePath),
    path: filePath,
    bytes: info.size,
    sha256: await sha256(filePath),
  });
}

const manifest = {
  schema: "agora-desktop-shell-visual-evidence/v0.1",
  task_id: taskID || undefined,
  scene_id: sceneID,
  source_url: url,
  root_selector: rootSelector,
  viewport: { width_px: Number(width), height_px: Number(height) },
  capture_mode: "viewport-clipped",
  artifacts,
};
await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
artifacts.push({
  name: "artifact-manifest.json",
  path: manifestPath,
  bytes: (await stat(manifestPath)).size,
  sha256: await sha256(manifestPath),
});
await writeFile(shaPath, artifacts.map((artifact) => `${artifact.sha256}  ${artifact.name}`).join("\n") + "\n");

console.log(JSON.stringify({ out_dir: resolvedOutDir, manifest: manifestPath, sha256sums: shaPath, artifacts }, null, 2));
