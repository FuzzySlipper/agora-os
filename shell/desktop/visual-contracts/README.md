# Agora Desktop Shell visual-contract harness

This directory makes visual-contract evidence repeatable for Agora Desktop Shell visual/design tasks.

The harness uses the Den Services `visual-contract` service documented in `den-services/visual-contract-service-usage` and the stable Series A shell markers from #3194. It validates layout/presence/relative structure; it is not a pixel-perfect screenshot diff or a replacement for visual design judgment.

## Directory layout

```text
shell/desktop/visual-contracts/
├── README.md
├── fixtures/
│   ├── command-center-smoke.html
│   ├── empty-desktop-smoke.html
│   ├── notifications-smoke.html
│   ├── surfaces-smoke.html
│   └── theme-default-smoke.html
├── scenes/
│   ├── command-center.promote.template.json
│   ├── empty-desktop.promote.template.json
│   ├── notifications.promote.template.json
│   ├── surfaces.promote.template.json
│   └── theme-default.promote.template.json
└── scripts/
    ├── collect-scene.mjs
    ├── compare-scene.mjs
    ├── make-service-payloads.mjs
    └── visual-contract-service.sh
```

Review/run artifacts should go under:

```text
/tmp/agora-shell-visual/<task-id>/<scene-id>/
├── screenshot.png
├── web-evidence.json
├── artifact-manifest.json
├── SHA256SUMS
├── candidate.contract.json
├── reference.contract.json
├── compare.json
├── report.json
└── diff.overlay.svg
```

Den review packets should include artifact paths and SHA256 hashes from `SHA256SUMS`, never bearer tokens.

## Stable selector contract

Use this root selector unless a design doc explicitly replaces it:

```text
[data-visual-id='agora_desktop_shell']
```

Preferred markers:

- `data-visual-id` for stable object identity.
- `data-visual-role` for domain vocabulary (`taskbar`, `command_center`, `window_chrome`, `surface_button`, `notification_stack`, `agent_health`, `background`, etc.).
- `data-testid` as a fallback stable ID.

## Local smoke scene, no Wayfire required

The checked-in smoke fixture is static HTML and can be collected without a live Wayfire session. It verifies the capture path, marker selection, screenshot output, and checksum manifest.

From the repo root:

```sh
node shell/desktop/visual-contracts/scripts/collect-scene.mjs \
  --scene-id agora_desktop_shell_empty \
  --url smoke \
  --task-id 3196 \
  --out-dir /tmp/agora-shell-visual/3196/agora_desktop_shell_empty
```

The script defaults to `/home/dev/den-services/visual-contract/tools/browser-evidence-collector.mjs`. Override it with `--collector /path/to/browser-evidence-collector.mjs` if needed.

Expected outputs:

- `/tmp/agora-shell-visual/3196/agora_desktop_shell_empty/screenshot.png`
- `/tmp/agora-shell-visual/3196/agora_desktop_shell_empty/web-evidence.json`
- `/tmp/agora-shell-visual/3196/agora_desktop_shell_empty/artifact-manifest.json`
- `/tmp/agora-shell-visual/3196/agora_desktop_shell_empty/SHA256SUMS`

## Live desktop shell scene

For a live shell served by `event-bus-web`, use the same script but point `--url` at the shell:

```sh
node shell/desktop/visual-contracts/scripts/collect-scene.mjs \
  --scene-id agora_desktop_shell_empty \
  --url http://127.0.0.1:7780/shell/dist/desktop/ \
  --task-id <task-id> \
  --out-dir /tmp/agora-shell-visual/<task-id>/agora_desktop_shell_empty
```

Use deterministic viewport defaults (`1920x1080`) unless the reference contract was authored for a different viewport. Keep `--capture-mode viewport-clipped` because the visual-contract service rejects page-space evidence for `from-web-evidence`.

## Service token hygiene

The service is loopback-bound on `den-srv`:

- Health: `http://127.0.0.1:8086/health`
- API base: `http://127.0.0.1:8086/visual-contracts`

Auth is required for `/visual-contracts/*` routes. Load `/etc/den-services/visual-contract.env` **only on `den-srv`** or in an explicit SSH session/tunnel context. Never print, echo, commit, or paste `DEN_VISUAL_CONTRACT_SERVICE_TOKEN` into Den, logs, screenshots, or review packets.

Do **not** assume the Agora repo is checked out on `den-srv`. The normal path is to run from the Agora repo host and stream `visual-contract-service.sh` over SSH stdin. The helper sources the token file internally on `den-srv` and only emits response files.

Health check from the normal Agora repo host:

```sh
ssh den-srv 'bash -s -- health' < shell/desktop/visual-contracts/scripts/visual-contract-service.sh
```

## One followable collect-and-compare sequence

This sequence works from the normal Agora repo host even though the service itself is loopback-bound on `den-srv` and `/home/dev/agora-os` may not exist there.

1. Collect local web evidence:

```sh
node shell/desktop/visual-contracts/scripts/collect-scene.mjs \
  --scene-id agora_desktop_shell_empty \
  --url smoke \
  --task-id 3196 \
  --out-dir /tmp/agora-shell-visual/3196/agora_desktop_shell_empty
```

2. Copy evidence to `den-srv`, convert it, promote it with the scene template, validate it, compare it, fetch service artifacts, and copy response files back:

```sh
node shell/desktop/visual-contracts/scripts/compare-scene.mjs \
  --local-dir /tmp/agora-shell-visual/3196/agora_desktop_shell_empty
```

The compare wrapper defaults to:

- SSH host: `den-srv`
- remote artifact directory: the same path as `--local-dir`
- scene template: `shell/desktop/visual-contracts/scenes/empty-desktop.promote.template.json`
- comparison mode: promoted candidate self-compare, useful for harness smoke tests before a long-lived reference exists

When a reference contract exists, compare against it explicitly:

```sh
node shell/desktop/visual-contracts/scripts/compare-scene.mjs \
  --local-dir /tmp/agora-shell-visual/<task-id>/<scene-id> \
  --reference shell/desktop/visual-contracts/references/agora-default.empty-desktop.contract.json
```

Expected local outputs after the wrapper completes:

- `candidate.contract.json`
- `candidate.promoted.contract.json`
- `validate.promoted.json`
- `compare.json`
- `compare-summary.json`
- `service-artifacts/SHA256SUMS`

`compare-summary.json` includes the service `run_id`, verdict, score, copied-back artifact paths, and SHA256 hashes for Den review packets.

## Manual service commands, if needed

If you need to debug individual service steps, first make the local evidence visible on `den-srv`:

```sh
ssh den-srv 'mkdir -p /tmp/agora-shell-visual/3196/agora_desktop_shell_empty'
scp /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/web-evidence.json \
  den-srv:/tmp/agora-shell-visual/3196/agora_desktop_shell_empty/web-evidence.json
```

Then run the helper over SSH stdin for each step instead of relying on a repo checkout on `den-srv`:

```sh
ssh den-srv 'bash -s -- from-web-evidence \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/web-evidence.json \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/candidate.contract.json' \
  < shell/desktop/visual-contracts/scripts/visual-contract-service.sh

scp den-srv:/tmp/agora-shell-visual/3196/agora_desktop_shell_empty/candidate.contract.json \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/candidate.contract.json

node shell/desktop/visual-contracts/scripts/make-service-payloads.mjs \
  --contract /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/candidate.contract.json \
  --scene-template shell/desktop/visual-contracts/scenes/empty-desktop.promote.template.json \
  --out-dir /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/payloads

scp /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/payloads/promote.json \
  den-srv:/tmp/agora-shell-visual/3196/agora_desktop_shell_empty/promote.json

ssh den-srv 'bash -s -- promote \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/promote.json \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/candidate.promoted.contract.json' \
  < shell/desktop/visual-contracts/scripts/visual-contract-service.sh

ssh den-srv 'bash -s -- validate \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/candidate.promoted.contract.json \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/validate.promoted.json' \
  < shell/desktop/visual-contracts/scripts/visual-contract-service.sh

ssh den-srv 'bash -s -- compare \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/candidate.promoted.contract.json \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/candidate.promoted.contract.json \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/compare.json' \
  < shell/desktop/visual-contracts/scripts/visual-contract-service.sh

ssh den-srv 'bash -s -- fetch-artifacts \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/compare.json \
  /tmp/agora-shell-visual/3196/agora_desktop_shell_empty/service-artifacts' \
  < shell/desktop/visual-contracts/scripts/visual-contract-service.sh
```

The fetched service artifact directory includes a `SHA256SUMS` file for review packet evidence.

## Reference contract storage

Long-lived references should live under theme packages or this directory once they represent intended UI state, for example:

```text
shell/desktop/themes/agora-default/visual-contracts/empty-desktop.contract.json
shell/desktop/themes/agora-default/visual-contracts/theme-default.contract.json
shell/desktop/themes/agora-default/visual-contracts/command-center-open.contract.json
shell/desktop/themes/agora-default/visual-contracts/surfaces.contract.json
shell/desktop/themes/agora-default/visual-contracts/notifications.contract.json
shell/desktop/visual-contracts/references/agora-default.empty-desktop.contract.json
```

The `agora-default` package currently stores the Series B baseline references in the theme package because they are part of the bundled default visual identity. Regenerate them with `collect-scene.mjs` + `compare-scene.mjs`, then copy `candidate.promoted.contract.json` to the matching theme package contract path after review.

Do not commit candidate/review artifacts from `/tmp`. Commit only reviewed reference contracts and reusable scene templates.

## Review checklist

- [ ] Evidence collected with `viewport-clipped` capture mode.
- [ ] Root selector is `[data-visual-id='agora_desktop_shell']` or a documented design-approved equivalent.
- [ ] Artifact paths and SHA256 hashes are included in Den.
- [ ] Token env file was sourced only on `den-srv`; token value was never printed.
- [ ] `npm run --prefix shell test` and `npm run --prefix shell build` still pass.
