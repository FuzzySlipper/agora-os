# Test Guide

Phase 1 testing is **VM-first**. Changes that depend on kernel attribution,
cross-uid behavior, privileged paths, or host services are not considered
validated based on local sandbox runs alone.

Phase 2 testing is **Wayfire-session-first**. The authoritative proof now needs
a live compositor session with the Agora Wayfire plugin loaded, because the
important behavior is in-process input mediation rather than just Go-side state.

## Use the VM for authoritative Phase 1 checks

- Validate `SO_PEERCRED` behavior in the disposable VM.
- Validate Unix sockets under `/run/agent-os` in the VM.
- Validate `useradd`, per-uid process execution, and `systemd-run` in the VM.
- Validate nftables, fanotify, and writes under `/var/log/agent-os` in the VM.
- Prefer reproducible integration scripts under `test/` for Phase 1 acceptance.

The local sandbox may block socket creation or other privileged operations, so
`go test ./...` on the host can produce false negatives for Phase 1 system
behavior.

## Use the graphical VM guest for authoritative Phase 2 checks

- Restore the `phase2-deps` snapshot and boot the VM with `scripts/vm.sh gui`.
- Run `test/phase2.sh` inside a root-owned Wayfire session with the
  `agora-bridge` plugin loaded.
- Use a root-owned Wayfire session under `/run/user/0` so the script can
  launch both the human-owned and agent-owned Wayland clients.
- Install one supported native Wayland terminal client: `foot`,
  `weston-terminal`, `alacritty`, or `kitty`.
- Install `wtype` so the script can generate a real keyboard event for the
  plugin deny/grant path.
- Treat the guest Wayfire session as disposable: `test/phase2.sh`
  temporarily relaxes the compositor socket permissions so a spawned agent uid
  can connect to the session.

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

Then, in the guest console, launch the compositor session:

```sh
sudo systemctl start seatd
sudo install -d -m 0700 /run/user/0
sudo dbus-run-session env XDG_RUNTIME_DIR=/run/user/0 wayfire
```

From another host terminal, run the Phase 2 proof against that guest session:

```sh
scripts/vm.sh ssh -- "cd /repo && sudo env XDG_RUNTIME_DIR=/run/user/0 WAYLAND_DISPLAY=$(basename $(ls /run/user/0/wayland-* | head -n1)) test/phase2.sh"
```
