# scripts/

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
scripts/vm.sh ssh               # open a shell inside the VM
scripts/vm.sh ssh 'cd /repo && go build ./cmd/...'  # one-shot command
scripts/vm.sh snap clean-base   # save a snapshot (VM must be stopped)
scripts/vm.sh restore clean-base
scripts/vm.sh stop
scripts/vm.sh destroy           # delete everything
```

### Snapshot strategy

| Snapshot | When to take | Purpose |
|---|---|---|
| `clean-base` | After first `build` + `start` + verify SSH works | Pristine Arch install |
| `phase1-deps` | After first `go build ./cmd/...` inside the VM | Fast test iteration |

Every test run restores from `phase1-deps`:

```sh
scripts/vm.sh restore phase1-deps
scripts/vm.sh start
scripts/vm.sh ssh -- 'sudo /repo/test/phase1.sh'
scripts/vm.sh stop
```

### Where state lives

All VM artifacts go to `.vm/` in the repo root (gitignored):

| File | Size | Purpose |
|---|---|---|
| `disk.qcow2` | ~5-8 GB after install, grows with snapshots | Disk image |
| `ssh_key` / `ssh_key.pub` | tiny | Passwordless SSH into the VM |
| `qemu.pid` | tiny | QEMU daemon PID |
| `console.log` | grows | Serial console output for debugging |

### Environment overrides

| Variable | Default | Description |
|---|---|---|
| `AGORA_VM_SSH_PORT` | `2222` | Host port forwarded to guest SSH |
| `AGORA_VM_MEM` | `4G` | VM memory |
| `AGORA_VM_CPUS` | `4` | VM CPU count |
| `AGORA_VM_DISK` | `20G` | Disk image size (only affects build) |
| `AGORA_VM_DIR` | `.vm/` | State directory |
| `AGORA_VM_NBD` | `/dev/nbd0` | NBD device used during build |

### Inside the VM

- User `dev` with NOPASSWD sudo, SSH key auth
- Root password is `root` (console emergency access only)
- Host repo mounted at `/repo` via virtiofs (r/w, live — no rsync)
- Arch stock kernel (`CONFIG_DEBUG_INFO_BTF=y` for future eBPF work)
- Pre-installed: `go`, `nftables`, `git`, `base-devel`, `openssh`
