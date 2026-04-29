// eBPF program: AgentSight-style audit via uprobes and tracepoints.
// Captures decrypted SSL traffic, syscall arguments, and network connections.
#![no_std]

use audit_ebpf_common::*;
use aya_ebpf::{
    helpers::{
        bpf_get_current_comm, bpf_get_current_pid_tgid, bpf_get_current_uid_gid, bpf_ktime_get_ns,
        bpf_probe_read_user, bpf_probe_read_user_str_bytes,
    },
    macros::{map, tracepoint, uprobe},
    maps::{HashMap, RingBuf},
    programs::{ProbeContext, RetProbeContext, TracePointContext},
};

#[map]
static EVENTS: RingBuf = RingBuf::with_byte_size(256 * 1024, 0);

/// Per-task buffer pointer stash: entry probe stores SSL_read buf ptr
/// keyed by PID+TID; return probe reads and deletes it.
#[map]
static SSL_BUF_STASH: HashMap<u64, u64> = HashMap::<u64, u64>::with_max_entries(1024, 0);

fn now_ns() -> u64 {
    unsafe { bpf_ktime_get_ns() }
}
fn current_pid() -> u32 {
    (bpf_get_current_pid_tgid() as u64 & 0xFFFF_FFFF) as u32
}
fn current_uid() -> u32 {
    (bpf_get_current_uid_gid() as u64 & 0xFFFF_FFFF) as u32
}

/// Only capture events from Agora agent UIDs (60000-61000).
fn is_agent_uid() -> bool {
    let uid = current_uid();
    uid >= 60000 && uid < 61000
}

fn emit(ev: KernelEvent) {
    if !is_agent_uid() {
        return;
    }
    if let Some(mut entry) = EVENTS.reserve::<KernelEvent>(0) {
        unsafe { entry.as_mut_ptr().write(ev) };
        entry.submit(0);
    }
}

// ── SSL_read: entry probe stashes buf pointer, retprobe captures data ──────

#[uprobe]
fn ssl_read_entry(ctx: ProbeContext) -> u32 {
    if !is_agent_uid() {
        return 0;
    }
    // SSL_read(SSL *s, void *buf, int num) — arg(1) = buf
    if let Some(buf_ptr) = ctx.arg::<*const u8>(1) {
        let key = bpf_get_current_pid_tgid();
        let val: u64 = buf_ptr as u64;
        let _ = SSL_BUF_STASH.insert(&key, &val, 0);
    }
    0
}

#[unsafe(no_mangle)]
#[unsafe(link_section = "uretprobe/ssl_read_ret")]
pub fn ssl_read_ret(ctx: RetProbeContext) -> u32 {
    if !is_agent_uid() {
        return 0;
    }
    let key = bpf_get_current_pid_tgid();

    let retval: i32 = match ctx.ret() {
        Some(v) => v,
        None => {
            // No return value: clean up and exit.
            let _ = SSL_BUF_STASH.remove(&key);
            return 0;
        }
    };
    if retval <= 0 {
        // Failed read: clean up and exit.
        let _ = SSL_BUF_STASH.remove(&key);
        return 0;
    }

    let mut data = SslData {
        len: 0,
        _pad: [0; 6],
        buf: [0; 128],
    };
    let cap = (retval as usize).min(data.buf.len());
    data.len = cap as u16;

    // Read the stashed buffer pointer from the per-task hash map.
    if let Some(buf_ptr_val) = SSL_BUF_STASH.get_ptr(&key) {
        let buf_ptr: *const u8 = unsafe { *buf_ptr_val } as *const u8;
        if !buf_ptr.is_null() {
            let dst = &mut data.buf[..cap];
            for i in 0..cap {
                match unsafe { bpf_probe_read_user(buf_ptr.add(i)) } {
                    Ok(b) => dst[i] = b,
                    Err(_) => break,
                }
            }
        }
        // Clean up: remove the stash entry for this task.
        let _ = SSL_BUF_STASH.remove(&key);
    }

    emit(KernelEvent {
        timestamp_ns: now_ns(),
        pid: current_pid(),
        uid: current_uid(),
        event_type: EventType::SslRead as u8,
        _pad: [0; 3],
        data: KernelEventData { ssl: data },
    });
    0
}

// ── SSL_write entry: captures plaintext before encryption ─────────────────

#[uprobe]
fn ssl_write_entry(ctx: ProbeContext) -> u32 {
    if !is_agent_uid() {
        return 0;
    }
    // SSL_write(SSL *s, const void *buf, int num)
    let num: i32 = match ctx.arg(2) {
        Some(v) => v,
        None => return 0,
    };
    if num <= 0 {
        return 0;
    }
    let buf_ptr: *const u8 = match ctx.arg(1) {
        Some(v) => v,
        None => return 0,
    };

    let cap = (num as usize).min(128);
    let mut data = SslData {
        len: cap as u16,
        _pad: [0; 6],
        buf: [0; 128],
    };
    let dst = &mut data.buf[..cap];
    for i in 0..cap {
        match unsafe { bpf_probe_read_user(buf_ptr.add(i)) } {
            Ok(b) => dst[i] = b,
            Err(_) => break,
        }
    }

    emit(KernelEvent {
        timestamp_ns: now_ns(),
        pid: current_pid(),
        uid: current_uid(),
        event_type: EventType::SslWrite as u8,
        _pad: [0; 3],
        data: KernelEventData { ssl: data },
    });
    0
}

// ── sys_enter_openat2: filename at offset 24 (user pointer) ───────────────

#[tracepoint]
fn sys_enter_openat2(ctx: TracePointContext) -> u32 {
    // sys_enter_* layout: common fields at 0-15, __syscall_nr at 8,
    // then trailing padding to 16, then args start:
    //   offset 16: int dfd
    //   offset 24: const char __user *filename
    let filename_ptr: *const u8 = match unsafe { ctx.read_at::<*const u8>(24) } {
        Ok(p) => p,
        Err(_) => return 0,
    };
    if filename_ptr.is_null() {
        return 0;
    }

    let mut path = [0u8; 64];
    let _ = unsafe { bpf_probe_read_user_str_bytes(filename_ptr, &mut path) };

    emit(KernelEvent {
        timestamp_ns: now_ns(),
        pid: current_pid(),
        uid: current_uid(),
        event_type: EventType::FileOpen as u8,
        _pad: [0; 3],
        data: KernelEventData {
            file_open: FileOpenData { path },
        },
    });
    0
}

// ── sys_enter_execve: filename at offset 16 (user pointer) ────────────────

#[tracepoint]
fn sys_enter_execve(ctx: TracePointContext) -> u32 {
    // sys_enter_* args start at offset 16.
    //   offset 16: const char __user *filename
    let filename_ptr: *const u8 = match unsafe { ctx.read_at::<*const u8>(16) } {
        Ok(p) => p,
        Err(_) => return 0,
    };
    if filename_ptr.is_null() {
        return 0;
    }

    let mut filename = [0u8; 64];
    let _ = unsafe { bpf_probe_read_user_str_bytes(filename_ptr, &mut filename) };

    let mut comm = [0u8; 16];
    if let Ok(comm_val) = bpf_get_current_comm() {
        let n = comm_val.len().min(comm.len());
        comm[..n].copy_from_slice(&comm_val[..n]);
    }

    emit(KernelEvent {
        timestamp_ns: now_ns(),
        pid: current_pid(),
        uid: current_uid(),
        event_type: EventType::ProcessExec as u8,
        _pad: [0; 3],
        data: KernelEventData {
            process_exec: ProcessExecData { filename, comm },
        },
    });
    0
}

// ── sys_enter_connect: sockaddr at offset 24 (user pointer) ───────────────

// Minimal sockaddr_in layout for reading from user memory via BPF.
#[repr(C)]
struct RawSockaddrIn {
    family: u16,
    port_be: u16,
    addr_be: u32,
    _zero: u64,
}

#[tracepoint]
fn sys_enter_connect(ctx: TracePointContext) -> u32 {
    // sys_enter_* args start at offset 16.
    //   offset 16: int fd
    //   offset 24: struct sockaddr __user *uservaddr
    let uservaddr: *const RawSockaddrIn = match unsafe { ctx.read_at::<*const RawSockaddrIn>(24) } {
        Ok(p) => p,
        Err(_) => return 0,
    };
    if uservaddr.is_null() {
        return 0;
    }

    let mut addr_info = TcpConnectData {
        saddr: 0,
        daddr: 0,
        dport: 0,
        _pad: [0; 2],
    };

    // Read sockaddr_in from user memory.
    if let Ok(sa) = unsafe { bpf_probe_read_user::<RawSockaddrIn>(uservaddr) } {
        if sa.family == 2 {
            // AF_INET
            addr_info.dport = u16::from_be(sa.port_be);
            // addr_be is network byte order. Store as-is; userspace does final conversion.
            addr_info.daddr = sa.addr_be;
        }
    }

    emit(KernelEvent {
        timestamp_ns: now_ns(),
        pid: current_pid(),
        uid: current_uid(),
        event_type: EventType::TcpConnect as u8,
        _pad: [0; 3],
        data: KernelEventData {
            tcp_connect: addr_info,
        },
    });
    0
}

#[panic_handler]
fn panic(_info: &core::panic::PanicInfo) -> ! {
    loop {}
}
