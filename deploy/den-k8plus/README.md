# den-k8plus Wayfire deployment assets

Task: Den `agora-os` #2511.

This directory contains the host-specific configuration and systemd units for
running the Agora OS compositor stack on the physical den-k8plus display.

## Files

- `wayfire.ini` → installed as `/home/agent/.config/wayfire.ini` (`agent:agents`, `0644`).
- `event-bus.service` → installed as `/etc/systemd/system/event-bus.service` (`root:root`, `0644`).
- `compositor-bridge.service` → installed as `/etc/systemd/system/compositor-bridge.service` (`root:root`, `0644`).
- `agora-wayfire.service` → installed as `/etc/systemd/system/agora-wayfire.service` (`root:root`, `0644`).
- `agora-shell-panel.service` → installed as `/etc/systemd/system/agora-shell-panel.service` (`root:root`, `0644`).
- `agora-shell-panel-supervisor` → installed as `/usr/local/bin/agora-shell-panel-supervisor` (`root:root`, `0755`).

The units deliberately run `event-bus` and `compositor-bridge` as root because
Phase A2 keeps the bridge/control sockets root-owned. `agora-wayfire.service`
starts Wayfire as the `agent` user on VT1 and conflicts with the display manager.

## Current output assumption

`wayfire.ini` pins `HDMI-A-1`, based on live den-k8plus readback:

- `kscreen-doctor -o`: `HDMI-A-1` connected
- `/sys/class/drm/card0-HDMI-A-1/status`: `connected`

If monitor wiring changes, remove or update the `[output:HDMI-A-1]` section and
let Wayfire auto-detect outputs.

## Ordinary-agent update path

On den-k8plus, ordinary agents deploy this host asset set through the root-owned
fixed-action wrapper:

```sh
sudo -n /usr/local/bin/agora-deploy install-host-assets
```

The wrapper intentionally accepts no path arguments. It uses the fixed source
directory `/home/dev/agora-os/deploy/den-k8plus` and only installs the reviewed
allow-listed unit/config/supervisor assets above. It refuses dirty/untracked
files under this directory, rejects symlinked or world-writable source assets,
stages them into a root-owned tempdir, runs content guards for the units and
shell-panel supervisor, runs `systemd-analyze verify`, creates a timestamped
backup under `/root/agora-host-assets-backup-*`, installs the files, runs
`systemctl daemon-reload`, re-verifies the installed units, enables the panel
unit for the next Wayfire compositor start, and restarts only support services
(`event-bus.service`, `event-bus-web.service`, and `compositor-bridge.service`).
`agora-shell-panel.service` is deliberately not started by the generic host-asset
install path, because starting it can pull in the physical Wayfire session through
its `Wants=` relationship. The panel is stopped automatically when
`agora-wayfire.service` stops (`BindsTo=`/`PartOf=`) and is restarted by systemd
on shell-panel crashes once started by the compositor lifecycle or an explicit
sysadmin/operator action. It must not restart or stop `sddm.service` or
`display-manager.service` unless a separate sysadmin/operator action explicitly
chooses a physical display cutover.

For a full code/artifact deploy, ordinary agents can run:

```sh
sudo -n /usr/local/bin/agora-deploy all
```

`all` builds the fixed Go binaries, builds/installs the Wayfire plugin, installs
these host assets through the guarded path, and reports status. The `build-go`
sub-action still requires the repo to be clean and on a tracked branch or `main`.

## Desktop shell panel auto-start

`agora-shell-panel.service` is a root-managed system unit that runs the actual
panel payload as `agent:agents`. It is enabled under `agora-wayfire.service` and
uses `BindsTo=`/`PartOf=` so the panel is part of the physical Wayfire lifecycle
without bouncing the compositor when support services restart.

The service intentionally executes `/usr/local/bin/agora-shell-panel-supervisor`
instead of `compositorctl launch` directly. `compositorctl launch --wait-surface`
returns after the WebKit layer-shell surface maps, so it is not itself a
foreground supervisor. The wrapper launches the corrected deployed shell URL,
records the returned `launch_id`/surface/pids, polls `compositorctl list-surfaces`,
terminates the launch on SIGTERM, and exits non-zero if the panel surface or
process disappears. systemd then satisfies the crash-restart requirement with
`Restart=on-failure` and `RestartSec=3s`.

Canonical launch shape:

```sh
/usr/local/bin/compositorctl --pretty launch \
  --role panel \
  --url http://127.0.0.1:7780/shell/dist/desktop/ \
  --expected-app-id agora-webview \
  --expected-title agora-webview \
  --wait-surface \
  --wait-timeout-ms 8000
```

Useful service checks:

```sh
systemctl --no-pager --full status agora-shell-panel.service
journalctl --no-pager -u agora-shell-panel.service -n 80
sudo -n /usr/local/bin/compositorctl --pretty list-surfaces
```

To rollback the panel service only, disable and stop it, then restore
`agora-shell-panel.service.bak` and `agora-shell-panel-supervisor.bak` from the
backup directory printed by `agora-deploy install-host-assets` if needed:

```sh
sudo systemctl disable --now agora-shell-panel.service
```

Because `agora-deploy` itself is root-owned at `/usr/local/bin/agora-deploy`,
changes to the wrapper must be promoted to that path before a new allow-listed
host asset (such as `agora-shell-panel.service`) can be installed through the
normal `install-host-assets` action. Treat that wrapper promotion plus any
`/etc/systemd/system` install/restart as the sysadmin/privileged gate for this
service.

## Default shell content

Default first-launch content is installed into the shared config directory with:

```sh
compositorctl shell install-defaults --config-dir /etc/agora-shell
compositorctl shell list-widgets --config-dir /etc/agora-shell
```

`install-defaults` creates `/etc/agora-shell/layout.json` only when it is missing
and installs the packaged `hello-world` widget under
`/etc/agora-shell/widgets/hello-world/`. Use `install-example-widgets` when you
want to refresh packaged widgets without touching an operator-customized layout.
The shell loads visible non-built-in widgets from `layout.json` on boot, so a
panel restart after this install shows the `hello-world` iframe at `top-left`.

## Desktop shell token/session smoke

`event-bus-web.service` serves the desktop shell and the browser WebSocket bridge
on `127.0.0.1:7780`. The service keeps the HMAC secret root-owned for the
normal `event-bus-web mint-token` path, so local browser/operator smokes should
not read `/run/agent-os/event-bus-web.secret` directly.

For local-only shell sessions, the shell API exposes:

```sh
curl -fsS http://127.0.0.1:7780/api/shell/session-token
```

The endpoint is loopback-only, returns `Cache-Control: no-store`, and mints a
short-lived human WebSocket token for the desktop shell. The shell page
(`/shell/dist/desktop/`) requests this endpoint automatically when no `#token=`
fragment or stored token is already present, then connects to `/ws` with the
`agora.token.<token>` WebSocket subprotocol. This preserves the existing `/ws`
authentication and origin policy while avoiding ad hoc unprivileged root-secret
reads.

A live widget/theme smoke can use the deployed service plus the CLI event path:

```sh
curl -fsS http://127.0.0.1:7780/shell/dist/desktop/
TOKEN_JSON=$(curl -fsS http://127.0.0.1:7780/api/shell/session-token)
compositorctl shell add-widget --config-dir /home/agent/.config/agora-shell \
  --name smoke_widget --url /path/to/widget-dir
compositorctl shell set-theme --config-dir /home/agent/.config/agora-shell \
  --properties '{"--shell-accent":"#88ccff"}'
```

Then verify in the browser page that `window.agoraDesktopShell.bus.status` is
`"connected"`, the injected widget iframe appears, and the theme applied event
is consumed/published.

## Verification

After deploying host assets, expected checks are:

```sh
sudo -n /usr/local/bin/agora-deploy status
systemctl --no-pager --full status event-bus.service compositor-bridge.service agora-wayfire.service
sudo -n /usr/local/bin/compositorctl list-surfaces
```

For Phase A2, `compositorctl list-surfaces` should return an empty surface list
when no Wayland clients are open, not a socket/service error.

## Sysadmin rollback shape

Each `install-host-assets` run prints and records a backup directory such as
`/root/agora-host-assets-backup-YYYYmmdd-HHMMSS`. To roll back a host-asset update,
copy the desired `.bak` files back to their targets, run `systemctl daemon-reload`,
verify the units with `systemd-analyze verify`, and restart only the support
services unless the display session itself must be rolled back.

For full display rollback to SDDM:

```sh
sudo systemctl stop agora-wayfire.service
sudo systemctl disable agora-wayfire.service
sudo systemctl enable --now sddm.service
```
