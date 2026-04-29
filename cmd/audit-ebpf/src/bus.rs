use anyhow::{Context as _, Result};
use serde::{Deserialize, Serialize};
use std::io::Write;
use std::os::unix::net::UnixStream;

/// Client for the Agora OS event bus wire protocol.
///
/// Protocol: newline-delimited JSON over a Unix socket.
///
/// ```json
/// // Client → Server
/// {"op":"pub","topic":"audit.net.ssl_write","body":{...}}
/// ```
///
/// The broker stamps sender metadata from SO_PEERCRED; we do not
/// need to send sender_uid/sender_kind.
pub struct BusClient {
    stream: UnixStream,
    buf: Vec<u8>,
}

#[derive(Serialize)]
struct BusMsg {
    op: String,
    topic: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    body: Option<serde_json::Value>,
}

#[derive(Deserialize, Debug)]
#[allow(dead_code)]
pub struct BusEvent {
    pub topic: String,
    #[serde(default)]
    pub body: serde_json::Value,
}

impl BusClient {
    /// Connect to the event bus at `socket_path`.
    pub fn connect(socket_path: &str) -> Result<Self> {
        let stream = UnixStream::connect(socket_path)
            .with_context(|| format!("connect to event bus at {}", socket_path))?;
        Ok(BusClient {
            stream,
            buf: Vec::with_capacity(4096),
        })
    }

    /// Publish an event to a topic.
    pub fn publish(&mut self, topic: &str, body: serde_json::Value) -> Result<()> {
        let msg = BusMsg {
            op: "pub".to_string(),
            topic: topic.to_string(),
            body: Some(body),
        };
        self.buf.clear();
        serde_json::to_writer(&mut self.buf, &msg)?;
        self.buf.push(b'\n');
        self.stream.write_all(&self.buf)?;
        Ok(())
    }
}
