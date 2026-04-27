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
}

// ActionKind discriminates the type of action the brain wants to take.
type ActionKind string

const (
	ActionPublish   ActionKind = "publish"
	ActionSubscribe ActionKind = "subscribe"
	ActionReceive   ActionKind = "receive" // blocking wait for one event
	ActionSleep     ActionKind = "sleep"
	ActionDone      ActionKind = "done" // brain signals run complete
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
