.PHONY: build clean

BPF_TARGET := target/bpfel-unknown-none/release
BPF_OBJ    := $(BPF_TARGET)/audit-ebpf-ebpf

build: $(BPF_OBJ)
	RUSTUP_TOOLCHAIN=nightly cargo build --release

# Build the BPF program using bpf-linker, which properly resolves
# BPF helper call relocations (required for LLVM >= 16 codegen).
$(BPF_OBJ): audit-ebpf-ebpf/src/lib.rs audit-ebpf-common/src/lib.rs
	RUSTUP_TOOLCHAIN=nightly \
	RUSTFLAGS="-C linker=bpf-linker" \
	cargo build \
		--package audit-ebpf-ebpf \
		--target bpfel-unknown-none \
		--release \
		-Z build-std=core

clean:
	cargo clean
	rm -f $(BPF_OBJ)
