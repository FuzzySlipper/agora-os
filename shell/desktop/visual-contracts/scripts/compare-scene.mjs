#!/usr/bin/env node
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { mkdir, readFile, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const defaultTemplate = path.resolve(__dirname, "../scenes/empty-desktop.promote.template.json");
const serviceHelper = path.resolve(__dirname, "visual-contract-service.sh");

function usage() {
  console.error(`usage: compare-scene.mjs --local-dir DIR [options]\n\nRuns the loopback-bound den-srv visual-contract service from a normal Agora repo host.\nIt copies evidence to den-srv, runs the checked-out helper over SSH stdin, copies\nservice outputs back, and never prints DEN_VISUAL_CONTRACT_SERVICE_TOKEN.\n\nOptions:\n  --remote-host HOST       SSH host for service (default: den-srv)\n  --remote-dir DIR         remote artifact dir (default: same as --local-dir)\n  --scene-template PATH    promotion template (default: empty-desktop template)\n  --reference PATH         optional reference contract; omit for promoted self-compare smoke\n`);
}

function arg(name, fallback = "") {
  const index = process.argv.indexOf(name);
  if (index === -1) return fallback;
  return process.argv[index + 1] ?? fallback;
}

function shellQuote(value) {
  return `'${String(value).replace(/'/g, `'"'"'`)}'`;
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: "utf8", ...options });
  if (result.status !== 0) {
    if (result.stdout) process.stdout.write(result.stdout);
    if (result.stderr) process.stderr.write(result.stderr);
    throw new Error(`${command} ${args.join(" ")} failed with exit ${result.status}`);
  }
  return result;
}

function ssh(host, remoteCommand, stdin = undefined) {
  return run("ssh", [host, remoteCommand], { input: stdin });
}

function scp(args) {
  return run("scp", args);
}

async function readJSON(filePath) {
  return JSON.parse(await readFile(filePath, "utf8"));
}

async function sha256(filePath) {
  const data = await readFile(filePath);
  return createHash("sha256").update(data).digest("hex");
}

async function artifactRecord(filePath) {
  const info = await stat(filePath);
  return {
    name: path.basename(filePath),
    path: filePath,
    bytes: info.size,
    sha256: await sha256(filePath),
  };
}

const localDir = path.resolve(arg("--local-dir"));
const remoteHost = arg("--remote-host", "den-srv");
const remoteDir = arg("--remote-dir", localDir);
const sceneTemplatePath = path.resolve(arg("--scene-template", defaultTemplate));
const referencePath = arg("--reference") ? path.resolve(arg("--reference")) : "";

if (!arg("--local-dir")) {
  usage();
  process.exit(2);
}

const evidencePath = path.join(localDir, "web-evidence.json");
await stat(evidencePath).catch((error) => {
  throw new Error(`web evidence not found at ${evidencePath}: ${error.message}`);
});
const helperSource = await readFile(serviceHelper, "utf8");
await mkdir(localDir, { recursive: true });

const remote = (name) => `${remoteDir}/${name}`;
ssh(remoteHost, `mkdir -p ${shellQuote(remoteDir)} ${shellQuote(remote("service-artifacts"))}`);
scp([evidencePath, `${remoteHost}:${remote("web-evidence.json")}`]);

ssh(
  remoteHost,
  `bash -s -- from-web-evidence ${shellQuote(remote("web-evidence.json"))} ${shellQuote(remote("candidate.contract.json"))}`,
  helperSource,
);
scp([`${remoteHost}:${remote("candidate.contract.json")}`, path.join(localDir, "candidate.contract.json")]);

const contract = await readJSON(path.join(localDir, "candidate.contract.json"));
const template = await readJSON(sceneTemplatePath);
const promotePayload = {
  contract,
  project: template.project,
  objects: template.objects ?? [],
  ignore_objects: template.ignore_objects ?? [],
  constraints: template.constraints ?? [],
};
const payloadDir = path.join(localDir, "payloads");
await mkdir(payloadDir, { recursive: true });
const promotePayloadPath = path.join(payloadDir, "promote.json");
await writeFile(promotePayloadPath, `${JSON.stringify(promotePayload, null, 2)}\n`);
scp([promotePayloadPath, `${remoteHost}:${remote("promote.json")}`]);

ssh(
  remoteHost,
  `bash -s -- promote ${shellQuote(remote("promote.json"))} ${shellQuote(remote("candidate.promoted.contract.json"))}`,
  helperSource,
);
ssh(
  remoteHost,
  `bash -s -- validate ${shellQuote(remote("candidate.promoted.contract.json"))} ${shellQuote(remote("validate.promoted.json"))}`,
  helperSource,
);

let remoteReference = remote("candidate.promoted.contract.json");
if (referencePath) {
  scp([referencePath, `${remoteHost}:${remote("reference.contract.json")}`]);
  remoteReference = remote("reference.contract.json");
}
ssh(
  remoteHost,
  `bash -s -- compare ${shellQuote(remoteReference)} ${shellQuote(remote("candidate.promoted.contract.json"))} ${shellQuote(remote("compare.json"))}`,
  helperSource,
);
ssh(
  remoteHost,
  `bash -s -- fetch-artifacts ${shellQuote(remote("compare.json"))} ${shellQuote(remote("service-artifacts"))}`,
  helperSource,
);

for (const name of ["candidate.promoted.contract.json", "validate.promoted.json", "compare.json"]) {
  scp([`${remoteHost}:${remote(name)}`, path.join(localDir, name)]);
}
run("rm", ["-rf", path.join(localDir, "service-artifacts")]);
scp(["-r", `${remoteHost}:${remote("service-artifacts")}`, path.join(localDir, "service-artifacts")]);

const compare = await readJSON(path.join(localDir, "compare.json"));
const artifacts = [];
for (const name of [
  "candidate.contract.json",
  "candidate.promoted.contract.json",
  "validate.promoted.json",
  "compare.json",
  "service-artifacts/SHA256SUMS",
]) {
  artifacts.push(await artifactRecord(path.join(localDir, name)));
}
const summary = {
  schema: "agora-desktop-shell-visual-compare/v0.1",
  local_dir: localDir,
  remote_host: remoteHost,
  remote_dir: remoteDir,
  scene_template: sceneTemplatePath,
  reference: referencePath || "self-compare",
  verdict: compare.verdict,
  score: compare.score,
  run_id: compare.run_id,
  artifacts,
};
await writeFile(path.join(localDir, "compare-summary.json"), `${JSON.stringify(summary, null, 2)}\n`);
console.log(JSON.stringify(summary, null, 2));
