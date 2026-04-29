// eBPF program: attaches uprobes to SSL_read/SSL_write and tracepoints
// for openat2/execve/connect. Events are written to a ring buffer for
// the userspace daemon to consume and publish to the event bus.
#![no_std]

use audit_ebpf_common::*;
use aya_ebpf::{
    helpers::{bpf_get_current_pid_tgid, bpf_get_current_uid_gid, bpf_ktime_get_ns},
    macros::{map, tracepoint, uprobe},
    maps::RingBuf,
    programs::{ProbeContext, TracePointContext},
};

#[map]
static EVENTS: RingBuf = RingBuf::with_byte_size(256 * 1024, 0);

fn now_ns() -> u64 {
    unsafe { bpf_ktime_get_ns() }
}

fn current_pid() -> u32 {
    (bpf_get_current_pid_tgid() & 0xFFFF_FFFF) as u32
}

fn current_uid() -> u32 {
    (bpf_get_current_uid_gid() & 0xFFFF_FFFF) as u32
}

/// Reserve a ring buffer slot, write the event, and submit.
fn emit(ev: KernelEvent) {
    if let Some(mut entry) = EVENTS.reserve::<KernelEvent>(0) {
        // SAFETY: entry is a valid MaybeUninit<KernelEvent> pointer.
        unsafe { entry.as_mut_ptr().write(ev) };
        entry.submit(0);
    }
}

const MAX_SSL_CAPTURE: usize = 512;

// ── SSL_read retprobe: captures decrypted plaintext ───────────────────────

/// Note: uretprobe captures only the return value (bytes read).
/// Full buffer capture from retprobe is unreliable because the buffer
/// may be overwritten between entry and return. For full capture,
/// use SSL_write_entry (pre-encryption) and correlate SSL_read byte
/// counts with process trace. The SSL_read retprobe emits a length-only
/// notification for correlation with subsequent SSL_write responses.
#[uprobe]
fn ssl_read_ret(ctx: ProbeContext) -> u32 {
    let retval: i32 = match ctx.arg(0) {
        Some(v) => v,
        None => return 0,
    };
    if retval <= 0 {
        return 0;
    }

    let len = (retval as usize).min(MAX_SSL_CAPTURE);

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
                buf: [0; 512],
            },
        },
    });
    0
}

// ── SSL_write entry: captures plaintext before encryption ─────────────────

#[uprobe]
fn ssl_write_entry(ctx: ProbeContext) -> u32 {
    // args: (SSL *s, const void *buf, int num)
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

    let len = (num as usize).min(MAX_SSL_CAPTURE);
    let mut ssl_data = SslData {
        len: len as u16,
        _pad: [0; 6],
        buf: [0; 512],
    };
    let dst = &mut ssl_data.buf[..len];
    for i in 0..len {
        // SAFETY: buf_ptr is the SSL_write plaintext buffer.
        dst[i] = unsafe { *buf_ptr.add(i) };
    }

    emit(KernelEvent {
        timestamp_ns: now_ns(),
        pid: current_pid(),
        uid: current_uid(),
        event_type: EventType::SslWrite as u8,
        _pad: [0; 3],
        data: KernelEventData { ssl: ssl_data },
    });
    0
}

// ── sys_enter_openat2 ──────────────────────────────────────────────────────

#[tracepoint]
fn sys_enter_openat2(ctx: TracePointContext) -> u32 {
    let filename_ptr: *const u8 = match unsafe { ctx.read_at::<*const u8>(8) } {
        Ok(p) => p,
        Err(_) => return 0,
    };

    let mut path = [0u8; 256];
    let _ = unsafe { aya_ebpf::helpers::bpf_probe_read_user_str_bytes(filename_ptr, &mut path) };

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

// ── sys_enter_execve ───────────────────────────────────────────────────────

#[tracepoint]
fn sys_enter_execve(ctx: TracePointContext) -> u32 {
    let filename_ptr: *const u8 = match unsafe { ctx.read_at::<*const u8>(0) } {
        Ok(p) => p,
        Err(_) => return 0,
    };

    let mut filename = [0u8; 128];
    let _ =
        unsafe { aya_ebpf::helpers::bpf_probe_read_user_str_bytes(filename_ptr, &mut filename) };

    emit(KernelEvent {
        timestamp_ns: now_ns(),
        pid: current_pid(),
        uid: current_uid(),
        event_type: EventType::ProcessExec as u8,
        _pad: [0; 3],
        data: KernelEventData {
            process_exec: ProcessExecData {
                filename,
                comm: [0; 16],
            },
        },
    });
    0
}

// ── sys_enter_connect ──────────────────────────────────────────────────────

#[tracepoint]
fn sys_enter_connect(_ctx: TracePointContext) -> u32 {
    // We'll add sockaddr_in parsing in a follow-up.
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
