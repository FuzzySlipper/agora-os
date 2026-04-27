package agentsim

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

// RunnerConfig holds the settings for a single scenario run.
type RunnerConfig struct {
	Scenario    schema.EmpiricalScenario
	Brain       Brain
	Agent       schema.AgentInfo
	BusSocket   string
	RunID       string
	Attempt     int
	ArtifactDir string
}

// Run executes one scenario attempt through the observe-act loop and
// returns the structured RunResult. Errors during setup (bus connect,
// prerequisite check) produce a VerdictEnvFailure result rather than a
// Go error, so callers can aggregate across multiple attempts.
func Run(ctx context.Context, cfg RunnerConfig) (*schema.RunResult, error) {
	startedAt := time.Now()

	runDir := ""
	if cfg.ArtifactDir != "" {
		runDir = filepath.Join(cfg.ArtifactDir, cfg.RunID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			return nil, fmt.Errorf("create artifact dir %s: %w", runDir, err)
		}
	}

	// 1. Connect to event bus.
	client, err := bus.Dial(cfg.BusSocket)
	if err != nil {
		return envFailureResult(cfg, startedAt, nil, nil, "connect event bus: "+err.Error()), nil
	}
	defer client.Close()

	// 2. Run the brain observe-act loop.
	var actionsAttempted []string
	var allEvents []bus.Event
	var recentEvents []bus.Event
	step := 0
	var doneAction *Action

	for {
		state := StateSnapshot{
			Agent:        cfg.Agent,
			Scenario:     cfg.Scenario,
			Step:         step,
			RecentEvents: recentEvents,
		}

		action, err := cfg.Brain.Observe(state)
		if err != nil {
			finishedAt := time.Now()
			return envFailureResult(cfg, startedAt, &finishedAt, actionsAttempted,
				fmt.Sprintf("brain error at step %d: %v", step, err)), nil
		}

		actionJSON, _ := json.Marshal(action)
		actionsAttempted = append(actionsAttempted, string(actionJSON))
		log.Printf("[agent-sim] step=%d action=%s", step, action.Kind)

		// Clear recent events for the next cycle.
		recentEvents = nil

		var execErr error
		switch action.Kind {
		case ActionPublish:
			execErr = client.Publish(action.Topic, action.Body)

		case ActionSubscribe:
			execErr = client.Subscribe(action.Pattern)

		case ActionReceive:
			evCh := make(chan bus.Event, 1)
			errCh := make(chan error, 1)
			go func() {
				ev, recvErr := client.Receive()
				if recvErr != nil {
					errCh <- recvErr
				} else {
					evCh <- ev
				}
			}()
			select {
			case ev := <-evCh:
				allEvents = append(allEvents, ev)
				recentEvents = append(recentEvents, ev)
			case recvErr := <-errCh:
				execErr = recvErr
			case <-ctx.Done():
				execErr = fmt.Errorf("receive cancelled: %w", ctx.Err())
			}

		case ActionSleep:
			if ctx.Err() != nil {
				finishedAt := time.Now()
				return envFailureResult(cfg, startedAt, &finishedAt, actionsAttempted,
					"cancelled: "+ctx.Err().Error()), nil
			}
			select {
			case <-time.After(time.Duration(action.SleepMS) * time.Millisecond):
			case <-ctx.Done():
				finishedAt := time.Now()
				return envFailureResult(cfg, startedAt, &finishedAt, actionsAttempted,
					"cancelled: "+ctx.Err().Error()), nil
			}

		case ActionDone:
			doneAction = &action
		default:
			execErr = fmt.Errorf("unknown action kind: %s", action.Kind)
		}

		if execErr != nil {
			finishedAt := time.Now()
			return envFailureResult(cfg, startedAt, &finishedAt, actionsAttempted,
				fmt.Sprintf("step %d %s: %v", step, action.Kind, execErr)), nil
		}

		if action.Kind == ActionDone {
			break
		}
		step++
	}

	finishedAt := time.Now()

	// 3. If the brain provided an explicit verdict, use it directly.
	if doneAction != nil && doneAction.DoneVerdict != "" {
		return &schema.RunResult{
			RunID:            cfg.RunID,
			ScenarioID:       cfg.Scenario.ID,
			Attempt:          cfg.Attempt,
			StartedAt:        startedAt,
			FinishedAt:       finishedAt,
			ActionsAttempted: actionsAttempted,
			Verdict:          doneAction.DoneVerdict,
			FailureCategory:  doneAction.DoneFailureCat,
			FailureReason:    doneAction.DoneFailureReason,
		}, nil
	}

	// 4. Evaluate against expected outcomes.
	observations := evaluate(cfg.Scenario.ExpectedOutcomes, allEvents, actionsAttempted)

	verdict, failCat, failReason := computeVerdict(observations)

	// 5. Write artifacts if requested.
	var transcriptRef, eventLogRef string
	if runDir != "" {
		transcriptRef, _ = writeTranscript(runDir, cfg, actionsAttempted, allEvents, observations)
		eventLogRef, _ = writeEventLog(runDir, allEvents)
	}

	return &schema.RunResult{
		RunID:            cfg.RunID,
		ScenarioID:       cfg.Scenario.ID,
		Attempt:          cfg.Attempt,
		StartedAt:        startedAt,
		FinishedAt:       finishedAt,
		TranscriptRef:    transcriptRef,
		EventLogRef:      eventLogRef,
		ActionsAttempted: actionsAttempted,
		Observations:     observations,
		Verdict:          verdict,
		FailureCategory:  failCat,
		FailureReason:    failReason,
	}, nil
}

// ---------------------------------------------------------------------------
// Evaluator
// ---------------------------------------------------------------------------

func evaluate(expected []schema.ExpectedOutcome, events []bus.Event, actions []string) []schema.EvaluatorObservation {
	obs := make([]schema.EvaluatorObservation, 0, len(expected))
	for _, eo := range expected {
		satisfied, actual := checkOutcome(eo, events, actions)
		obs = append(obs, schema.EvaluatorObservation{
			OutcomeID: eo.ID,
			Satisfied: satisfied,
			Actual:    actual,
		})
	}
	return obs
}

func checkOutcome(eo schema.ExpectedOutcome, events []bus.Event, actions []string) (bool, string) {
	// count_gte / count_lte apply to event or action counts regardless of source.
	switch eo.Match {
	case "count_gte":
		return matchCountGTE(eo, events, actions)
	case "count_lte":
		return matchCountLTE(eo, events, actions)
	}

	switch eo.Source {
	case "event_bus_topic":
		return matchTopic(eo, events)
	case "event_bus_payload":
		return matchPayload(eo, events)
	case "action":
		return matchAction(eo, actions)
	default:
		return false, fmt.Sprintf("unsupported outcome source: %s", eo.Source)
	}
}

func matchTopic(eo schema.ExpectedOutcome, events []bus.Event) (bool, string) {
	for _, ev := range events {
		if matchesValue(eo.Match, ev.Topic, eo.Value) {
			return true, ev.Topic
		}
	}
	return false, fmt.Sprintf("no event matched topic %s=%q", eo.Match, eo.Value)
}

func matchPayload(eo schema.ExpectedOutcome, events []bus.Event) (bool, string) {
	for _, ev := range events {
		if matchesValue(eo.Match, string(ev.Body), eo.Value) {
			return true, fmt.Sprintf("event %s body matched", ev.Topic)
		}
	}
	return false, "no event payload matched"
}

func matchAction(eo schema.ExpectedOutcome, actions []string) (bool, string) {
	for _, a := range actions {
		if matchesValue(eo.Match, a, eo.Value) {
			return true, a
		}
	}
	return false, "no action matched"
}

func matchCountGTE(eo schema.ExpectedOutcome, events []bus.Event, actions []string) (bool, string) {
	threshold, err := strconv.Atoi(eo.Value)
	if err != nil {
		return false, fmt.Sprintf("invalid count_gte threshold %q: %v", eo.Value, err)
	}
	n := countForSource(eo.Source, events, actions)
	if n >= threshold {
		return true, fmt.Sprintf("count %d >= %d", n, threshold)
	}
	return false, fmt.Sprintf("count %d < %d", n, threshold)
}

func matchCountLTE(eo schema.ExpectedOutcome, events []bus.Event, actions []string) (bool, string) {
	threshold, err := strconv.Atoi(eo.Value)
	if err != nil {
		return false, fmt.Sprintf("invalid count_lte threshold %q: %v", eo.Value, err)
	}
	n := countForSource(eo.Source, events, actions)
	if n <= threshold {
		return true, fmt.Sprintf("count %d <= %d", n, threshold)
	}
	return false, fmt.Sprintf("count %d > %d", n, threshold)
}

func countForSource(source string, events []bus.Event, actions []string) int {
	switch source {
	case "action":
		return len(actions)
	default:
		return len(events)
	}
}

func matchesValue(matchType, actual, expected string) bool {
	switch matchType {
	case "contains":
		return contains(actual, expected)
	case "equals":
		return actual == expected
	case "regex":
		re, err := regexp.Compile(expected)
		if err != nil {
			return false
		}
		return re.MatchString(actual)
	default:
		return false
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func computeVerdict(obs []schema.EvaluatorObservation) (schema.RunVerdict, schema.FailureCategory, string) {
	failures := 0
	var failDetails []string
	for _, o := range obs {
		if !o.Satisfied {
			failures++
			failDetails = append(failDetails, fmt.Sprintf("%s: %s", o.OutcomeID, o.Actual))
		}
	}
	if failures == 0 {
		return schema.VerdictPass, "", ""
	}
	return schema.VerdictFail, schema.FailureAssertion,
		fmt.Sprintf("%d/%d outcomes not satisfied: %v", failures, len(obs), failDetails)
}

// ---------------------------------------------------------------------------
// Artifact helpers
// ---------------------------------------------------------------------------

func writeTranscript(dir string, cfg RunnerConfig, actions []string, events []bus.Event, obs []schema.EvaluatorObservation) (string, error) {
	path := filepath.Join(dir, "transcript.json")
	type entry struct {
		Time   string                       `json:"time"`
		Action string                       `json:"action,omitempty"`
		Event  *bus.Event                   `json:"event,omitempty"`
		Obs    *schema.EvaluatorObservation `json:"observation,omitempty"`
	}
	var entries []entry
	now := time.Now().Format(time.RFC3339Nano)
	for _, a := range actions {
		entries = append(entries, entry{Time: now, Action: a})
	}
	for i := range events {
		entries = append(entries, entry{Time: now, Event: &events[i]})
	}
	for i := range obs {
		entries = append(entries, entry{Time: now, Obs: &obs[i]})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o644)
}

func writeEventLog(dir string, events []bus.Event) (string, error) {
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return path, err
		}
	}
	return path, nil
}

func envFailureResult(cfg RunnerConfig, startedAt time.Time, finishedAt *time.Time, actions []string, reason string) *schema.RunResult {
	ft := startedAt
	if finishedAt != nil {
		ft = *finishedAt
	}
	return &schema.RunResult{
		RunID:            cfg.RunID,
		ScenarioID:       cfg.Scenario.ID,
		Attempt:          cfg.Attempt,
		StartedAt:        startedAt,
		FinishedAt:       ft,
		ActionsAttempted: actions,
		Verdict:          schema.VerdictEnvFailure,
		FailureCategory:  schema.FailureSetup,
		FailureReason:    reason,
	}
}

// PeerUIDAgent returns an AgentInfo populated from the current process
// identity via os.Getuid. Suitable for standalone agent-sim runs where
// the process is already running as the target agent uid.
func PeerUIDAgent(name string) schema.AgentInfo {
	uid := uint32(os.Getuid())
	return schema.AgentInfo{
		Name:   name,
		UID:    uid,
		Status: schema.StatusRunning,
	}
}
