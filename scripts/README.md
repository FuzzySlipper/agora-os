# scripts/

## asha_agora_control_proof.py — ASHA camera-control compositor proof

Drives the cross-project ASHA first-person camera scenario through the deployed
Agora compositor stack. It runs `npm run camera:agora-control` in `asha-demo`,
creates a named compositor session, launches the generated page with
`webview-launcher`, injects the public keyboard controls, captures before/after
surface PNGs, destroys the temporary compositor session by default, and writes
`harness/out/camera-agora-control/latest/agora-control-proof.json`
in the ASHA demo output directory.

```sh
python3 scripts/asha_agora_control_proof.py --self-test
python3 scripts/asha_agora_control_proof.py
```

The live command expects `/usr/local/bin/compositorctl`, `/usr/local/bin/webview-launcher`,
and `/home/dev/asha-demo` by default. It registers an exit cleanup handler after
session creation, so ordinary failure paths also attempt to destroy the temporary
session. The launcher starts a loopback app-command readiness endpoint for the
webview helper, and the proof fails closed unless typed page markers confirm the
ASHA proof title, scenario id, step number, projection hash, and post-input state
advancement. This distinguishes “surface mapped” from “proof page actually
ready” and catches visible-but-wrong captures. It does not define ASHA camera
semantics; it only launches, drives, captures, and classifies the public ASHA
demo surface.

## vm.sh — dev VM wrapper

Disposable Arch VM driven by raw `qemu-system-x86_64`. No libvirt, no
virt-manager — every step is a single shell command that an agent can
read and reason about.

### Host prerequisites

```sh
sudo pacman -S qemu-full virtiofsd arch-install-scripts
```

Your user needs to be in the `kvm` group for hardware acceleration:

```sh
sudo usermod -aG kvm $USER   # then re-login
```

### Quick start

```sh
sudo scripts/vm.sh build       # one-time: install Arch to qcow2 (~5 min)
scripts/vm.sh start             # boot headless, SSH on port 2222
scripts/vm.sh gui               # boot with a local graphics window
scripts/vm.sh gui --no-ssh-wait # boot even when you expect SSH/boot to be broken
scripts/vm.sh ssh               # open a shell inside the VM
scripts/vm.sh ssh 'cd /repo && go build ./cmd/...'  # one-shot command
scripts/vm.sh status            # show QEMU/QMP/QGA/SSH reachability
scripts/vm.sh screenshot        # capture the QEMU framebuffer to .vm/screenshots/latest.png
scripts/vm.sh diag              # collect host, QMP/QGA, screenshot, and guest logs
scripts/vm.sh phase2-deps      # install Wayfire/plugin/webview test deps inside the guest
scripts/vm.sh snap clean-base   # save a snapshot (VM must be stopped)
scripts/vm.sh restore clean-base
scripts/vm.sh stop
scripts/vm.sh destroy           # delete everything
```

### Snapshot strategy

| Snapshot | When to take | Purpose |
|---|---|---|
| `clean-base` | After first `build` + `start` + verify SSH works | Pristine Arch install |
| `phase1-deps` | After first `go build ./cmd/...` inside the VM | Fast Phase 1 test iteration |
| `phase2-deps` | After `scripts/vm.sh phase2-deps` + plugin build sanity-check | Reusable Wayfire/plugin/webview dev environment |

Phase 1 restores from `phase1-deps`:

```sh
scripts/vm.sh restore phase1-deps
scripts/vm.sh start
scripts/vm.sh ssh -- 'sudo /repo/test/phase1.sh'
scripts/vm.sh stop
```

Phase 2 and Phase 3 guest setup use the same pattern:

```sh
scripts/vm.sh start
scripts/vm.sh phase2-deps
scripts/vm.sh ssh -- 'meson setup /tmp/agora-wayfire-plugin-build /repo/compositor/wayfire-plugin && meson compile -C /tmp/agora-wayfire-plugin-build && sudo meson install -C /tmp/agora-wayfire-plugin-build'
scripts/vm.sh stop
scripts/vm.sh snap phase2-deps
```

Use a guest-local build directory such as `/tmp/agora-wayfire-plugin-build`
instead of `/repo/compositor/wayfire-plugin/build`. The `/repo` tree is mounted
into the guest via `virtiofs`, and Meson can trip over sub-second host/guest
timestamp skew if the build dir lives on that shared filesystem.

For live compositor work, restore `phase2-deps` and boot the graphical guest:

```sh
scripts/vm.sh restore phase2-deps
scripts/vm.sh gui
```

That opens a local QEMU window with a virtual GPU while keeping the host
workflow unprivileged. Current Wayfire refuses to run as root, so the
`phase2-deps` provisioner prepares a dev-owned session config at
`/home/dev/.config/wayfire-agora.ini` and adds `dev` to the required
seat/device groups. Start that compositor session when you need to run
`test/phase2.sh` or `test/phase3.sh`:

```sh
scripts/vm.sh ssh -- 'sudo systemctl start seatd && sudo install -d -o dev -g dev -m 0700 /run/user/1000 && sudo openvt -c 2 -f -s -- runuser -u dev -- sh -lc "export XDG_RUNTIME_DIR=/run/user/1000; export WLR_RENDERER_ALLOW_SOFTWARE=1; exec dbus-run-session -- wayfire -c /home/dev/.config/wayfire-agora.ini >/tmp/wayfire.log 2>&1"'
```

Use `start` for Phase 1 and headless guest setup; use `gui` when the task needs
a real guest compositor session. If GTK is unavailable on the host, override
`AGORA_VM_GUI_DISPLAY` to another supported backend such as `sdl`.

When the guest is broken before SSH comes up, boot with:

```sh
scripts/vm.sh gui --no-ssh-wait
```

Then use host-side diagnostics instead of manually transcribing the GUI state:

```sh
scripts/vm.sh status
scripts/vm.sh screenshot        # .vm/screenshots/latest.png
scripts/vm.sh console-log 300
scripts/vm.sh diag              # .vm/diag/<timestamp>/
scripts/vm.sh sendkey ctrl-alt-f2
scripts/vm.sh serial            # interactive serial console, if the guest reaches getty
```

The VM exposes local-only QMP (`.vm/qmp.sock`), qemu-guest-agent
(`.vm/qga.sock`), and serial (`.vm/serial.sock`) sockets while it is running.
Newly built disks mirror GRUB/kernel output to `ttyS0`, enable
`serial-getty@ttyS0.service`, and enable `qemu-guest-agent.service` so these
channels can diagnose failures before SSH is available.

### Where state lives

All VM artifacts go to `.vm/` in the repo root (gitignored):

| File | Size | Purpose |
|---|---|---|
| `disk.qcow2` | ~5-8 GB after install, grows with snapshots | Disk image |
| `ssh_key` / `ssh_key.pub` | tiny | Passwordless SSH into the VM |
| `qemu.pid` | tiny | QEMU daemon PID |
| `console.log` | grows | Serial console output for debugging |
| `qmp.sock` | tiny | QMP control socket while QEMU is running |
| `qga.sock` | tiny | qemu-guest-agent socket while QEMU is running and the guest agent is alive |
| `serial.sock` | tiny | Interactive serial console socket while QEMU is running |
| `screenshots/` | small | QEMU framebuffer captures from `vm.sh screenshot` |
| `diag/` | varies | Diagnostic bundles from `vm.sh diag` |

### Environment overrides

| Variable | Default | Description |
|---|---|---|
| `AGORA_VM_SSH_PORT` | `2222` | Host port forwarded to guest SSH |
| `AGORA_VM_MEM` | `4G` | VM memory |
| `AGORA_VM_CPUS` | `4` | VM CPU count |
| `AGORA_VM_DISK` | `20G` | Disk image size (only affects build) |
| `AGORA_VM_DIR` | `.vm/` | State directory |
| `AGORA_VM_GUI_DISPLAY` | `gtk,gl=off` | QEMU display backend for `vm.sh gui` |
| `AGORA_VM_GUI_GPU` | `virtio-vga` | Virtual GPU device for `vm.sh gui` |
| `AGORA_VM_VIRTIOFSD` | unset | Override path to `virtiofsd` if the package is installed outside `PATH` (for example `/usr/lib/virtiofsd` on Arch) |
| `AGORA_VM_NBD` | `/dev/nbd0` | NBD device used during build |

### Inside the VM

- User `dev` with NOPASSWD sudo, SSH key auth
- Root password is `root` (console emergency access only)
- Host repo mounted at `/repo` via virtiofs (r/w, live — no rsync)
- Arch stock kernel (`CONFIG_DEBUG_INFO_BTF=y` for future eBPF work)
- Pre-installed: `go`, `nftables`, `git`, `base-devel`, `openssh`, `qemu-guest-agent`
