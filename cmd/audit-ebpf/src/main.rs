mod bus;

#[cfg(test)]
mod guard_test;

use anyhow::{Context as _, Result};
use audit_ebpf_common::*;
use aya::{
    Ebpf,
    maps::RingBuf,
    programs::{Program, trace_point::TracePointLinkId, uprobe::UProbeLinkId},
};
use bus::BusClient;
use serde_json::json;
use std::time::SystemTime;

const DEFAULT_BUS_SOCKET: &str = "/run/agent-os/bus.sock";
const DEFAULT_SSL_LIB: &str = "libssl.so.3";

fn main() -> Result<()> {
    env_logger::init_from_env(env_logger::Env::default().default_filter_or("info"));

    let args: Vec<String> = std::env::args().collect();
    let bus_socket = args
        .get(1)
        .map(|s| s.as_str())
        .unwrap_or(DEFAULT_BUS_SOCKET);
    let ssl_lib = args.get(2).map(|s| s.as_str()).unwrap_or(DEFAULT_SSL_LIB);

    log::info!("audit-ebpf starting");
    log::info!("  bus socket: {}", bus_socket);
    log::info!("  ssl lib:    {}", ssl_lib);

    let bpf_obj_path = args
        .get(3)
        .map(|s| s.as_str())
        .unwrap_or("target/bpfel-unknown-none/release/audit-ebpf-ebpf");

    let mut bpf = Ebpf::load_file(bpf_obj_path).context("load BPF program")?;

    let _links = attach_probes(&mut bpf, ssl_lib)?;
    log::info!("eBPF probes attached");

    let mut bus = BusClient::connect(bus_socket).context("connect to event bus")?;
    log::info!("connected to event bus");

    let mut rb: RingBuf<_> = bpf
        .take_map("EVENTS")
        .context("EVENTS map not found")?
        .try_into()
        .context("EVENTS map not a ringbuf")?;

    log::info!("polling for eBPF events");

    loop {
        match rb.next() {
            Some(item) => {
                let ev: &KernelEvent = unsafe { &*(item.as_ptr() as *const KernelEvent) };
                if let Err(e) = handle_event(&mut bus, ev) {
                    log::error!("handle event: {}", e);
                }
            }
            None => {
                // No events available — yield to avoid busy-spinning.
                // On Linux, this would be replaced by epoll on the ring buffer fd.
                std::thread::sleep(std::time::Duration::from_millis(10));
            }
        }
    }
}

struct ProbeLinks {
    _ssl_read_entry: UProbeLinkId,
    _ssl_read_ret: UProbeLinkId,
    _ssl_write: UProbeLinkId,
    _openat2: TracePointLinkId,
    _execve: TracePointLinkId,
    _connect: TracePointLinkId,
}

fn attach_probes(bpf: &mut Ebpf, ssl_lib: &str) -> Result<ProbeLinks> {
    let lib_path = resolve_lib_path(ssl_lib)?;
    let lib_str = lib_path.to_string_lossy().to_string();

    Ok(ProbeLinks {
        _ssl_read_entry: attach_uprobe(bpf, &lib_str, "SSL_read", "ssl_read_entry")?,
        _ssl_read_ret: attach_uprobe(bpf, &lib_str, "SSL_read", "ssl_read_ret")?,
        _ssl_write: attach_uprobe(bpf, &lib_str, "SSL_write", "ssl_write_entry")?,
        _openat2: attach_tracepoint(bpf, "syscalls", "sys_enter_openat2", "sys_enter_openat2")?,
        _execve: attach_tracepoint(bpf, "syscalls", "sys_enter_execve", "sys_enter_execve")?,
        _connect: attach_tracepoint(bpf, "syscalls", "sys_enter_connect", "sys_enter_connect")?,
    })
}

fn attach_uprobe(
    bpf: &mut Ebpf,
    lib_path: &str,
    symbol: &str,
    program_name: &str,
) -> Result<UProbeLinkId> {
    let prog = bpf
        .program_mut(program_name)
        .with_context(|| format!("BPF program not found: {}", program_name))?;

    match prog {
        Program::UProbe(uprobe) => {
            uprobe.load()?;
            let link = uprobe.attach(Some(symbol), 0, lib_path, None)?;
            log::info!("uprobe: {}!{} -> {}", lib_path, symbol, program_name);
            Ok(link)
        }
        _ => anyhow::bail!("program {} is not a UProbe", program_name),
    }
}

fn attach_tracepoint(
    bpf: &mut Ebpf,
    category: &str,
    name: &str,
    program_name: &str,
) -> Result<TracePointLinkId> {
    let prog = bpf
        .program_mut(program_name)
        .with_context(|| format!("BPF program not found: {}", program_name))?;

    match prog {
        Program::TracePoint(tp) => {
            tp.load()?;
            let link = tp.attach(category, name)?;
            log::info!("tracepoint: {}/{} -> {}", category, name, program_name);
            Ok(link)
        }
        _ => anyhow::bail!("program {} is not a TracePoint", program_name),
    }
}

fn resolve_lib_path(soname: &str) -> Result<std::path::PathBuf> {
    let output = std::process::Command::new("ldconfig")
        .arg("-p")
        .output()
        .context("ldconfig -p")?;
    let stdout = String::from_utf8_lossy(&output.stdout);
    for line in stdout.lines() {
        if let Some(path) = line.trim().split(" => ").nth(1) {
            let path = path.trim();
            if path.contains(soname) || line.contains(soname) {
                if std::fs::metadata(path).is_ok() {
                    return Ok(std::path::PathBuf::from(path));
                }
            }
        }
    }
    let candidates = [
        format!("/usr/lib/{}", soname),
        format!("/usr/lib64/{}", soname),
        format!("/lib/x86_64-linux-gnu/{}", soname),
    ];
    for p in &candidates {
        if std::fs::metadata(p).is_ok() {
            return Ok(std::path::PathBuf::from(p));
        }
    }
    anyhow::bail!("could not resolve library: {}", soname)
}

fn handle_event(bus: &mut BusClient, ev: &KernelEvent) -> Result<()> {
    // Use wall-clock time for the published timestamp.
    // bpf_ktime_get_ns() (CLOCK_MONOTONIC) is only used for event ordering.
    let ts = humantime::format_rfc3339_nanos(SystemTime::now()).to_string();

    match ev.event_type {
        1 => {
            let path = bytes_to_str(unsafe { &ev.data.file_open.path });
            let body = json!({"timestamp": ts, "agent_uid": ev.uid, "action": "file_open", "resource": path, "outcome": "allowed"});
            bus.publish("audit.file.open", body)?;
        }
        2 => {
            let filename = bytes_to_str(unsafe { &ev.data.process_exec.filename });
            let body = json!({"timestamp": ts, "agent_uid": ev.uid, "action": "process_exec", "resource": filename, "outcome": "allowed", "pid": ev.pid});
            bus.publish("audit.process.exec", body)?;
        }
        3 => {
            let tc = unsafe { ev.data.tcp_connect };
            let addr = if tc.daddr != 0 {
                format!("{}:{}", u32_to_ipv4(tc.daddr), tc.dport)
            } else {
                "connect event".to_string()
            };
            let body = json!({"timestamp": ts, "agent_uid": ev.uid, "action": "tcp_connect", "resource": addr, "outcome": "allowed", "pid": ev.pid});
            bus.publish("audit.net.connect", body)?;
        }
        4 | 5 => {
            let dir = if ev.event_type == 4 {
                "ssl_read"
            } else {
                "ssl_write"
            };
            let ssl = unsafe { &ev.data.ssl };
            let len = ssl.len as usize;
            let buf = &ssl.buf[..len.min(ssl.buf.len())];
            let text = String::from_utf8_lossy(buf);
            let topic = format!("audit.net.{}", dir);
            let body = json!({"timestamp": ts, "agent_uid": ev.uid, "action": dir, "resource": format!("ssl buffer ({} bytes)", len), "outcome": "allowed", "pid": ev.pid, "ssl_data": text});
            bus.publish(&topic, body)?;
        }
        _ => {}
    }
    Ok(())
}

fn bytes_to_str(buf: &[u8]) -> String {
    let end = buf.iter().position(|&b| b == 0).unwrap_or(buf.len());
    String::from_utf8_lossy(&buf[..end]).to_string()
}

fn u32_to_ipv4(addr_be: u32) -> String {
    let a = u32::from_be(addr_be);
    format!(
        "{}.{}.{}.{}",
        (a >> 24) & 0xff,
        (a >> 16) & 0xff,
        (a >> 8) & 0xff,
        a & 0xff
    )
}
