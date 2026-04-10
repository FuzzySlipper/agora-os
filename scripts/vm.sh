#!/usr/bin/env bash
#
# vm.sh — bootstrap and manage the Agora OS dev VM (raw qemu + Arch).
# See scripts/README.md for host prerequisites.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VM_DIR="${AGORA_VM_DIR:-$REPO_DIR/.vm}"

DISK="$VM_DIR/disk.qcow2"
SSH_KEY="$VM_DIR/ssh_key"
PID_FILE="$VM_DIR/qemu.pid"
VFS_SOCK="$VM_DIR/virtiofsd.sock"
VFS_PID="$VM_DIR/virtiofsd.pid"
CONSOLE_LOG="$VM_DIR/console.log"

SSH_PORT="${AGORA_VM_SSH_PORT:-2222}"
VM_MEM="${AGORA_VM_MEM:-4G}"
VM_CPUS="${AGORA_VM_CPUS:-4}"
DISK_SIZE="${AGORA_VM_DISK:-20G}"
NBD_DEV="${AGORA_VM_NBD:-/dev/nbd0}"

die()  { echo "error: $*" >&2; exit 1; }
info() { echo ":: $*"; }

is_running() {
    [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null
}

ssh_cmd() {
    ssh -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR \
        -i "$SSH_KEY" \
        -p "$SSH_PORT" \
        dev@127.0.0.1 "$@"
}

# ---------------------------------------------------------------------------
# build — create disk image and install Arch via pacstrap
# ---------------------------------------------------------------------------
cmd_build() {
    [[ $EUID -eq 0 ]] || die "build requires root — run: sudo $0 build"
    [[ -f "$DISK" ]] && die "disk already exists at $DISK — run 'vm.sh destroy' first"

    for bin in qemu-img qemu-nbd pacstrap arch-chroot genfstab sfdisk; do
        command -v "$bin" >/dev/null || die "missing: $bin"
    done

    mkdir -p "$VM_DIR"

    # SSH keypair (owned by the invoking user, not root)
    local caller_uid="${SUDO_UID:-$(id -u)}"
    local caller_gid="${SUDO_GID:-$(id -g)}"
    if [[ ! -f "$SSH_KEY" ]]; then
        ssh-keygen -t ed25519 -f "$SSH_KEY" -N "" -C "agora-os-vm" -q
        chown "$caller_uid:$caller_gid" "$SSH_KEY" "$SSH_KEY.pub"
    fi

    info "Creating ${DISK_SIZE} qcow2 image"
    qemu-img create -f qcow2 "$DISK" "$DISK_SIZE" -q
    chown "$caller_uid:$caller_gid" "$DISK"

    # Attach via NBD
    modprobe nbd max_part=8 2>/dev/null || true
    qemu-nbd --connect="$NBD_DEV" "$DISK"

    local mnt="$VM_DIR/mnt"
    cleanup_build() {
        set +e
        mountpoint -q "$mnt/boot" 2>/dev/null && umount "$mnt/boot"
        mountpoint -q "$mnt" 2>/dev/null && umount -R "$mnt"
        qemu-nbd --disconnect "$NBD_DEV" 2>/dev/null
        rmdir "$mnt" 2>/dev/null
    }
    trap cleanup_build EXIT

    # Partition: MBR, single bootable ext4 partition
    info "Partitioning"
    sfdisk -q "$NBD_DEV" <<EOF
label: dos
type=83, bootable
EOF
    sleep 1
    partprobe "$NBD_DEV" 2>/dev/null || true

    info "Formatting"
    mkfs.ext4 -qL agoraos "${NBD_DEV}p1"

    mkdir -p "$mnt"
    mount "${NBD_DEV}p1" "$mnt"

    # Pacstrap
    info "Installing packages (this takes a few minutes)"
    pacstrap -K "$mnt" \
        base linux linux-firmware \
        grub \
        go nftables git base-devel openssh \
        sudo vim less which iproute2 procps-ng

    # fstab
    genfstab -U "$mnt" > "$mnt/etc/fstab"
    echo "repo    /repo    virtiofs    nofail    0 0" >> "$mnt/etc/fstab"
    mkdir -p "$mnt/repo"

    # Locale & timezone
    echo "en_US.UTF-8 UTF-8" > "$mnt/etc/locale.gen"
    arch-chroot "$mnt" locale-gen
    echo "LANG=en_US.UTF-8" > "$mnt/etc/locale.conf"
    ln -sf /usr/share/zoneinfo/UTC "$mnt/etc/localtime"

    # Hostname
    echo "agora-vm" > "$mnt/etc/hostname"

    # systemd-networkd: DHCP on all ethernet interfaces
    mkdir -p "$mnt/etc/systemd/network"
    cat > "$mnt/etc/systemd/network/20-wired.network" <<EOF
[Match]
Name=en*

[Network]
DHCP=yes
EOF

    # Enable services
    arch-chroot "$mnt" systemctl enable sshd systemd-networkd systemd-resolved

    # SSH daemon: key-only auth
    sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' "$mnt/etc/ssh/sshd_config"
    sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin no/' "$mnt/etc/ssh/sshd_config"

    # Non-root user with NOPASSWD sudo
    arch-chroot "$mnt" useradd -m -G wheel -s /bin/bash dev
    echo "dev ALL=(ALL:ALL) NOPASSWD: ALL" > "$mnt/etc/sudoers.d/dev"
    chmod 440 "$mnt/etc/sudoers.d/dev"

    # SSH public key for dev user
    local dotssh="$mnt/home/dev/.ssh"
    mkdir -p "$dotssh"
    cp "$SSH_KEY.pub" "$dotssh/authorized_keys"
    chmod 700 "$dotssh"
    chmod 600 "$dotssh/authorized_keys"
    arch-chroot "$mnt" chown -R dev:dev /home/dev/.ssh

    # Root password for emergency console access
    echo "root:root" | arch-chroot "$mnt" chpasswd

    # GRUB (BIOS / i386-pc)
    info "Installing GRUB"
    arch-chroot "$mnt" grub-install --target=i386-pc "$NBD_DEV"
    arch-chroot "$mnt" grub-mkconfig -o /boot/grub/grub.cfg

    # Cleanup
    trap - EXIT
    cleanup_build
    chown "$caller_uid:$caller_gid" "$VM_DIR"

    info "Build complete."
    info "Next: vm.sh start"
    info "Then: vm.sh snap clean-base"
}

# ---------------------------------------------------------------------------
# start — boot headless with virtiofs, user-mode networking, SSH port-forward
# ---------------------------------------------------------------------------
cmd_start() {
    [[ -f "$DISK" ]] || die "no disk — run 'vm.sh build' first"
    is_running && die "VM already running (pid $(cat "$PID_FILE"))"

    for bin in qemu-system-x86_64 virtiofsd; do
        command -v "$bin" >/dev/null || die "missing: $bin"
    done

    mkdir -p "$VM_DIR"

    # Clean up stale socket
    rm -f "$VFS_SOCK"

    # Start virtiofsd (runs unprivileged with --sandbox none)
    info "Starting virtiofsd"
    virtiofsd \
        --socket-path="$VFS_SOCK" \
        --shared-dir="$REPO_DIR" \
        --sandbox none \
        --log-level error &
    echo $! > "$VFS_PID"

    # Wait for the socket to appear
    local i=0
    while [[ ! -S "$VFS_SOCK" ]] && (( i < 30 )); do
        sleep 0.1; (( i++ ))
    done
    [[ -S "$VFS_SOCK" ]] || die "virtiofsd socket did not appear"

    info "Booting QEMU (mem=$VM_MEM cpus=$VM_CPUS ssh=127.0.0.1:$SSH_PORT)"
    qemu-system-x86_64 \
        -machine q35,accel=kvm,memory-backend=mem \
        -object "memory-backend-memfd,id=mem,share=on,size=$VM_MEM" \
        -cpu host \
        -m "$VM_MEM" \
        -smp "$VM_CPUS" \
        -drive "file=$DISK,format=qcow2,if=virtio,cache=writeback" \
        -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:${SSH_PORT}-:22" \
        -device virtio-net-pci,netdev=net0 \
        -chardev "socket,id=vfs,path=$VFS_SOCK" \
        -device vhost-user-fs-pci,chardev=vfs,tag=repo \
        -display none \
        -serial "file:$CONSOLE_LOG" \
        -pidfile "$PID_FILE" \
        -daemonize

    info "Waiting for SSH..."
    local i=0
    while ! ssh_cmd true 2>/dev/null; do
        (( i++ ))
        (( i > 90 )) && die "SSH did not come up within 90s (check $CONSOLE_LOG)"
        sleep 1
    done

    info "VM ready. Use: vm.sh ssh"
}

# ---------------------------------------------------------------------------
# ssh — passwordless SSH into the VM (or run a one-shot command)
# ---------------------------------------------------------------------------
cmd_ssh() {
    is_running || die "VM is not running — use 'vm.sh start'"
    ssh_cmd "$@"
}

# ---------------------------------------------------------------------------
# snap / restore — qcow2 internal snapshots (VM must be stopped)
# ---------------------------------------------------------------------------
cmd_snap() {
    [[ -n "${1:-}" ]] || die "usage: vm.sh snap <name>"
    is_running && die "stop the VM first"
    qemu-img snapshot -c "$1" "$DISK"
    info "Snapshot '$1' created."
}

cmd_restore() {
    [[ -n "${1:-}" ]] || die "usage: vm.sh restore <name>"
    is_running && die "stop the VM first"
    qemu-img snapshot -a "$1" "$DISK"
    info "Restored to '$1'."
}

# ---------------------------------------------------------------------------
# stop — shut down QEMU and virtiofsd
# ---------------------------------------------------------------------------
cmd_stop() {
    local stopped=false

    if [[ -f "$PID_FILE" ]]; then
        local pid
        pid=$(cat "$PID_FILE")
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid"
            local i=0
            while kill -0 "$pid" 2>/dev/null && (( i < 30 )); do
                sleep 0.5; (( i++ ))
            done
            kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null
            stopped=true
        fi
        rm -f "$PID_FILE"
    fi

    if [[ -f "$VFS_PID" ]]; then
        kill "$(cat "$VFS_PID")" 2>/dev/null || true
        rm -f "$VFS_PID" "$VFS_SOCK"
    fi

    $stopped && info "VM stopped." || info "VM was not running."
}

# ---------------------------------------------------------------------------
# destroy — stop + delete all VM state
# ---------------------------------------------------------------------------
cmd_destroy() {
    cmd_stop 2>/dev/null || true
    rm -rf "$VM_DIR"
    info "VM destroyed."
}

# ---------------------------------------------------------------------------
# dispatch
# ---------------------------------------------------------------------------
case "${1:-}" in
    build|start|ssh|snap|restore|stop|destroy)
        cmd="$1"; shift; "cmd_$cmd" "$@" ;;
    *)
        cat >&2 <<'USAGE'
Usage: vm.sh <command> [args]

Commands:
  build              Create disk image and install Arch (requires sudo)
  start              Boot the VM headless (SSH on port 2222)
  ssh [cmd]          SSH into the VM or run a one-shot command
  snap <name>        Take a qcow2 snapshot (VM must be stopped)
  restore <name>     Restore a snapshot (VM must be stopped)
  stop               Shut down the VM
  destroy            Stop and delete all VM state

Environment overrides:
  AGORA_VM_SSH_PORT  SSH port forward (default: 2222)
  AGORA_VM_MEM       VM memory (default: 4G)
  AGORA_VM_CPUS      VM CPU count (default: 4)
  AGORA_VM_DISK      Disk size on build (default: 20G)
  AGORA_VM_DIR       State directory (default: .vm/)
  AGORA_VM_NBD       NBD device for build (default: /dev/nbd0)
USAGE
        exit 1 ;;
esac
