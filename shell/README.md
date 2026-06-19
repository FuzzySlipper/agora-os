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
`/api/shell/widget-proxy/hello-world/`, and publishes a `loaded` postMessage that
the shell prefixes onto the event bus as `widget.hello-world.loaded`.

## Desktop shell launch smoke

After `event-bus-web` is serving the embedded shell assets on port 7780, launch
the desktop shell as a layer-shell panel with:

```sh
compositorctl launch \
  --role panel \
  --url http://127.0.0.1:7780/shell/dist/desktop/ \
  --expected-title "Agora Desktop Shell" \
  --wait-surface
```

The desktop shell should render a clock, taskbar, and agent-health summary at
the same host that serves the operator console. The operator console remains at
`http://127.0.0.1:7780/shell/dist/` and `/shell/`.
