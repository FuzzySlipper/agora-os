# Test Guide

Phase 1 testing is **VM-first**. Changes that depend on kernel attribution,
cross-uid behavior, privileged paths, or host services are not considered
validated based on local sandbox runs alone.

Phase 2 testing is **Wayfire-session-first**. The authoritative proof now needs
a live compositor session with the Agora Wayfire plugin loaded, because the
important behavior is in-process input mediation rather than just Go-side state.

Phase 3 testing uses the same graphical guest setup, but now the acceptance
target is the full shell/webview loop rather than only the grant path.

## Controlled X11/Xvfb fixture lane

`test/fixtures/x11-xvfb/` is reserved for deterministic commodity UI
automation fixtures. That lane is useful for browser/component smokes,
agent-sim comparison pages, fixed-size screenshots, and similar tests where
Wayland compositor identity is intentionally irrelevant.

An X11/Xvfb pass is **not** production desktop evidence. It does not validate
Wayland isolation, Agora compositor bridge grants, input mediation, clipboard
mediation, surface ownership, audit attribution, or Wayland compatibility. Any
test that asserts those properties must run through the real Wayland/VM path:
`test/phase2.sh`, `test/phase3.sh`, or a future compositor computer-use probe.

## Use the VM for authoritative Phase 1 checks

- Validate `SO_PEERCRED` behavior in the disposable VM.
- Validate Unix sockets under `/run/agent-os` in the VM.
- Validate `useradd`, per-uid process execution, and `systemd-run` in the VM.
- Validate nftables, fanotify, and writes under `/var/log/agent-os` in the VM.
- Prefer reproducible integration scripts under `test/` for Phase 1 acceptance.

The local sandbox may block socket creation or other privileged operations, so
`go test ./...` on the host can produce false negatives for Phase 1 system
behavior.

## Use the graphical VM guest for authoritative Phase 2 and Phase 3 checks

- Restore the `phase2-deps` snapshot and boot the VM with `scripts/vm.sh gui`.
- Run `test/phase2.sh` against a live Wayfire session with the `agora-bridge`
  plugin loaded.
- Current Wayfire refuses to run as root. Use a normal compositor user such as
  `dev`, and let the tests derive the trusted plugin uid from the Wayland
  socket owner (or override it with `AGORA_COMPOSITOR_UID`). The scripts still
  run as root so they can launch uid-0 human clients and agent-owned clients.
- Install one supported native Wayland terminal client: `foot`,
  `weston-terminal`, `alacritty`, or `kitty`.
- Install `wtype` so the script can generate a real keyboard event for the
  plugin deny/grant path.
- Treat the guest Wayfire session as disposable: `test/phase2.sh`
  temporarily relaxes the compositor socket permissions so a spawned agent uid
  can connect to the session.
- Install the GTK/WebKit runtime used by `cmd/webview-launcher`; the current
  `scripts/vm.sh phase2-deps` guest provisioning step now includes it.

## Integration tests

- **`test/phase1.sh`**: end-to-end Phase 1 scenario. Starts all three services
  (isolation, admin-agent, audit), spawns an agent with resource limits
  (cpu 50%, mem 256M, net deny), then verifies:
  - cgroup limits are enforced (cpu.max, memory.max in cgroupfs)
  - outbound network access is blocked by nftables
  - file I/O is captured in the audit log attributed to the agent uid
  - escalation request is logged with kernel-verified uid and safe default decision
  - terminate removes the user, systemd unit, slice, nft rules, and home directory

  Runs 19 assertions. Requires root, `python3`, and `nft`.

- **`test/phase1-peercred.sh`**: focused proof that `SO_PEERCRED` attribution
  overrides self-reported identity and that cross-uid authorization checks
  hold. Creates a temporary user, starts isolation + admin-agent, and runs
  four assertions (uid override in admin-agent log, spawn denied for non-root,
  cross-uid terminate denied, list_agents filtered). Requires root and
  `python3`.

- **`test/phase2.sh`**: end-to-end Phase 2 compositor proof. Starts the event
  bus, isolation service, and compositor bridge; launches one agent-owned and
  one uid-0 Wayland surface; verifies surface attribution; proves agent-driven
  keyboard input to the human surface is denied before a viewport grant; then
  records a grant via `compositorctl` and verifies the denial stops. Requires a
  running Wayfire session with the plugin loaded, root, `python3`, `wtype`,
  and one supported native Wayland terminal client.

- **`test/phase3.sh`**: end-to-end Phase 3 shell/webview proof. Starts the
  event bus, audit service, isolation service, compositor bridge, and
  `event-bus-web`; launches the human shell webview plus two agent-owned
  WebKitGTK windows; verifies an `agent.message.<from>.<to>.chat` event crosses
  the web bridge, the receiver acknowledges on `webview.broadcast.phase3.ack`,
  the shell state API shows both agents and their surfaces, and the shell audit
  websocket sees recent activity from both agent uids. Requires a running
  Wayfire session with the plugin loaded, root, `python3`, `curl`, and the
  GTK/WebKit runtime used by `cmd/webview-launcher`.

## Typical loop

### Phase 1

```sh
scripts/vm.sh start
scripts/vm.sh ssh -- 'cd /repo && go test ./...'
scripts/vm.sh ssh -- 'cd /repo && sudo test/phase1.sh'
scripts/vm.sh ssh -- 'cd /repo && sudo test/phase1-peercred.sh'
scripts/vm.sh stop
```

### Phase 2

```sh
scripts/vm.sh restore phase2-deps
scripts/vm.sh gui
```

Then start the dev-owned compositor session. The `phase2-deps` provisioner
creates `/home/dev/.config/wayfire-agora.ini` with the `agora-bridge` plugin
enabled and adds `dev` to the required seat/device groups:

```sh
scripts/vm.sh ssh -- 'sudo systemctl start seatd && sudo install -d -o dev -g dev -m 0700 /run/user/1000 && sudo openvt -c 2 -f -s -- runuser -u dev -- sh -lc "export XDG_RUNTIME_DIR=/run/user/1000; export WLR_RENDERER_ALLOW_SOFTWARE=1; exec dbus-run-session -- wayfire -c /home/dev/.config/wayfire-agora.ini >/tmp/wayfire.log 2>&1"'
```

From another host terminal, run the Phase 2 proof against that guest session:

```sh
scripts/vm.sh ssh -- 'cd /repo && sudo env XDG_RUNTIME_DIR=/run/user/1000 WAYLAND_DISPLAY=$(basename $(ls /run/user/1000/wayland-* | grep -v lock | head -n1)) test/phase2.sh'
```

### Phase 3

Use the same guest session shape, but run the Phase 3 proof:

```sh
scripts/vm.sh restore phase2-deps
scripts/vm.sh gui
```

Then start the same dev-owned compositor session shape:

```sh
scripts/vm.sh ssh -- 'sudo systemctl start seatd && sudo install -d -o dev -g dev -m 0700 /run/user/1000 && sudo openvt -c 2 -f -s -- runuser -u dev -- sh -lc "export XDG_RUNTIME_DIR=/run/user/1000; export WLR_RENDERER_ALLOW_SOFTWARE=1; exec dbus-run-session -- wayfire -c /home/dev/.config/wayfire-agora.ini >/tmp/wayfire.log 2>&1"'
```

From another host terminal, run the Phase 3 proof against that guest session:

```sh
scripts/vm.sh ssh -- 'cd /repo && sudo env XDG_RUNTIME_DIR=/run/user/1000 WAYLAND_DISPLAY=$(basename $(ls /run/user/1000/wayland-* | grep -v lock | head -n1)) test/phase3.sh'
```

For optional manual inspection after the probe passes, run the script from an
interactive guest shell or console instead:

```sh
sudo --preserve-env=XDG_RUNTIME_DIR,WAYLAND_DISPLAY env AGORA_PHASE3_HOLD=1 test/phase3.sh
```

That leaves the shell UI and both agent webviews running until you press Enter
or interrupt the script, which is handy for manual clicking and shell checks
without making that manual pass a merge blocker.
