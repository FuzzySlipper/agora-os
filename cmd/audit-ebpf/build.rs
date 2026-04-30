// build.rs — pre-relocation BPF helper call fixer
// Patches LLVM bitcode → ELF conversion artifacts so that BPF helper calls
// are correctly resolved before being loaded by aya-obj.
//
// LLVM >= 16 generates BPF helper calls with:
//   - src_reg=1 (BPF_PSEUDO_CALL bit set)
//   - imm=-1 (placeholder)
//   - R_BPF_64_32 relocation to the external helper symbol
//
// aya-obj 0.2.1 does not handle this relocation style. This script
// post-processes the raw .rcgu.o bitcode via llc, then patches the resulting
// ELF to resolve helper call relocations before aya loads it.
fn main() {
    // This build.rs is invoked when the main audit-ebpf crate builds.
    // The BPF object is built separately via the Makefile (make build).
    // For now, the Makefile handles everything.
    println!("cargo:rerun-if-changed=build.rs");
}
