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
QMP_SOCK="$VM_DIR/qmp.sock"
QGA_SOCK="$VM_DIR/qga.sock"
SERIAL_SOCK="$VM_DIR/serial.sock"
SCREENSHOT_DIR="$VM_DIR/screenshots"
DIAG_DIR="$VM_DIR/diag"

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
    local _attempt

    sync -f "$mnt" 2>/dev/null || sync

    if mountpoint -q "$mnt/boot" 2>/dev/null; then
        for _attempt in 1 2 3; do
            umount "$mnt/boot" 2>/dev/null && break
            sleep 1
        done
    fi

    if mountpoint -q "$mnt" 2>/dev/null; then
        for _attempt in 1 2 3; do
            umount -R "$mnt" 2>/dev/null && break
            sleep 1
        done
    fi

    if mountpoint -q "$mnt" 2>/dev/null; then
        # Disposable build mount: fall back to a lazy unmount if the normal
        # recursive unmount still sees the tree as busy.
        umount -l -R "$mnt" || return 1
    fi

    if mountpoint -q "$mnt" 2>/dev/null; then
        return 1
    fi

    qemu-nbd --disconnect "$nbd_dev" 2>/dev/null || return 1
    rmdir "$mnt" 2>/dev/null || true
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
    ssh -F /dev/null \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR \
        -i "$SSH_KEY" \
        -p "$SSH_PORT" \
        dev@127.0.0.1 "$@"
}

ssh_quick_cmd() {
    ssh -F /dev/null \
        -o BatchMode=yes \
        -o ConnectTimeout=3 \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR \
        -i "$SSH_KEY" \
        -p "$SSH_PORT" \
        dev@127.0.0.1 "$@"
}

require_python3() {
    command -v python3 >/dev/null || die "missing: python3"
}

qmp_cmd() {
    local payload="$1"
    [[ -S "$QMP_SOCK" ]] || die "QMP socket not found at $QMP_SOCK (is the VM running?)"
    require_python3

    python3 - "$QMP_SOCK" "$payload" <<'PY'
import json
import socket
import sys

sock_path, payload_raw = sys.argv[1], sys.argv[2]
try:
    payload = json.loads(payload_raw)
except json.JSONDecodeError as exc:
    print(f"invalid QMP JSON: {exc}", file=sys.stderr)
    sys.exit(2)
if not isinstance(payload, dict):
    print("invalid QMP JSON: top-level value must be an object", file=sys.stderr)
    sys.exit(2)
request_id = payload.setdefault("id", "agora-command")

def read_msg(stream):
    line = stream.readline()
    if not line:
        raise RuntimeError("QMP socket closed")
    return json.loads(line.decode("utf-8"))

def write_msg(stream, msg):
    stream.write(json.dumps(msg).encode("utf-8") + b"\r\n")

try:
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
        sock.settimeout(10)
        sock.connect(sock_path)
        stream = sock.makefile("rwb", buffering=0)

        # Greeting.
        read_msg(stream)

        write_msg(stream, {"execute": "qmp_capabilities", "id": "agora-caps"})
        while True:
            msg = read_msg(stream)
            if msg.get("id") == "agora-caps":
                if "error" in msg:
                    print(json.dumps(msg, indent=2, sort_keys=True))
                    sys.exit(1)
                break

        write_msg(stream, payload)
        while True:
            msg = read_msg(stream)
            if msg.get("id") == request_id:
                print(json.dumps(msg, indent=2, sort_keys=True))
                sys.exit(1 if "error" in msg else 0)
except Exception as exc:
    print(f"QMP error: {exc}", file=sys.stderr)
    sys.exit(1)
PY
}

qmp_hmp_cmd() {
    local command_line="$1"
    local payload
    require_python3
    payload="$(python3 - "$command_line" <<'PY'
import json
import sys
print(json.dumps({
    "execute": "human-monitor-command",
    "arguments": {"command-line": sys.argv[1]},
}))
PY
)"
    qmp_cmd "$payload"
}

qga_cmd() {
    local payload="$1"
    [[ -S "$QGA_SOCK" ]] || die "QEMU guest-agent socket not found at $QGA_SOCK (is the VM running?)"
    require_python3

    python3 - "$QGA_SOCK" "$payload" <<'PY'
import json
import random
import socket
import sys

sock_path, payload_raw = sys.argv[1], sys.argv[2]
try:
    payload = json.loads(payload_raw)
except json.JSONDecodeError as exc:
    print(f"invalid QGA JSON: {exc}", file=sys.stderr)
    sys.exit(2)
if not isinstance(payload, dict):
    print("invalid QGA JSON: top-level value must be an object", file=sys.stderr)
    sys.exit(2)
request_id = payload.setdefault("id", "agora-command")

def read_msg(stream):
    line = stream.readline()
    if not line:
        raise RuntimeError("QGA socket closed")
    return json.loads(line.decode("utf-8"))

def write_msg(stream, msg):
    stream.write(json.dumps(msg).encode("utf-8") + b"\n")

try:
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
        sock.settimeout(10)
        sock.connect(sock_path)
        stream = sock.makefile("rwb", buffering=0)

        sync_value = random.randint(1, 2**31 - 1)
        write_msg(stream, {
            "execute": "guest-sync",
            "arguments": {"id": sync_value},
            "id": "agora-sync",
        })
        while True:
            msg = read_msg(stream)
            if msg.get("id") == "agora-sync":
                if "error" in msg:
                    print(json.dumps(msg, indent=2, sort_keys=True))
                    sys.exit(1)
                break

        write_msg(stream, payload)
        while True:
            msg = read_msg(stream)
            if msg.get("id") == request_id:
                print(json.dumps(msg, indent=2, sort_keys=True))
                sys.exit(1 if "error" in msg else 0)
except Exception as exc:
    print(f"QGA error: {exc}", file=sys.stderr)
    sys.exit(1)
PY
}

qga_exec_cmd() {
    local shell_command="$1"
    [[ -S "$QGA_SOCK" ]] || die "QEMU guest-agent socket not found at $QGA_SOCK (is the VM running?)"
    require_python3

    python3 - "$QGA_SOCK" "$shell_command" <<'PY'
import base64
import json
import random
import socket
import sys
import time

sock_path, shell_command = sys.argv[1], sys.argv[2]

def read_msg(stream):
    line = stream.readline()
    if not line:
        raise RuntimeError("QGA socket closed")
    return json.loads(line.decode("utf-8"))

def write_msg(stream, msg):
    stream.write(json.dumps(msg).encode("utf-8") + b"\n")

def request(stream, msg, request_id):
    write_msg(stream, msg)
    while True:
        response = read_msg(stream)
        if response.get("id") == request_id:
            if "error" in response:
                print(json.dumps(response, indent=2, sort_keys=True), file=sys.stderr)
                sys.exit(1)
            return response.get("return")

try:
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
        sock.settimeout(10)
        sock.connect(sock_path)
        stream = sock.makefile("rwb", buffering=0)

        sync_value = random.randint(1, 2**31 - 1)
        request(stream, {
            "execute": "guest-sync",
            "arguments": {"id": sync_value},
            "id": "agora-sync",
        }, "agora-sync")

        started = request(stream, {
            "execute": "guest-exec",
            "arguments": {
                "path": "/usr/bin/env",
                "arg": ["bash", "-lc", shell_command],
                "capture-output": True,
            },
            "id": "agora-exec",
        }, "agora-exec")
        pid = started["pid"]

        deadline = time.monotonic() + 60
        status = None
        while time.monotonic() < deadline:
            status = request(stream, {
                "execute": "guest-exec-status",
                "arguments": {"pid": pid},
                "id": "agora-exec-status",
            }, "agora-exec-status")
            if status.get("exited"):
                break
            time.sleep(0.25)
        else:
            print("QGA command timed out after 60s", file=sys.stderr)
            sys.exit(124)

        if status.get("out-data"):
            sys.stdout.buffer.write(base64.b64decode(status["out-data"]))
        if status.get("err-data"):
            sys.stderr.buffer.write(base64.b64decode(status["err-data"]))
        if status.get("out-truncated"):
            print("\n[stdout truncated by qemu-guest-agent]", file=sys.stderr)
        if status.get("err-truncated"):
            print("\n[stderr truncated by qemu-guest-agent]", file=sys.stderr)
        exit_code = status.get("exitcode")
        if exit_code is None:
            if status.get("signal"):
                print(f"\n[guest command terminated by signal {status['signal']}]", file=sys.stderr)
            exit_code = 1
        exit_code = int(exit_code)
        sys.exit(exit_code if 0 <= exit_code < 256 else 1)
except Exception as exc:
    print(f"QGA exec error: {exc}", file=sys.stderr)
    sys.exit(1)
PY
}

ppm_to_png() {
    local ppm="$1"
    local png="$2"
    require_python3

    python3 - "$ppm" "$png" <<'PY'
import os
import struct
import sys
import zlib

ppm_path, png_path = sys.argv[1], sys.argv[2]

def read_token(stream):
    while True:
        ch = stream.read(1)
        if not ch:
            raise ValueError("unexpected EOF in PPM header")
        if ch == b"#":
            stream.readline()
            continue
        if ch.isspace():
            continue
        token = bytearray(ch)
        while True:
            ch = stream.read(1)
            if not ch or ch.isspace():
                break
            token.extend(ch)
        return bytes(token)

def chunk(kind, payload):
    return (
        struct.pack(">I", len(payload))
        + kind
        + payload
        + struct.pack(">I", zlib.crc32(kind + payload) & 0xFFFFFFFF)
    )

with open(ppm_path, "rb") as stream:
    magic = read_token(stream)
    if magic != b"P6":
        raise ValueError(f"unsupported PPM magic {magic!r}; expected P6")
    width = int(read_token(stream))
    height = int(read_token(stream))
    max_value = int(read_token(stream))
    if max_value != 255:
        raise ValueError(f"unsupported PPM max value {max_value}; expected 255")
    data = stream.read(width * height * 3)
    if len(data) != width * height * 3:
        raise ValueError("PPM pixel data is truncated")

raw_rows = b"".join(
    b"\x00" + data[y * width * 3:(y + 1) * width * 3]
    for y in range(height)
)
png = (
    b"\x89PNG\r\n\x1a\n"
    + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
    + chunk(b"IDAT", zlib.compress(raw_rows, 9))
    + chunk(b"IEND", b"")
)
os.makedirs(os.path.dirname(png_path) or ".", exist_ok=True)
with open(png_path, "wb") as stream:
    stream.write(png)
PY
}

absolute_path() {
    local path="$1"
    local dir
    local base
    dir="$(dirname "$path")"
    base="$(basename "$path")"
    mkdir -p "$dir"
    dir="$(cd "$dir" && pwd)"
    printf '%s/%s\n' "$dir" "$base"
}

cleanup_stale_qemu_control_state() {
    rm -f "$QMP_SOCK" "$QGA_SOCK" "$SERIAL_SOCK"
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
        go nftables git base-devel openssh qemu-guest-agent \
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
    arch-chroot "$mnt" systemctl enable \
        sshd \
        systemd-networkd \
        systemd-resolved \
        serial-getty@ttyS0.service \
        qemu-guest-agent.service
    ln -sf /run/systemd/resolve/resolv.conf "$mnt/etc/resolv.conf"

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

    # Mirror GRUB, kernel logs, and an emergency login to the first serial
    # device. The GUI remains usable via console=tty0, but boot failures are
    # also visible through .vm/console.log and vm.sh serial.
    cat >> "$mnt/etc/default/grub" <<'EOF'

# Agora OS VM debugging: mirror boot output to serial for agent diagnostics.
GRUB_SERIAL_COMMAND="serial --unit=0 --speed=115200 --word=8 --parity=no --stop=1"
GRUB_TERMINAL_INPUT="console serial"
GRUB_TERMINAL_OUTPUT="console serial"
GRUB_CMDLINE_LINUX="console=tty0 console=ttyS0,115200n8"
EOF

    # Ensure the kernel image and initramfs are materialized under /boot.
    arch-chroot "$mnt" mkinitcpio -P
    [[ -f "$mnt/boot/vmlinuz-linux" ]] || die "kernel image missing at $mnt/boot/vmlinuz-linux after mkinitcpio"
    [[ -f "$mnt/boot/initramfs-linux.img" ]] || die "initramfs missing at $mnt/boot/initramfs-linux.img after mkinitcpio"

    # GRUB (BIOS / i386-pc)
    info "Installing GRUB"
    arch-chroot "$mnt" grub-install --target=i386-pc "$NBD_DEV"
    arch-chroot "$mnt" grub-mkconfig -o /boot/grub/grub.cfg
    if [[ -s "$mnt/boot/grub/grub.cfg.new" ]]; then
        # On current Arch/grub in this chrooted NBD build, grub-mkconfig can
        # report success while leaving the validated output at grub.cfg.new.
        # Install that file explicitly from the host side so the VM never boots
        # into the GRUB rescue shell just because /boot/grub/grub.cfg is absent.
        info "grub-mkconfig left grub.cfg.new; validating and installing it as grub.cfg"
        arch-chroot "$mnt" grub-script-check /boot/grub/grub.cfg.new
        install -m 0600 "$mnt/boot/grub/grub.cfg.new" "$mnt/boot/grub/grub.cfg"
        rm -f "$mnt/boot/grub/grub.cfg.new"
    fi
    [[ -s "$mnt/boot/grub/grub.cfg" ]] || die "GRUB config missing after grub-mkconfig; check $mnt/boot/grub/grub.cfg.new and build output"
    arch-chroot "$mnt" grub-script-check /boot/grub/grub.cfg
    sync -f "$mnt/boot/grub/grub.cfg" 2>/dev/null || sync

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
    local boot_mode="headless"
    local wait_for_ssh=true
    local running_pids=""

    if [[ "${1:-}" == "gui" ]]; then
        boot_mode="gui"
        shift
    fi

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --no-ssh-wait|--no-wait)
                wait_for_ssh=false
                shift ;;
            *)
                die "usage: vm.sh start|gui [--no-ssh-wait]" ;;
        esac
    done

    [[ -f "$DISK" ]] || die "no disk — run 'vm.sh build' first"
    running_pids="$(find_qemu_pids | paste -sd' ' -)"
    [[ -n "$running_pids" ]] && die "VM already running (pid(s): $running_pids)"

    command -v qemu-system-x86_64 >/dev/null || die "missing: qemu-system-x86_64"
    command -v setsid >/dev/null || die "missing: setsid (install util-linux)"
    resolve_virtiofsd

    mkdir -p "$VM_DIR"
    : > "$VFS_LOG"

    # Clean up stale socket state from failed or interrupted launches.
    cleanup_stale_virtiofsd_state
    cleanup_stale_qemu_control_state

    # Start virtiofsd (runs unprivileged with --sandbox none).
    #
    # A plain backgrounded nohup is not detached enough here: on this host,
    # virtiofsd would accept QEMU's connection and then disappear as soon as
    # the vm.sh process exited, leaving the guest with a mounted-but-hung
    # /repo. Launch it in its own session so it survives the parent shell.
    info "Starting virtiofsd"
    setsid "$VIRTIOFSD_BIN" \
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

    local qemu_serial=(
        "-chardev" "socket,id=serial0,path=$SERIAL_SOCK,server=on,wait=off,logfile=$CONSOLE_LOG,logappend=off"
        "-serial" "chardev:serial0"
    )
    local qemu_control=(
        "-qmp" "unix:$QMP_SOCK,server=on,wait=off"
        "-device" "virtio-serial-pci"
        "-chardev" "socket,id=qga0,path=$QGA_SOCK,server=on,wait=off"
        "-device" "virtserialport,chardev=qga0,name=org.qemu.guest_agent.0"
    )
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
        "${qemu_control[@]}" \
        "${qemu_display[@]}" \
        "${qemu_graphics[@]}" \
        "${qemu_serial[@]}" \
        "${qemu_daemon[@]}"

    # In foreground mode, QEMU blocks — clean up virtiofsd when it exits.
    if [[ "${FOREGROUND:-}" == "1" ]]; then
        stop_virtiofsd
        cleanup_stale_qemu_control_state
        return
    fi

    if [[ ! -f "$PID_FILE" ]]; then
        stop_virtiofsd
        cleanup_stale_qemu_control_state
        die "QEMU exited before writing $PID_FILE; run 'vm.sh console' to see the boot error"
    fi

    local qemu_pid
    qemu_pid="$(cat "$PID_FILE")"
    if ! kill -0 "$qemu_pid" 2>/dev/null; then
        stop_virtiofsd
        cleanup_stale_qemu_control_state
        rm -f "$PID_FILE"
        die "QEMU exited immediately after launch (pid $qemu_pid); run 'vm.sh console' to see the boot error"
    fi

    if [[ "$wait_for_ssh" == "false" ]]; then
        info "VM launched without waiting for SSH."
        info "Control sockets: QMP=$QMP_SOCK QGA=$QGA_SOCK serial=$SERIAL_SOCK"
        info "Use: vm.sh status | vm.sh screenshot | vm.sh console-log"
        return
    fi

    info "Waiting for SSH..."
    local i=0
    while ! ssh_cmd true 2>/dev/null; do
        if ! kill -0 "$qemu_pid" 2>/dev/null; then
            stop_virtiofsd
            cleanup_stale_qemu_control_state
            rm -f "$PID_FILE"
            die "QEMU exited while waiting for SSH; run 'vm.sh console' to see the boot error"
        fi
        (( ++i ))
        (( i > 90 )) && die "SSH did not come up within 90s (check $CONSOLE_LOG; use vm.sh status/screenshot/diag while the VM is still running)"
        sleep 1
    done

    if ! timeout 5 ssh \
        -F /dev/null \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR \
        -i "$SSH_KEY" \
        -p "$SSH_PORT" \
        dev@127.0.0.1 \
        "ls /repo >/dev/null"; then
        stop_virtiofsd
        kill "$qemu_pid" 2>/dev/null || true
        cleanup_stale_qemu_control_state
        rm -f "$PID_FILE"
        die "/repo virtiofs mount is not responding"
    fi

    info "VM ready. Use: vm.sh ssh"
    info "Debug helpers: vm.sh status | vm.sh screenshot | vm.sh diag"
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
# status / diagnostic helpers — work even when guest SSH is unavailable
# ---------------------------------------------------------------------------
cmd_status() {
    local qemu_pids
    local virtiofsd_pids

    echo "VM dir: $VM_DIR"
    echo "Disk: $DISK$([[ -f "$DISK" ]] && echo ' (present)' || echo ' (missing)')"
    echo "SSH forward: 127.0.0.1:$SSH_PORT -> guest:22"

    qemu_pids="$(find_qemu_pids | paste -sd' ' -)"
    if [[ -n "$qemu_pids" ]]; then
        echo "QEMU: running (pid(s): $qemu_pids)"
    else
        echo "QEMU: stopped"
    fi

    virtiofsd_pids="$(find_virtiofsd_pids | paste -sd' ' -)"
    if [[ -n "$virtiofsd_pids" ]]; then
        echo "virtiofsd: running (pid(s): $virtiofsd_pids)"
    else
        echo "virtiofsd: stopped"
    fi

    echo "QMP socket: $QMP_SOCK$([[ -S "$QMP_SOCK" ]] && echo ' (listening)' || echo ' (missing)')"
    echo "QGA socket: $QGA_SOCK$([[ -S "$QGA_SOCK" ]] && echo ' (listening)' || echo ' (missing)')"
    echo "Serial socket: $SERIAL_SOCK$([[ -S "$SERIAL_SOCK" ]] && echo ' (listening)' || echo ' (missing)')"
    echo "Console log: $CONSOLE_LOG$([[ -f "$CONSOLE_LOG" ]] && echo ' (present)' || echo ' (missing)')"

    if [[ -S "$QMP_SOCK" ]]; then
        echo
        echo "QMP query-status:"
        qmp_cmd '{"execute":"query-status"}' || true
    fi

    echo
    if ssh_quick_cmd true 2>/dev/null; then
        echo "SSH: reachable"
        ssh_quick_cmd 'printf "hostname: "; uname -n; printf "uptime: "; uptime; printf "cmdline: "; cat /proc/cmdline' || true
    else
        echo "SSH: not reachable"
    fi
}

cmd_console_log() {
    local lines="${1:-200}"
    [[ "$lines" =~ ^[0-9]+$ ]] || die "usage: vm.sh console-log [lines]"
    [[ -f "$CONSOLE_LOG" ]] || die "console log not found at $CONSOLE_LOG"
    tail -n "$lines" "$CONSOLE_LOG"
}

cmd_journal() {
    is_running || die "VM is not running — use 'vm.sh start' or 'vm.sh gui'"
    ssh_cmd journalctl -b --no-pager "$@"
}

cmd_qmp() {
    [[ $# -eq 1 ]] || die "usage: vm.sh qmp '<json-command>'"
    qmp_cmd "$1"
}

cmd_hmp() {
    [[ $# -gt 0 ]] || die "usage: vm.sh hmp <human-monitor-command>"
    qmp_hmp_cmd "$*"
}

cmd_qga() {
    [[ $# -eq 1 ]] || die "usage: vm.sh qga '<json-command>'"
    qga_cmd "$1"
}

cmd_qga_exec() {
    [[ $# -gt 0 ]] || die "usage: vm.sh qga-exec <shell-command>"
    qga_exec_cmd "$*"
}

cmd_screenshot() {
    [[ $# -le 1 ]] || die "usage: vm.sh screenshot [output.png]"

    local ts
    local ppm
    local png
    ts="$(date -u +%Y%m%dT%H%M%SZ)"
    mkdir -p "$SCREENSHOT_DIR"

    if [[ -n "${1:-}" ]]; then
        case "$1" in
            *.png)
                png="$(absolute_path "$1")"
                ppm="${png%.png}.ppm" ;;
            *.ppm)
                ppm="$(absolute_path "$1")"
                png="${ppm%.ppm}.png" ;;
            *)
                png="$(absolute_path "$1.png")"
                ppm="${png%.png}.ppm" ;;
        esac
    else
        ppm="$SCREENSHOT_DIR/$ts.ppm"
        png="$SCREENSHOT_DIR/$ts.png"
    fi

    qmp_hmp_cmd "screendump $ppm" >/dev/null
    ppm_to_png "$ppm" "$png"
    ln -sf "$png" "$SCREENSHOT_DIR/latest.png"
    ln -sf "$ppm" "$SCREENSHOT_DIR/latest.ppm"
    info "Screenshot written to $png"
}

cmd_sendkey() {
    [[ $# -gt 0 ]] || die "usage: vm.sh sendkey <key-sequence>"
    qmp_hmp_cmd "sendkey $*" >/dev/null
    info "Sent key sequence: $*"
}

cmd_serial() {
    [[ -S "$SERIAL_SOCK" ]] || die "serial socket not found at $SERIAL_SOCK (is the VM running?)"
    if command -v socat >/dev/null; then
        info "Connecting to serial console. Press Ctrl-] to disconnect."
        exec socat -,rawer,escape=0x1d "UNIX-CONNECT:$SERIAL_SOCK"
    fi
    if command -v nc >/dev/null; then
        info "Connecting to serial console with nc. Disconnect with your terminal's interrupt/EOF key."
        exec nc -U "$SERIAL_SOCK"
    fi
    die "missing: socat or nc (install one to use vm.sh serial)"
}

cmd_diag() {
    local ts
    local out
    ts="$(date -u +%Y%m%dT%H%M%SZ)"
    out="$DIAG_DIR/$ts"
    mkdir -p "$out"

    capture() {
        local file="$1"
        shift
        {
            printf '$'
            printf ' %q' "$@"
            printf '\n\n'
            "$@"
        } >"$file" 2>&1 || true
    }

    {
        echo "Agora OS VM diagnostic bundle"
        echo "created_utc=$ts"
        echo "repo=$REPO_DIR"
        echo "vm_dir=$VM_DIR"
        echo "disk=$DISK"
        echo "ssh_port=$SSH_PORT"
        echo "qmp_socket=$QMP_SOCK"
        echo "qga_socket=$QGA_SOCK"
        echo "serial_socket=$SERIAL_SOCK"
    } >"$out/README.txt"

    [[ -f "$CONSOLE_LOG" ]] && cp "$CONSOLE_LOG" "$out/console.log"
    [[ -f "$VFS_LOG" ]] && cp "$VFS_LOG" "$out/virtiofsd.log"
    [[ -f "$PID_FILE" ]] && cp "$PID_FILE" "$out/qemu.pid"

    capture "$out/host-ps.txt" bash -c 'ps -C qemu-system-x86_64 -o pid,ppid,stat,etime,args; echo; ps -C virtiofsd -o pid,ppid,stat,etime,args'
    [[ -f "$DISK" ]] && capture "$out/qemu-img-info.txt" qemu-img info "$DISK"
    [[ -f "$DISK" ]] && capture "$out/qemu-img-snapshots.txt" qemu-img snapshot -l "$DISK"

    if [[ -S "$QMP_SOCK" ]]; then
        qmp_cmd '{"execute":"query-status"}' >"$out/qmp-query-status.json" 2>&1 || true
        qmp_cmd '{"execute":"query-block"}' >"$out/qmp-query-block.json" 2>&1 || true
        qmp_cmd '{"execute":"query-chardev"}' >"$out/qmp-query-chardev.json" 2>&1 || true
        qmp_cmd '{"execute":"query-cpus-fast"}' >"$out/qmp-query-cpus-fast.json" 2>&1 || true
        qmp_cmd '{"execute":"query-pci"}' >"$out/qmp-query-pci.json" 2>&1 || true
        cmd_screenshot "$out/screenshot.png" >"$out/screenshot.log" 2>&1 || true
    else
        echo "QMP socket unavailable" >"$out/qmp-unavailable.txt"
    fi

    if [[ -S "$QGA_SOCK" ]]; then
        if qga_cmd '{"execute":"guest-info"}' >"$out/qga-guest-info.json" 2>&1; then
            qga_cmd '{"execute":"guest-get-osinfo"}' >"$out/qga-osinfo.json" 2>&1 || true
            qga_cmd '{"execute":"guest-network-get-interfaces"}' >"$out/qga-network.json" 2>&1 || true
        else
            echo "qemu-guest-agent did not respond; see qga-guest-info.json" >"$out/qga-unavailable.txt"
        fi
    else
        echo "QGA socket unavailable" >"$out/qga-unavailable.txt"
    fi

    if ssh_quick_cmd true 2>/dev/null; then
        ssh_quick_cmd 'set +e
printf "## uname\n"; uname -a
printf "\n## uptime\n"; uptime
printf "\n## /proc/cmdline\n"; cat /proc/cmdline
printf "\n## lsblk -f\n"; lsblk -f
printf "\n## findmnt\n"; findmnt
printf "\n## ip addr\n"; ip addr
printf "\n## systemctl --failed\n"; systemctl --failed --no-pager
printf "\n## qemu-guest-agent\n"; systemctl status qemu-guest-agent --no-pager
printf "\n## /repo\n"; timeout 5 ls -la /repo
' >"$out/guest-summary.txt" 2>&1 || true
        ssh_quick_cmd 'journalctl -b --no-pager' >"$out/guest-journal.txt" 2>&1 || true
        ssh_quick_cmd 'dmesg -T || dmesg' >"$out/guest-dmesg.txt" 2>&1 || true
        ssh_quick_cmd 'sudo nft list ruleset' >"$out/guest-nft.txt" 2>&1 || true
    else
        echo "SSH unavailable; guest-side SSH diagnostics skipped." >"$out/ssh-unavailable.txt"
    fi

    info "Diagnostic bundle written to $out"
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
# phase4-ebpf-deps — install guest-side Rust/eBPF toolchain for audit-ebpf
# ---------------------------------------------------------------------------
cmd_phase4_ebpf_deps() {
    is_running || die "VM is not running — use 'vm.sh start' or 'vm.sh gui'"
    ssh_cmd "sudo /repo/scripts/provision-phase4-ebpf.sh"
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
    cleanup_stale_qemu_control_state

    stop_virtiofsd

    if $stopped; then
        info "VM stopped."
    else
        info "VM was not running."
    fi
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
    build|start|gui|ssh|console|status|diag|console-log|journal|qmp|hmp|qga|qga-exec|screenshot|sendkey|serial|snap|restore|phase2-deps|phase4-ebpf-deps|stop|destroy)
        cmd=${1//-/_}; shift; "cmd_$cmd" "$@" ;;
    *)
        cat >&2 <<'USAGE'
Usage: vm.sh <command> [args]

Commands:
  build              Create disk image and install Arch (requires sudo)
  start [--no-ssh-wait]
                     Boot headless (SSH on port 2222)
  gui [--no-ssh-wait]
                     Boot with a local QEMU window for guest Wayfire/debugging
  console            Boot foreground with serial on stdio (first-boot debug)
  ssh [cmd]          SSH into the VM or run a one-shot command
  status             Show host/QMP/QGA/serial/SSH reachability state
  diag               Collect host, QMP, QGA, screenshot, and SSH diagnostics
  console-log [n]    Print the last n serial-console log lines (default 200)
  journal [args]     Run journalctl -b --no-pager inside the guest over SSH
  qmp '<json>'       Send a raw QMP command to .vm/qmp.sock
  hmp <command>      Send a human-monitor command through QMP
  qga '<json>'       Send a raw qemu-guest-agent command to .vm/qga.sock
  qga-exec <cmd>     Run a guest shell command through qemu-guest-agent
  screenshot [png]   Capture the QEMU framebuffer to PNG
  sendkey <keys>     Send QEMU key sequence, e.g. ctrl-alt-f2 or ret
  serial             Attach to the interactive serial console socket
  snap <name>        Take a qcow2 snapshot (VM must be stopped)
  restore <name>     Restore a snapshot (VM must be stopped)
  phase2-deps        Install Wayfire/plugin/test dependencies inside the guest
  stop               Shut down QEMU and virtiofsd
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
