// Shared types between BPF and userspace programs.
// Must work in both `no_std` (BPF) and `std` (userspace) contexts.
//
// All types are `#[repr(C)]` and use only `core` primitives.
#![no_std]
/// Represents a kernel event — syscall, process lifecycle, or network.
#[repr(C)]
#[derive(Clone, Copy)]
pub struct KernelEvent {
    pub timestamp_ns: u64,
    pub pid: u32,
    pub uid: u32,
    pub event_type: u8,
    pub _pad: [u8; 3],
    pub data: KernelEventData,
}

/// Event type tags.
#[repr(u8)]
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EventType {
    Unknown = 0,
    FileOpen = 1,
    ProcessExec = 2,
    TcpConnect = 3,
    SslRead = 4,
    SslWrite = 5,
}

/// Tagged union of event-specific data.
#[repr(C)]
#[derive(Clone, Copy)]
pub union KernelEventData {
    pub file_open: FileOpenData,
    pub process_exec: ProcessExecData,
    pub tcp_connect: TcpConnectData,
    pub ssl: SslData,
}

#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct FileOpenData {
    pub path: [u8; 256],
}

#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct ProcessExecData {
    pub filename: [u8; 128],
    pub comm: [u8; 16],
}

#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct TcpConnectData {
    pub saddr: u32,
    pub daddr: u32,
    pub dport: u16,
    pub _pad: [u8; 2],
}

#[repr(C)]
#[derive(Debug, Clone, Copy)]
pub struct SslData {
    /// Number of bytes captured (<= 512).
    pub len: u16,
    pub _pad: [u8; 6],
    /// First 512 bytes of decrypted SSL read/write buffer.
    pub buf: [u8; 512],
}

impl core::fmt::Debug for KernelEvent {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.debug_struct("KernelEvent")
            .field("timestamp_ns", &self.timestamp_ns)
            .field("pid", &self.pid)
            .field("uid", &self.uid)
            .field("event_type", &self.event_type)
            .finish()
    }
}

impl KernelEvent {
    pub fn event_type_name(&self) -> &'static str {
        match self.event_type {
            1 => "file_open",
            2 => "process_exec",
            3 => "tcp_connect",
            4 => "ssl_read",
            5 => "ssl_write",
            _ => "unknown",
        }
    }
}

// In-memory layout check.
const _: () = {
    assert!(core::mem::size_of::<KernelEvent>() <= 1024);
};
