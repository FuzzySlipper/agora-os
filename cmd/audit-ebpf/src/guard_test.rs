#[cfg(test)]
mod tests {
    use std::fs;

    /// Regression test: every eBPF probe entry point must gate on
    /// `is_agent_uid()` before any user-memory read or stash operation.
    ///
    /// This test reads the BPF program source and checks that each
    /// probe function body starts with the guard. If a new probe is
    /// added without the guard, this test will fail.
    #[test]
    fn all_probe_entry_points_have_uid_guard() {
        let manifest_dir = env!("CARGO_MANIFEST_DIR");
        let src = fs::read_to_string(format!("{manifest_dir}/audit-ebpf-ebpf/src/lib.rs"))
            .expect("read BPF source");

        // Probe functions: uprobe, tracepoint, and manual retprobe.
        // Each must have `if !is_agent_uid()` as the first statement.
        let probe_sigs = [
            "fn ssl_read_entry",
            "pub fn ssl_read_ret",
            "fn ssl_write_entry",
            "fn sys_enter_openat2",
            "fn sys_enter_execve",
            "fn sys_enter_connect",
        ];

        let mut missing = Vec::new();
        for sig in &probe_sigs {
            // Find the function body: look for the opening brace after the signature.
            let body_start = match src.find(sig) {
                Some(pos) => {
                    let rest = &src[pos + sig.len()..];
                    match rest.find('{') {
                        Some(i) => pos + sig.len() + i + 1,
                        None => {
                            missing.push(format!("{sig}: no opening brace found"));
                            continue;
                        }
                    }
                }
                None => {
                    missing.push(format!("{sig}: function not found in source"));
                    continue;
                }
            };

            // Check the next non-whitespace line after the opening brace.
            let body = &src[body_start..];
            let first_line = body.trim_start();
            if !first_line.starts_with("if !is_agent_uid()") {
                let snippet: String = first_line.chars().take(60).collect();
                missing.push(format!(
                    "{sig}: missing is_agent_uid() guard (first line: {snippet:?})"
                ));
            }
        }

        if !missing.is_empty() {
            panic!(
                "{} probe(s) missing is_agent_uid() early-return guard:\n  {}",
                missing.len(),
                missing.join("\n  ")
            );
        }
    }
}
