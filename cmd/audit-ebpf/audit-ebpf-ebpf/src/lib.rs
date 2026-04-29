// eBPF program: AgentSight-style audit via uprobes and tracepoints.
// Captures decrypted SSL traffic, syscall arguments, and network connections.
#![no_std]

use aya_ebpf::{
    helpers::{
        bpf_get_current_comm, bpf_get_current_pid_tgid, bpf_get_current_uid_gid,
        bpf_ktime_get_ns, bpf_probe_read_user,
    },
    macros::{map, tracepoint, uprobe},
    maps::RingBuf,
    programs::{ProbeContext, TracePointContext},
};
use audit_ebpf_common::*;

#[map]
static EVENTS: RingBuf = RingBuf::with_byte_size(256 * 1024, 0);

fn now_ns() -> u64 {
    unsafe { bpf_ktime_get_ns() }
}

fn current_pid() -> u32 {
    (bpf_get_current_pid_tgid() as u64 & 0xFFFF_FFFF) as u32
}

fn current_uid() -> u32 {
    (bpf_get_current_uid_gid() as u64 & 0xFFFF_FFFF) as u32
}

fn emit(ev: KernelEvent) {
    if let Some(mut entry) = EVENTS.reserve::<KernelEvent>(0) {
        unsafe { entry.as_mut_ptr().write(ev) };
        entry.submit(0);
    }
}

// ── SSL probes ─────────────────────────────────────────────────────────────

/// SSL_read uretprobe: captures the length of decrypted data read.
/// Full buffer content capture from a uretprobe is unreliable because
/// the buffer may have been freed or overwritten by the time the
/// return probe fires. Instead we capture the return-value count and
/// rely on the companion SSL_write entry probe (which sees plaintext
/// before encryption) for content. PID/UID correlation in userspace
/// links the two directions per-connection.
#[unsafe(no_mangle)]
#[unsafe(link_section = "uretprobe/ssl_read_ret")]
pub fn ssl_read_ret(ctx: ProbeContext) -> u32 {
    // uretprobe: ctx.arg(0) is the return value (bytes read, or <=0 on error).
    let retval: i32 = match ctx.arg(0) {
        Some(v) => v,
        None => return 0,
    };
    if retval <= 0 {
        return 0;
    }
    let len = (retval as usize).min(128);

    emit(KernelEvent {
        timestamp_ns: now_ns(),
        pid: current_pid(),
        uid: current_uid(),
        event_type: EventType::SslRead as u8,
        _pad: [0; 3],
        data: KernelEventData {
            ssl: SslData {
                len: len as u16,
                _pad: [0; 6],
                buf: [0; 128],
            },
        },
    });
    0
}

/// SSL_write uprobe: captures plaintext BEFORE encryption.
#[uprobe]
fn ssl_write_entry(ctx: ProbeContext) -> u32 {
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

    let len = (num as usize).min(128);
    let mut data = SslData {
        len: len as u16,
        _pad: [0; 6],
        buf: [0; 128],
    };

    // Use probe-read to safely copy user memory through BPF verifier.
    let dst = &mut data.buf[..len];
    // Read one byte at a time; the verifier will bounds-check each.
    for i in 0..len {
        match unsafe { bpf_probe_read_user(buf_ptr.add(i) as *const u8) } {
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

// ── File open ──────────────────────────────────────────────────────────────

#[tracepoint]
fn sys_enter_openat2(ctx: TracePointContext) -> u32 {
    // sys_enter_openat2 fields (kernel >= 5.6):
    //   offset 0:  __data_loc char[] filename  (4-byte data_loc encoded)
    //   offset 4:  int dfd
    //   offset 8:  struct open_how *how  
    //   offset 16: size_t usize
    //
    // __data_loc: upper 16 bits = offset into trace entry, lower 16 bits = length.

    let data_loc: u32 = match unsafe { ctx.read_at::<u32>(0) } {
        Ok(v) => v,
        Err(_) => return 0,
    };
    let str_offset = (data_loc >> 16) as usize;
    let str_len = (data_loc & 0xFFFF) as usize;

    if str_offset == 0 || str_len == 0 || str_len > 64 {
        return 0;
    }

    // Read filename from trace entry at str_offset.
    let mut path = [0u8; 64];
    let read_len = str_len.min(63);
    // SAFETY: reading from tracepoint-provided data, bounded by __data_loc.
    for i in 0..read_len {
        if let Ok(b) = unsafe { ctx.read_at::<u8>(str_offset + i) } {
            path[i] = b;
        } else {
            break;
        }
    }
    path[read_len] = 0;

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

// ── Process exec ───────────────────────────────────────────────────────────

#[tracepoint]
fn sys_enter_execve(ctx: TracePointContext) -> u32 {
    // sys_enter_execve fields:
    //   offset 0:  __data_loc char[] filename
    //   offset 4:  const char **argv (user pointer)
    //   offset 12: const char **envp (user pointer)

    let data_loc: u32 = match unsafe { ctx.read_at::<u32>(0) } {
        Ok(v) => v,
        Err(_) => return 0,
    };
    let str_offset = (data_loc >> 16) as usize;
    let str_len = (data_loc & 0xFFFF) as usize;

    let mut filename = [0u8; 64];
    let mut comm = [0u8; 16];

    // Read filename.
    if str_offset > 0 && str_len > 0 {
        let read_len = str_len.min(63);
        for i in 0..read_len {
            if let Ok(b) = unsafe { ctx.read_at::<u8>(str_offset + i) } {
                filename[i] = b;
            } else {
                break;
            }
        }
    }

    // Capture process name via bpf_get_current_comm.
    if let Ok(comm_val) = bpf_get_current_comm() {
        let copy_len = comm_val.len().min(comm.len());
        comm[..copy_len].copy_from_slice(&comm_val[..copy_len]);
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

// ── Network connect ────────────────────────────────────────────────────────

#[tracepoint]
fn sys_enter_connect(ctx: TracePointContext) -> u32 {
    // sys_enter_connect fields:
    //   offset 0:  __data_loc char[] uservaddr  (the sockaddr, binary)
    //   offset 4:  int fd

    let data_loc: u32 = match unsafe { ctx.read_at::<u32>(0) } {
        Ok(v) => v,
        Err(_) => {
            // Fallback: emit a marker event without address.
            emit(KernelEvent {
                timestamp_ns: now_ns(),
                pid: current_pid(),
                uid: current_uid(),
                event_type: EventType::TcpConnect as u8,
                _pad: [0; 3],
                data: KernelEventData {
                    tcp_connect: TcpConnectData {
                        saddr: 0,
                        daddr: 0,
                        dport: 0,
                        _pad: [0; 2],
                    },
                },
            });
            return 0;
        }
    };

    let addr_offset = (data_loc >> 16) as usize;
    let addr_len = (data_loc & 0xFFFF) as usize;

    // sockaddr_in is 16 bytes: sa_family (u16) + port (u16 BE) + addr (u32 BE) + zero (8).
    if addr_offset > 0 && addr_len >= 8 {
        // Read sa_family and port.
        let family: u16 = match unsafe { ctx.read_at::<u16>(addr_offset) } {
            Ok(v) => v,
            Err(_) => return 0,
        };
        if family == 2 {
            // AF_INET
            let port_be: u16 = match unsafe { ctx.read_at::<u16>(addr_offset + 2) } {
                Ok(v) => v,
                Err(_) => 0,
            };
            let addr_be: u32 = match unsafe { ctx.read_at::<u32>(addr_offset + 4) } {
                Ok(v) => v,
                Err(_) => 0,
            };
            emit(KernelEvent {
                timestamp_ns: now_ns(),
                pid: current_pid(),
                uid: current_uid(),
                event_type: EventType::TcpConnect as u8,
                _pad: [0; 3],
                data: KernelEventData {
                    tcp_connect: TcpConnectData {
                        saddr: 0,
                        daddr: addr_be,
                        dport: u16::from_be(port_be),
                        _pad: [0; 2],
                    },
                },
            });
            return 0;
        }
    }

    // Non-IPv4 or unparseable: emit marker without address.
    emit(KernelEvent {
        timestamp_ns: now_ns(),
        pid: current_pid(),
        uid: current_uid(),
        event_type: EventType::TcpConnect as u8,
        _pad: [0; 3],
        data: KernelEventData {
            tcp_connect: TcpConnectData {
                saddr: 0,
                daddr: 0,
                dport: 0,
                _pad: [0; 2],
            },
        },
    });
    0
}

#[panic_handler]
fn panic(_info: &core::panic::PanicInfo) -> ! {
    loop {}
}
