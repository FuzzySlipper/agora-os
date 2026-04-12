#!/usr/bin/env bash
#
# provision-phase2-vm.sh
# ----------------------
# Install the guest-side runtime and build dependencies needed for
# Phase 2 compositor development inside the Agora OS dev VM.
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
    meson
    ninja
    pkgconf
    wtype
    foot
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
    info "Installing Phase 2 guest packages"
    pacman -S --needed --noconfirm "${REQUIRED_PACKAGES[@]}"
}

print_next_steps() {
    cat <<'STEPS'

:: Phase 2 guest packages installed.
:: Suggested next steps:
::   1. Build the Go services inside the guest:
::        cd /repo && go build ./cmd/...
::   2. Build the Wayfire plugin inside the guest:
::        cd /repo/compositor/wayfire-plugin
::        meson setup build
::        meson compile -C build
::   3. Stop the VM and snapshot the prepared environment:
::        scripts/vm.sh stop
::        scripts/vm.sh snap phase2-deps
::
:: Notes:
::   - This installs the current known runtime needs for test/phase2.sh:
::     Wayfire, wtype, one supported terminal client (foot), and the plugin
::     build dependencies.
::   - The current VM wrapper is still headless by default. A follow-up task
::     should add a graphical guest mode for full live Wayfire validation.
STEPS
}

require_root
require_pacman
install_packages
print_next_steps
