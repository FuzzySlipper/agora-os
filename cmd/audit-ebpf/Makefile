.PHONY: build clean

BPF_TARGET := target/bpfel-unknown-none/release
BPF_OBJ    := $(BPF_TARGET)/audit-ebpf-ebpf

build: $(BPF_OBJ)
	RUSTUP_TOOLCHAIN=nightly cargo build --release

# Build the BPF program, then convert LLVM bitcode to ELF via llc.
$(BPF_OBJ): audit-ebpf-ebpf/src/lib.rs audit-ebpf-common/src/lib.rs
	RUSTUP_TOOLCHAIN=nightly cargo build \
		--package audit-ebpf-ebpf \
		--target bpfel-unknown-none \
		--release \
		-Z build-std=core
	# Extract the object file from the rlib (LLVM bitcode).
	ar x $(BPF_TARGET)/deps/libaudit_ebpf_ebpf-*.rlib
	# Convert bitcode to ELF. There should be exactly one .rcgu.o.
	llc -march=bpfel -filetype=obj -o $(BPF_OBJ) *.rcgu.o
	rm -f *.rcgu.o

clean:
	cargo clean
	rm -f $(BPF_OBJ)
