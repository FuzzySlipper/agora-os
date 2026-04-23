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
VFS_DAEMON_PID="$VFS_SOCK.pid"
VFS_LOG="$VM_DIR/virtiofsd.log"
CONSOLE_LOG="$VM_DIR/console.log"

SSH_PORT="${AGORA_VM_SSH_PORT:-2222}"
VM_MEM="${AGORA_VM_MEM:-4G}"
VM_CPUS="${AGORA_VM_CPUS:-4}"
DISK_SIZE="${AGORA_VM_DISK:-20G}"
NBD_DEV="${AGORA_VM_NBD:-/dev/nbd0}"
VM_GUI_DISPLAY="${AGORA_VM_GUI_DISPLAY:-gtk,gl=off}"
VM_GUI_GPU="${AGORA_VM_GUI_GPU:-virtio-vga}"
VIRTIOFSD_BIN="${AGORA_VM_VIRTIOFSD:-}"

die()  { echo "error: $*" >&2; exit 1; }
info() { echo ":: $*"; }

cleanup_build_mount() {
    local mnt="$1"
    local nbd_dev="$2"
    local rc=0

    if mountpoint -q "$mnt/boot" 2>/dev/null; then
        umount "$mnt/boot" || rc=1
    fi

    if mountpoint -q "$mnt" 2>/dev/null; then
        umount -R "$mnt" || rc=1
    fi

    if mountpoint -q "$mnt" 2>/dev/null; then
        # Disposable build mount: fall back to a lazy unmount if the normal
        # recursive unmount still sees the tree as busy.
        umount -l -R "$mnt" || rc=1
    fi

    if mountpoint -q "$mnt" 2>/dev/null; then
        return 1
    fi

    qemu-nbd --disconnect "$nbd_dev" 2>/dev/null || rc=1
    rmdir "$mnt" 2>/dev/null || true
    return "$rc"
}

find_qemu_pids() {
    ps -C qemu-system-x86_64 -o pid=,args= 2>/dev/null | awk \
        -v disk="file=$DISK," \
        -v pidfile="-pidfile $PID_FILE" \
        'index($0, "qemu-system-x86_64") && index($0, disk) && index($0, pidfile) { print $1 }' || true
}

find_virtiofsd_pids() {
    ps -C virtiofsd -o pid=,args= 2>/dev/null | awk \
        -v sock="--socket-path=$VFS_SOCK" \
        -v share="--shared-dir=$REPO_DIR" \
        'index($0, "virtiofsd") && index($0, sock) && index($0, share) { print $1 }' || true
}

resolve_virtiofsd() {
    if [[ -n "$VIRTIOFSD_BIN" ]]; then
        [[ -x "$VIRTIOFSD_BIN" ]] || die "AGORA_VM_VIRTIOFSD points to a non-executable path: $VIRTIOFSD_BIN"
        return
    fi

    if VIRTIOFSD_BIN=$(command -v virtiofsd 2>/dev/null); then
        return
    fi

    # Arch packages virtiofsd under /usr/lib/virtiofsd rather than on PATH.
    if [[ -x /usr/lib/virtiofsd ]]; then
        VIRTIOFSD_BIN=/usr/lib/virtiofsd
        return
    fi

    die "missing: virtiofsd (install the package or set AGORA_VM_VIRTIOFSD)"
}

is_running() {
    [[ -n "$(find_qemu_pids)" ]]
}

ssh_cmd() {
    ssh -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR \
        -i "$SSH_KEY" \
        -p "$SSH_PORT" \
        dev@127.0.0.1 "$@"
}

stop_virtiofsd() {
    local pid
    while read -r pid; do
        [[ -n "$pid" ]] || continue
        kill "$pid" 2>/dev/null || true
    done < <(find_virtiofsd_pids)

    rm -f "$VFS_PID" "$VFS_DAEMON_PID" "$VFS_SOCK"
}

cleanup_stale_virtiofsd_state() {
    for pid_file in "$VFS_PID" "$VFS_DAEMON_PID"; do
        [[ -f "$pid_file" ]] || continue

        local pid
        pid="$(cat "$pid_file" 2>/dev/null || true)"
        if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
            die "virtiofsd appears to already be running (pid $pid from $pid_file); use 'vm.sh stop' first"
        fi

        rm -f "$pid_file"
    done

    rm -f "$VFS_SOCK"
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
    chown "$caller_uid:$caller_gid" "$VM_DIR"
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
    trap 'cleanup_build_mount "'"$mnt"'" "'"$NBD_DEV"'"' EXIT

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
        mkinitcpio \
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
    echo "KEYMAP=us" > "$mnt/etc/vconsole.conf"

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

    # Ensure the kernel image and initramfs are materialized under /boot.
    arch-chroot "$mnt" mkinitcpio -P
    [[ -f "$mnt/boot/vmlinuz-linux" ]] || die "kernel image missing at $mnt/boot/vmlinuz-linux after mkinitcpio"
    [[ -f "$mnt/boot/initramfs-linux.img" ]] || die "initramfs missing at $mnt/boot/initramfs-linux.img after mkinitcpio"

    # GRUB (BIOS / i386-pc)
    info "Installing GRUB"
    arch-chroot "$mnt" grub-install --target=i386-pc "$NBD_DEV"
    arch-chroot "$mnt" grub-mkconfig -o /boot/grub/grub.cfg

    # Cleanup
    trap - EXIT
    cleanup_build_mount "$mnt" "$NBD_DEV" || die "failed to clean up VM build mount at $mnt"
    chown "$caller_uid:$caller_gid" "$VM_DIR"

    info "Build complete."
    info "Next: vm.sh start   # or: vm.sh gui for a graphical guest window"
    info "Then: vm.sh snap clean-base"
}

# ---------------------------------------------------------------------------
# start — boot with virtiofs, user-mode networking, SSH port-forward
# ---------------------------------------------------------------------------
cmd_start() {
    local boot_mode="${1:-headless}"
    local running_pids=""

    [[ -f "$DISK" ]] || die "no disk — run 'vm.sh build' first"
    running_pids="$(find_qemu_pids | paste -sd' ' -)"
    [[ -n "$running_pids" ]] && die "VM already running (pid(s): $running_pids)"

    command -v qemu-system-x86_64 >/dev/null || die "missing: qemu-system-x86_64"
    resolve_virtiofsd

    mkdir -p "$VM_DIR"
    : > "$VFS_LOG"

    # Clean up stale virtiofsd state from failed or interrupted launches.
    cleanup_stale_virtiofsd_state

    # Start virtiofsd (runs unprivileged with --sandbox none)
    info "Starting virtiofsd"
    nohup "$VIRTIOFSD_BIN" \
        --socket-path="$VFS_SOCK" \
        --shared-dir="$REPO_DIR" \
        --sandbox none \
        --log-level info \
        >"$VFS_LOG" 2>&1 < /dev/null &
    local virtiofsd_pid=$!
    echo "$virtiofsd_pid" > "$VFS_PID"

    # Wait for the socket to appear
    local i=0
    while [[ ! -S "$VFS_SOCK" ]] && (( i < 30 )); do
        if ! kill -0 "$virtiofsd_pid" 2>/dev/null; then
            rm -f "$VFS_PID" "$VFS_DAEMON_PID" "$VFS_SOCK"
            die "virtiofsd exited before creating $VFS_SOCK"
        fi
        sleep 0.1
        (( ++i ))
    done
    [[ -S "$VFS_SOCK" ]] || die "virtiofsd socket did not appear"

    local qemu_serial=("-serial" "file:$CONSOLE_LOG")
    local qemu_daemon=("-pidfile" "$PID_FILE" "-daemonize")

    if [[ "${FOREGROUND:-}" == "1" ]]; then
        qemu_serial=("-serial" "mon:stdio")
        qemu_daemon=()
    fi

    local qemu_display=("-display" "none")
    local qemu_graphics=()
    if [[ "$boot_mode" == "gui" ]]; then
        qemu_display=("-display" "$VM_GUI_DISPLAY")
        qemu_graphics=(
            "-device" "$VM_GUI_GPU"
            "-device" "qemu-xhci"
            "-device" "usb-tablet"
            "-device" "usb-kbd"
        )
    fi

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
        "${qemu_display[@]}" \
        "${qemu_graphics[@]}" \
        "${qemu_serial[@]}" \
        "${qemu_daemon[@]}"

    # In foreground mode, QEMU blocks — clean up virtiofsd when it exits.
    if [[ "${FOREGROUND:-}" == "1" ]]; then
        stop_virtiofsd
        return
    fi

    if [[ ! -f "$PID_FILE" ]]; then
        stop_virtiofsd
        die "QEMU exited before writing $PID_FILE; run 'vm.sh console' to see the boot error"
    fi

    local qemu_pid
    qemu_pid="$(cat "$PID_FILE")"
    if ! kill -0 "$qemu_pid" 2>/dev/null; then
        stop_virtiofsd
        rm -f "$PID_FILE"
        die "QEMU exited immediately after launch (pid $qemu_pid); run 'vm.sh console' to see the boot error"
    fi

    info "Waiting for SSH..."
    local i=0
    while ! ssh_cmd true 2>/dev/null; do
        if ! kill -0 "$qemu_pid" 2>/dev/null; then
            stop_virtiofsd
            rm -f "$PID_FILE"
            die "QEMU exited while waiting for SSH; run 'vm.sh console' to see the boot error"
        fi
        (( ++i ))
        (( i > 90 )) && die "SSH did not come up within 90s (check $CONSOLE_LOG)"
        sleep 1
    done

    info "VM ready. Use: vm.sh ssh"
}

# ---------------------------------------------------------------------------
# console — foreground boot with serial mon:stdio for first-boot debugging
# ---------------------------------------------------------------------------
cmd_console() {
    FOREGROUND=1 cmd_start "$@"
}

# ---------------------------------------------------------------------------
# gui — boot with a local QEMU window and virtual GPU for guest Wayfire work
# ---------------------------------------------------------------------------
cmd_gui() {
    cmd_start gui "$@"
}

# ---------------------------------------------------------------------------
# ssh — passwordless SSH into the VM (or run a one-shot command)
# ---------------------------------------------------------------------------
cmd_ssh() {
    is_running || die "VM is not running — use 'vm.sh start' or 'vm.sh gui'"
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
# phase2-deps — install guest-side Phase 2 compositor dependencies
# ---------------------------------------------------------------------------
cmd_phase2_deps() {
    is_running || die "VM is not running — use 'vm.sh start' or 'vm.sh gui'"
    ssh_cmd "sudo /repo/scripts/provision-phase2-vm.sh"
}

# ---------------------------------------------------------------------------
# stop — shut down QEMU and virtiofsd
# ---------------------------------------------------------------------------
cmd_stop() {
    local stopped=false
    local pid
    while read -r pid; do
        [[ -n "$pid" ]] || continue
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid"
            local i=0
            while kill -0 "$pid" 2>/dev/null && (( i < 30 )); do
                sleep 0.5
                (( ++i ))
            done
            kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null
            stopped=true
        fi
    done < <(find_qemu_pids)

    rm -f "$PID_FILE"

    stop_virtiofsd

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
    build|start|gui|ssh|console|snap|restore|phase2-deps|stop|destroy)
        cmd=${1//-/_}; shift; "cmd_$cmd" "$@" ;;
    *)
        cat >&2 <<'USAGE'
Usage: vm.sh <command> [args]

Commands:
  build              Create disk image and install Arch (requires sudo)
  start              Boot the VM headless (SSH on port 2222)
  gui                Boot the VM with a local graphics window for guest Wayfire
  console            Boot foreground with serial on stdio (first-boot debug)
  ssh [cmd]          SSH into the VM or run a one-shot command
  snap <name>        Take a qcow2 snapshot (VM must be stopped)
  restore <name>     Restore a snapshot (VM must be stopped)
  phase2-deps        Install Wayfire/plugin/test dependencies inside the guest
  stop               Shut down the VM
  destroy            Stop and delete all VM state

Environment overrides:
  AGORA_VM_SSH_PORT  SSH port forward (default: 2222)
  AGORA_VM_MEM       VM memory (default: 4G)
  AGORA_VM_CPUS      VM CPU count (default: 4)
  AGORA_VM_DISK      Disk size on build (default: 20G)
  AGORA_VM_DIR       State directory (default: .vm/)
  AGORA_VM_GUI_DISPLAY  QEMU display backend for 'gui' (default: gtk,gl=off)
  AGORA_VM_GUI_GPU      Virtual GPU device for 'gui' (default: virtio-vga)
  AGORA_VM_VIRTIOFSD    Path to virtiofsd binary (default: PATH, then /usr/lib/virtiofsd)
  AGORA_VM_NBD       NBD device for build (default: /dev/nbd0)
USAGE
        exit 1 ;;
esac
