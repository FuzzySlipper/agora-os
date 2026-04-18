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
TypeScript/HTML/CSS source lives under `shell/src/`.

Rebuild after frontend changes:

```sh
npm install --prefix shell
npm run --prefix shell build
```
