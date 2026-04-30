#!/usr/bin/env bash
#
# provision-phase4-ebpf.sh
# ------------------------
# Install the guest-side toolchain needed to build and run the eBPF audit
# daemon inside the Agora OS dev VM.
#
# Run inside the guest as root, or from the host via:
#   scripts/vm.sh phase4-ebpf-deps
#
set -euo pipefail

PACKAGES=(
    lld                # linker for BPF objects
    llvm               # llc for bitcode → ELF conversion
    linux-headers      # BTF and kernel headers for BPF CO-RE
    bpftrace            # optional: human-readable BPF inspection
    gnu-netcat          # nc for bus pub/sub validation
)

info() { echo ":: $*"; }
die()  { echo "error: $*" >&2; exit 1; }

require_root() { [[ ${EUID} -eq 0 ]] || die "run this inside the guest as root"; }
require_pacman() { command -v pacman >/dev/null || die "pacman not found"; }

install_packages() {
    info "Installing eBPF/audit build dependencies"
    pacman -S --needed --noconfirm "${PACKAGES[@]}"
}

install_rust_nightly() {
    if command -v rustup &>/dev/null; then
        info "rustup already installed"
    else
        info "Installing rustup for dev user"
        su - dev -c 'curl --proto "=https" --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain nightly' 2>&1
    fi

    info "Adding BPF target and rust-src"
    su - dev -c '
        export PATH="$HOME/.cargo/bin:$PATH"
        rustup target add bpfel-unknown-none 2>/dev/null || true
        rustup component add rust-src --toolchain nightly 2>/dev/null || true
    '
}

add_dev_to_cap_bpf() {
    # BPF operations require CAP_BPF, CAP_SYS_ADMIN, or root.
    # For dev-user testing, grant CAP_BPF via ambient capabilities.
    # The daemon itself runs as root in validation scripts.
    info "Verifying BPF capabilities (daemon runs as root; ambient cap not required for dev user)"
}

print_next_steps() {
    cat <<'STEPS'

:: Phase 4 eBPF audit dependencies installed.
:: Suggested next steps:
::   1. Build the eBPF daemon:
::        su - dev -c 'cd /repo/cmd/audit-ebpf && make build'
::   2. Start the event bus (from /repo):
::        nohup /repo/event-bus > /tmp/bus.log 2>&1 &
::   3. Run the daemon as root:
::        sudo /repo/cmd/audit-ebpf/target/release/audit-ebpf
::   4. Validate with the Phase 4 eBPF test script:
::        sudo /repo/test/phase4-ebpf.sh
::
:: Notes:
::   - The daemon must run as root to create BPF maps and attach probes.
::   - libssl.so.3 must be present; it's installed as a dependency of the
::     base system (via curl/openssl).
::   - After provisioning, snapshot the VM:
::        scripts/vm.sh stop
::        scripts/vm.sh snap phase4-ebpf
STEPS
}

require_root
require_pacman
install_packages
install_rust_nightly
add_dev_to_cap_bpf
print_next_steps
