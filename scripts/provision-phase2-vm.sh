#!/usr/bin/env bash
#
# provision-phase2-vm.sh
# ----------------------
# Install the guest-side runtime and build dependencies needed for
# Phase 2 and Phase 3 guest validation inside the Agora OS dev VM.
#
# Run inside the guest as root, or from the host via:
#   scripts/vm.sh start
#   scripts/vm.sh phase2-deps
#   scripts/vm.sh stop
#   scripts/vm.sh snap phase2-deps
#
set -euo pipefail

REQUIRED_PACKAGES=(
    wayfire
    wf-config
    wayland
    wayland-protocols
    vulkan-headers
    glm
    meson
    python
    ninja
    pkgconf
    wtype
    foot
    python-gobject
    webkit2gtk-4.1
    # Keep Xwayland available for mixed-client debugging and future bridge checks.
    xorg-xwayland
    seatd
)

info() { echo ":: $*"; }
die()  { echo "error: $*" >&2; exit 1; }

require_root() {
    [[ ${EUID} -eq 0 ]] || die "run this inside the guest as root"
}

require_pacman() {
    command -v pacman >/dev/null || die "pacman not found"
}

install_packages() {
    info "Installing Phase 2/3 guest packages"
    pacman -S --needed --noconfirm "${REQUIRED_PACKAGES[@]}"
}

print_next_steps() {
    cat <<'STEPS'

:: Phase 2/3 guest packages installed.
:: Suggested next steps:
::   1. Build the Go services inside the guest:
::        cd /repo && go build ./cmd/...
::   2. Build the Wayfire plugin inside the guest:
::        meson setup /tmp/agora-wayfire-plugin-build /repo/compositor/wayfire-plugin
::        meson compile -C /tmp/agora-wayfire-plugin-build
::        sudo meson install -C /tmp/agora-wayfire-plugin-build
::   3. Stop the VM and snapshot the prepared environment:
::        scripts/vm.sh stop
::        scripts/vm.sh snap phase2-deps
::
:: Notes:
::   - This installs the current known runtime needs for test/phase2.sh and
::     test/phase3.sh: Wayfire, wtype, one supported terminal client (foot),
::     the plugin build dependencies (including Vulkan headers and GLM
::     required by the current wlroots/Wayfire headers), and the GTK/WebKit
::     runtime used by cmd/webview-launcher.
::   - After provisioning, use scripts/vm.sh gui on the host to boot the
::     guest with a local graphics window for live Wayfire validation.
STEPS
}

require_root
require_pacman
install_packages
print_next_steps
