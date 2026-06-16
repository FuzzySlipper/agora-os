# den-k8plus Wayfire deployment assets

Task: Den `agora-os` #2511.

This directory contains the host-specific configuration and systemd units for
running the Agora OS compositor stack on the physical den-k8plus display.

## Files

- `wayfire.ini` → install as `/home/agent/.config/wayfire.ini` (`agent:agents`, `0644`).
- `event-bus.service` → install as `/etc/systemd/system/event-bus.service`.
- `compositor-bridge.service` → install as `/etc/systemd/system/compositor-bridge.service`.
- `agora-wayfire.service` → install as `/etc/systemd/system/agora-wayfire.service`.

The units deliberately run `event-bus` and `compositor-bridge` as root because
Phase A2 keeps the bridge/control sockets root-owned. `agora-wayfire.service`
starts Wayfire as the `agent` user on VT1 and conflicts with the display manager.

## Current output assumption

`wayfire.ini` pins `HDMI-A-1`, based on live den-k8plus readback:

- `kscreen-doctor -o`: `HDMI-A-1` connected
- `/sys/class/drm/card0-HDMI-A-1/status`: `connected`

If monitor wiring changes, remove or update the `[output:HDMI-A-1]` section and
let Wayfire auto-detect outputs.

## Sysadmin install sequence

Run from `/home/dev/agora-os` as root or with sudo:

```sh
install -o agent -g agents -m 0644 deploy/den-k8plus/wayfire.ini /home/agent/.config/wayfire.ini
install -o root -g root -m 0644 deploy/den-k8plus/event-bus.service /etc/systemd/system/event-bus.service
install -o root -g root -m 0644 deploy/den-k8plus/compositor-bridge.service /etc/systemd/system/compositor-bridge.service
install -o root -g root -m 0644 deploy/den-k8plus/agora-wayfire.service /etc/systemd/system/agora-wayfire.service
systemctl daemon-reload
systemctl enable event-bus.service compositor-bridge.service agora-wayfire.service
systemctl disable sddm.service
systemctl restart event-bus.service compositor-bridge.service
# Switch/reboot only when ready for the physical session handoff:
# systemctl stop sddm.service
# systemctl start agora-wayfire.service
# reboot
```

After reboot, expected checks:

```sh
systemctl --no-pager --full status event-bus.service compositor-bridge.service agora-wayfire.service
sudo /usr/local/bin/agora-deploy status
sudo /usr/local/bin/compositorctl list-surfaces
```

For Phase A2, `compositorctl list-surfaces` should return an empty surface list
when no Wayland clients are open, not a socket/service error.
