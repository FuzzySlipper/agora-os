# Build the audit-ebpf daemon and its embedded BPF program.
#
# Requires:
#   rustup (with nightly toolchain + rust-src component)
#   cargo, ar

.PHONY: build clean

BPF_TARGET := target/bpfel-unknown-none/release
BPF_OBJ    := $(BPF_TARGET)/audit-ebpf-ebpf.o

build: $(BPF_OBJ)
	RUSTUP_TOOLCHAIN=nightly cargo build --release

$(BPF_OBJ): audit-ebpf-ebpf/src/lib.rs audit-ebpf-common/src/lib.rs
	RUSTUP_TOOLCHAIN=nightly cargo build \
		--package audit-ebpf-ebpf \
		--target bpfel-unknown-none \
		--release \
		-Z build-std=core
	ar x $(BPF_TARGET)/libaudit_ebpf_ebpf.rlib
	mv audit_ebpf_ebpf.*.o $(BPF_OBJ) 2>/dev/null || \
	mv *.o $(BPF_OBJ) 2>/dev/null || true

clean:
	cargo clean
