// Package bus implements a topic-based pub/sub broker over Unix sockets.
//
// Wire protocol: newline-delimited JSON over a single bidirectional connection.
// Each connection can publish, subscribe, and receive events.
//
// Client → Server:
//
//	{"op":"sub","topic":"audit.file.*"}
//	{"op":"unsub","topic":"audit.file.*"}
//	{"op":"pub","topic":"audit.file.modify","body":{...}}
//
// Server → Client (matching events only):
//
//	{"topic":"audit.file.modify","body":{...},"sender":{"uid":60001}}
//
// The sender uid is stamped by the broker from the client's Unix-socket peer
// credentials (SO_PEERCRED). Payload fields may still contain claimed roles or
// identities, but subscribers should treat sender metadata as authoritative and
// payload identity fields as advisory only.
//
// # Topic conventions
//
// Topics use dot-separated segments: <domain>.<entity>.<action>.
// The wildcard "*" matches exactly one segment in a subscription pattern.
//
// Planned topic taxonomy:
//
//	audit.file.modify            — agent wrote to a watched path
//	audit.file.open              — agent opened a watched file
//	audit.file.close_write       — agent closed a written file
//	compositor.surface.created   — new Wayland surface mapped
//	compositor.surface.destroyed — surface unmapped
//	compositor.surface.focused   — surface received keyboard focus
//	agent.lifecycle.spawned      — new agent user created
//	agent.lifecycle.terminated   — agent torn down
//	escalation.request.submitted — escalation sent to admin agent
//	escalation.request.decided   — admin agent returned a decision
package bus

import "encoding/json"

// Op is the operation type for client-to-server messages.
type Op string

const (
	OpPub   Op = "pub"
	OpSub   Op = "sub"
	OpUnsub Op = "unsub"
)

// ClientMsg is a message sent from a client to the broker.
type ClientMsg struct {
	Op        Op              `json:"op"`
	Topic     string          `json:"topic"`
	Body      json.RawMessage `json:"body,omitempty"`
	SenderUID *uint32         `json:"sender_uid,omitempty"`
}

// Sender identifies the Unix-socket peer that published an event.
// Its values are broker-stamped from kernel peer credentials.
type Sender struct {
	UID uint32 `json:"uid"`
}

// Event is a published event delivered to matching subscribers.
type Event struct {
	Topic  string          `json:"topic"`
	Body   json.RawMessage `json:"body"`
	Sender *Sender         `json:"sender,omitempty"`
}
