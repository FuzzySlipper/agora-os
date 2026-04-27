// Package agentsim provides a pluggable agent simulator that exercises
// the same event-bus, shell, and webview APIs as real agents.
//
// The Brain interface separates agent decision-making from the runner harness.
// A deterministic scripted brain can drive scenarios without an LLM; LLM-backed
// brains plug in through the same interface for empirical Phase 4 tests.
package agentsim

import (
	"encoding/json"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

// ---------------------------------------------------------------------------
// Brain interface
// ---------------------------------------------------------------------------

// HTTPResponse is the result of an ActionHTTP request, stored for the
// evaluator and included in state snapshots.
type HTTPResponse struct {
	StatusCode int               `json:"status_code"`
	Body       string            `json:"body"`
	Headers    map[string]string `json:"headers,omitempty"`
}

// WSMessage is a single WebSocket message received during ws_recv.
type WSMessage struct {
	Body string `json:"body"`
}

// StateSnapshot is the world as the brain sees it at the start of an
// observe-act cycle. The runner populates this from the live event bus
// and agent state before calling Brain.Observe.
type StateSnapshot struct {
	// Agent is the runner's agent identity (populated from the
	// isolation-service spawn response or CLI args in standalone mode).
	Agent schema.AgentInfo `json:"agent"`

	// Scenario is the full scenario definition the runner is executing.
	Scenario schema.EmpiricalScenario `json:"scenario"`

	// Step is the 0-based index of the current observe-act cycle within
	// this run.
	Step int `json:"step"`

	// RecentEvents is a sliding window of events received from the bus
	// since the last observe cycle.
	RecentEvents []bus.Event `json:"recent_events,omitempty"`

	// LastHTTPResponse is the result of the most recent HTTP action.
	LastHTTPResponse *HTTPResponse `json:"last_http_response,omitempty"`

	// WSReceived is the accumulated set of WebSocket messages received
	// during ws_recv actions across the run.
	WSReceived []WSMessage `json:"ws_received,omitempty"`
}

// ActionKind discriminates the type of action the brain wants to take.
type ActionKind string

const (
	ActionPublish   ActionKind = "publish"
	ActionSubscribe ActionKind = "subscribe"
	ActionReceive   ActionKind = "receive" // blocking wait for one event
	ActionSleep     ActionKind = "sleep"
	ActionHTTP      ActionKind = "http"     // HTTP request (GET/POST)
	ActionWSConn    ActionKind = "ws_conn"  // WebSocket connect
	ActionWSRecv    ActionKind = "ws_recv"  // WebSocket receive messages
	ActionWSSend    ActionKind = "ws_send"  // WebSocket send message
	ActionWSClose   ActionKind = "ws_close" // WebSocket close
	ActionDone      ActionKind = "done"     // brain signals run complete
)

// Action is a single step the brain emits. Only one action per observe-act
// cycle; multi-step sequences are handled through repeated cycles.
type Action struct {
	Kind ActionKind `json:"kind"`

	// Publish fields.
	Topic string          `json:"topic,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`

	// Subscribe / receive fields.
	Pattern string `json:"pattern,omitempty"`

	// Sleep duration in milliseconds.
	SleepMS int `json:"sleep_ms,omitempty"`

	// HTTP fields.
	URL     string            `json:"url,omitempty"`
	Method  string            `json:"method,omitempty"`  // GET, POST (default GET)
	Headers map[string]string `json:"headers,omitempty"` // request headers

	// WebSocket fields.
	WSMsgCount  int `json:"ws_msg_count,omitempty"`  // messages to receive in ws_recv
	WSTimeoutMS int `json:"ws_timeout_ms,omitempty"` // per-message receive timeout

	// Done verdict — optional; when omitted the evaluator runs against
	// the scenario's expected outcomes. When set, the run ends immediately
	// with this verdict.
	DoneVerdict       schema.RunVerdict      `json:"done_verdict,omitempty"`
	DoneFailureCat    schema.FailureCategory `json:"done_failure_cat,omitempty"`
	DoneFailureReason string                 `json:"done_failure_reason,omitempty"`
}

// Brain is the pluggable decision-making backend for an agent simulation
// run. Implementations may be deterministic (scripted), LLM-backed, or
// stubbed for testing.
type Brain interface {
	// Observe is called once per cycle with the current state snapshot.
	// The brain returns the next action the runner should execute.
	// Returning an Action with Kind == ActionDone ends the run loop.
	Observe(state StateSnapshot) (Action, error)
}

// BrainArtifacts is an optional interface that brains can implement to
// provide model metadata and request/response artifacts for inclusion in
// the RunResult. The runner checks for this interface after the run
// completes.
type BrainArtifacts interface {
	BrainRunInfo() schema.BrainRunInfo
}
