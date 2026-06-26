# Agora Shell UI

The human shell is a browser-style operator console served by `event-bus-web`
at `/shell/`.

## What it uses

- `GET /api/shell/state` for agent, surface, and pending-escalation snapshots
- `POST /api/shell/grants` to record viewport grants
- `POST /api/shell/escalations/decide` to record human escalation decisions
- `/api/shell/audit/ws` for the live audit tail
- `/ws` for event-bus topics such as `agent.lifecycle.*` and
  `compositor.surface.*`

## Token flow

The shell expects a human token minted by:

```sh
go run ./cmd/event-bus-web mint-token --human
```

In practice the shell can be opened in a webview like:

```sh
webview-launcher --url=http://127.0.0.1:7780/shell/#token=<human-token>
```

The token is read from the URL fragment, stored in local storage, and never
sent to the server as a query parameter.

For the most realistic current manual loop, run `test/phase3.sh` inside the
graphical guest Wayfire session. If you set `AGORA_PHASE3_HOLD=1`, the script
keeps the shell and two agent-owned webviews running after the automated probe
passes so you can inspect the live UI before cleanup.

## Frontend build

The checked-in assets served by Go live under `shell/dist/`. The editable
TypeScript/HTML/CSS source lives under `shell/src/`, `shell/shared/`, and
`shell/desktop/`.

Rebuild after frontend changes:

```sh
npm install --prefix shell
npm run --prefix shell build
```

## Default config and example widgets

Package defaults live under `shell/example-widgets/` and are installed with
`compositorctl`:

```sh
# Create layout.json only if it is missing and install packaged widgets.
compositorctl shell install-defaults

# Refresh packaged widgets without touching an existing customized layout.json.
compositorctl shell install-example-widgets

# Confirm installed widgets.
compositorctl shell list-widgets
```

By default the CLI uses `/etc/agora-shell` when that shared config directory is
present; pass `--config-dir /path/to/agora-shell` for development or tests. The
packaged `hello-world` widget is listed in the default `layout.json`, loads via
`/api/shell/widget-proxy/hello-world/`, and publishes a `hello-world.loaded`
postMessage that the shell prefixes onto the event bus as
`widget.hello-world.loaded`.

## Shell dev-mode static assets

For frontend iteration, `event-bus-web` can serve shell assets from the local
filesystem instead of the embedded `shell/dist` assets compiled into the Go
binary:

```sh
event-bus-web --shell-dev-dir /home/dev/agora-os/shell/dist
```

With this flag, changes written to `shell/dist/` are picked up on the next HTTP
request, so the edit loop is `npm run --prefix shell build` plus browser refresh
or shell-panel restart. Leave the flag unset in production to use embedded
assets with no dependency on a checkout path.

## Desktop shell launch smoke

After `event-bus-web` is serving the embedded shell assets on port 7780, launch
the desktop shell with the same visible fallback used by the host service:

```sh
compositorctl launch \
  --cmd "webview-launcher --url http://127.0.0.1:7780/shell/dist/desktop/ --role toplevel --width 2560 --height 1440 --title AGORA-SHELL-TOPLEVEL --app-id io.agoraos.ShellPanel --fullscreen" \
  --expected-title "Agora Desktop Shell" \
  --wait-surface
```

WebKitGTK layer-shell smokes (`--role panel`/`overlay`) are still useful for
debugging, but on the current den-k8plus Wayfire/WebKit stack they can map while
presenting black/no frames. Treat compositor presentation/capture evidence as
the visibility gate, not `mapped` alone.

### WebKitGTK readiness evidence

Do not treat `frame_count=0` as proof that a WebKitGTK surface has not rendered.
On den-k8plus a visible/nonblank WebKitGTK toplevel capture can be produced while
`list-surfaces` still reports `frame_count: 0` and no
`last_present_timestamp`; the plugin does not always receive a compositor
`frame_done` event for WebKitGTK in time for readiness checks. A fresh capture
now records separate capture-readback evidence (`capture_count`,
`last_capture_timestamp`, and `capture.captured_at`) so agents can distinguish
"compositor presented-frame event observed" from "plugin readback captured
visible pixels".

For WebKitGTK readiness, use this evidence ladder:

1. `mapped`/`visible` readback identifies the target surface, but is not enough.
2. `frame_count > 0` plus `last_present_timestamp` is strong compositor-presented
   evidence when available.
3. If `frame_count` remains zero, require a fresh `compositorctl capture` result
   for the same surface with `visual_inspection.status == "visible"`, a nonzero
   `capture_count` / `last_capture_timestamp` on subsequent surface readback,
   and a current `captured_at` timestamp in the capture response.
4. Treat blank/black captures as failures even when the surface is mapped.

### Desktop shell surface modes

The desktop shell can render separate opt-in surface modes without changing the
production default fullscreen fallback. Mode selection is URL-driven so the same
`ShellApp`, bus controllers, and widget implementations can be reused by layer
specific webviews:

```text
/shell/dist/desktop/?surface=background
/shell/dist/desktop/?surface=dock
/shell/dist/desktop/?surface=overlay
/shell/dist/desktop/?surface=full
```

`surface=toplevel` and `surface=fallback` alias to `full`. Unknown modes are
ignored and also fall back to `full`.

Mode composition:

- `background`: wallpaper/backdrop and a visual mode marker only; no taskbar,
  Command Center, notifications, or critical controls.
- `dock`: taskbar/launcher, clock, agent health, and Work Surface controls.
  This is the intended persistent control surface for split-shell canaries. Its
  Command Center button publishes `shell.overlay.requested` for the future
  split-shell supervisor instead of rendering a modal inside the dock surface.
- `overlay`: Command Center/modal surface only. It opens Command Center by
  default so an overlay webview is visibly useful when launched.
- `full`: current all-in-one fallback with all default widgets. This remains the
  production default until split-shell supervisor/live canary work switches it
  explicitly.

Every non-full mode adds `data-surface-mode` and a stable
`surface_mode_<mode>_marker` visual marker so screenshots and smoke artifacts can
identify which shell surface was loaded.

### Webview launch identity and session cleanup

For agent test webviews, prefer app-id/session/launch handles over page titles.
A WebKitGTK surface title used by `list-surfaces`/launch matching is the
launcher `webview-launcher --title` value observed at launch; it is not a stable
HTML page `<title>` identity. The page title can still appear in WebKit/GTK
window chrome or page content after load, so prove page identity by
capture/a11y/app-command evidence, not by expecting compositor readback to mirror
the HTML title. The `compositorctl launch --expected-title` matcher is therefore
a launcher-title substring matcher.

The safest CLI shape for URL-launched webviews avoids hand-quoting a large
single `--cmd` string and matches by app id:

```sh
SESSION=$(compositorctl --pretty session create \
  --label task-3430-smoke --project-id agora-os --task-id 3430)
SESSION_ID=$(jq -r .session_id <<<'$SESSION')
SESSION_TOKEN=$(jq -r .session_token <<<'$SESSION')
export AGORA_COMPOSITOR_SESSION_TOKEN="$SESSION_TOKEN"

compositorctl --pretty launch \
  --session "$SESSION_ID" \
  --path "/tmp/agora-3430.html" \
  --webview-title AGORA-3430-PROBE \
  --app-id io.agoraos.probe.3430 \
  --expected-app-id io.agoraos.probe.3430 \
  --wait-surface \
  --wait-timeout-ms 8000
```

Keep the returned `launch_id`, `session_id`, and `surface.surface.id`. If a
launch uses `--session`, cleanup commands also need the session token. Pass
`--session-token` explicitly or keep `AGORA_COMPOSITOR_SESSION_TOKEN` exported:

```sh
compositorctl --pretty terminate --launch-id "$LAUNCH_ID" --session-token "$SESSION_TOKEN"
compositorctl --pretty session destroy --session "$SESSION_ID" --session-token "$SESSION_TOKEN"
```

If a launch intermittently returns `app_not_ready`, use bounded retry/backoff:
create one session for the smoke, attempt the launch up to a small cap, preserve
every returned `launch_id`/process handle, run `list-processes --session` after
failures, terminate any running attempts with the token, and finally destroy the
session with the token. Do not retry in an unbounded loop or drop failed launch
handles.

## Desktop theme packages

The desktop shell loads the selected theme manifest from
`/api/shell/theme.json` at mount time. `event-bus-web` resolves that endpoint
from the shell config directory first:

- `${SHELL_CONFIG_DIR:-/var/lib/agora-shell}/theme-selection.json` selects a
  package with `{ "selected_theme_id": "<id>", "source": "runtime" }`.
- Runtime packages live under
  `${SHELL_CONFIG_DIR:-/var/lib/agora-shell}/themes/<id>/theme.json`.
- If no runtime selection exists, the bundled `agora-default` manifest is used;
  its visual identity is **Agora Observatory** (dark teal observatory/workbench
  with cyan primary accent and amber secondary accent). The editable source
  package lives at `shell/desktop/themes/agora-default/theme.json`, includes
  reference visual contracts under
  `shell/desktop/themes/agora-default/visual-contracts/`, and is copied into
  `shell/dist/desktop/themes/` by `npm run --prefix shell build`.

Theme manifests may set #3193 contract tokens, wallpaper, and optional
safe-visual-only CSS overrides. The frontend validates token names/values and
only accepts known `--agora-*` tokens or explicitly enabled `extension.*`
tokens; theme CSS served through `/api/shell/theme/<id>/...` is filtered by the
server-side safe visual CSS sanitizer.

The desktop shell should render a clock, taskbar, and agent-health summary at
the same host that serves the operator console. The operator console remains at
`http://127.0.0.1:7780/shell/dist/` and `/shell/`.
