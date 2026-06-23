#!/usr/bin/env node
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

function usage() {
  console.error(`usage: make-service-payloads.mjs --contract CONTRACT --scene-template TEMPLATE --out-dir DIR [--reference REF --candidate CANDIDATE]\n\nWrites:\n  promote.json          { contract, project, objects, constraints, ignore_objects }\n  validate.json         { contract }\n  compare.json          { reference, candidate } when both --reference and --candidate are supplied\n`);
}

function arg(name, fallback = "") {
  const index = process.argv.indexOf(name);
  if (index === -1) return fallback;
  return process.argv[index + 1] ?? fallback;
}

async function readJSON(filePath) {
  return JSON.parse(await readFile(filePath, "utf8"));
}

const contractPath = arg("--contract");
const templatePath = arg("--scene-template");
const outDir = arg("--out-dir");
const referencePath = arg("--reference");
const candidatePath = arg("--candidate");

if (!contractPath || !templatePath || !outDir) {
  usage();
  process.exit(2);
}

const contract = await readJSON(contractPath);
const template = await readJSON(templatePath);
await mkdir(outDir, { recursive: true });

const promote = {
  contract,
  project: template.project,
  objects: template.objects ?? [],
  ignore_objects: template.ignore_objects ?? [],
  constraints: template.constraints ?? [],
};
await writeFile(path.join(outDir, "promote.json"), `${JSON.stringify(promote, null, 2)}\n`);
await writeFile(path.join(outDir, "validate.json"), `${JSON.stringify({ contract }, null, 2)}\n`);

if (referencePath && candidatePath) {
  const reference = await readJSON(referencePath);
  const candidate = await readJSON(candidatePath);
  await writeFile(path.join(outDir, "compare.json"), `${JSON.stringify({ reference, candidate }, null, 2)}\n`);
}

console.log(JSON.stringify({ out_dir: path.resolve(outDir), files: ["promote.json", "validate.json", referencePath && candidatePath ? "compare.json" : undefined].filter(Boolean) }, null, 2));
