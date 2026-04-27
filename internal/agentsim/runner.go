package agentsim

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
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
	var wsConn *websocket.Conn
	var httpResp *HTTPResponse
	var wsMessages []WSMessage
	step := 0
	var doneAction *Action

	for {
		state := StateSnapshot{
			Agent:            cfg.Agent,
			Scenario:         cfg.Scenario,
			Step:             step,
			RecentEvents:     recentEvents,
			LastHTTPResponse: httpResp,
			WSReceived:       wsMessages,
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

		case ActionHTTP:
			httpResp, execErr = executeHTTP(ctx, action)
			if httpResp != nil {
				allEvents = append(allEvents, httpResponseAsEvent(*httpResp))
			}

		case ActionWSConn:
			wsConn, execErr = dialWebSocket(ctx, action)

		case ActionWSSend:
			if wsConn == nil {
				execErr = fmt.Errorf("ws_send: not connected")
			} else {
				execErr = wsConn.WriteMessage(websocket.TextMessage, action.Body)
			}

		case ActionWSRecv:
			if wsConn == nil {
				execErr = fmt.Errorf("ws_recv: not connected")
			} else {
				var msgs []WSMessage
				msgs, execErr = receiveWSMessages(ctx, wsConn, action.WSMsgCount, action.WSTimeoutMS)
				wsMessages = append(wsMessages, msgs...)
				for _, m := range msgs {
					allEvents = append(allEvents, wsMessageAsEvent(m))
				}
			}

		case ActionWSClose:
			if wsConn != nil {
				wsConn.Close()
				wsConn = nil
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

	// Cleanup WebSocket connection if still open.
	if wsConn != nil {
		wsConn.Close()
	}

	finishedAt := time.Now()

	// 3. If the brain provided an explicit non-pass verdict (fail,
	// ambiguous, env_failure), use it directly. A brain-reported
	// "pass" verdict is NEVER trusted — the evaluator must verify
	// against expected outcomes. This prevents LLM brains from
	// self-reporting success without actually satisfying the scenario.
	if doneAction != nil && doneAction.DoneVerdict != "" && doneAction.DoneVerdict != schema.VerdictPass {
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
			Brain:            collectBrainInfo(cfg.Brain),
		}, nil
	}

	// 4. Evaluate against expected outcomes.
	observations := evaluate(cfg.Scenario.ExpectedOutcomes, allEvents, actionsAttempted, httpResp, wsMessages)

	verdict, failCat, failReason := computeVerdict(observations)

	// 5. Write artifacts if requested.
	var transcriptRef, eventLogRef string
	if runDir != "" {
		transcriptRef, _ = writeTranscript(runDir, cfg, actionsAttempted, allEvents, observations)
		eventLogRef, _ = writeEventLog(runDir, allEvents)
	}

	// 6. Collect brain artifacts if the brain supports it.
	brainInfo := collectBrainInfo(cfg.Brain)

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
		Brain:            brainInfo,
	}, nil
}

// ---------------------------------------------------------------------------
// Evaluator
// ---------------------------------------------------------------------------

func evaluate(expected []schema.ExpectedOutcome, events []bus.Event, actions []string, httpResp *HTTPResponse, wsMessages []WSMessage) []schema.EvaluatorObservation {
	obs := make([]schema.EvaluatorObservation, 0, len(expected))
	for _, eo := range expected {
		satisfied, actual := checkOutcome(eo, events, actions, httpResp, wsMessages)
		obs = append(obs, schema.EvaluatorObservation{
			OutcomeID: eo.ID,
			Satisfied: satisfied,
			Actual:    actual,
		})
	}
	return obs
}

func checkOutcome(eo schema.ExpectedOutcome, events []bus.Event, actions []string, httpResp *HTTPResponse, wsMessages []WSMessage) (bool, string) {
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
	case "http_response":
		return matchHTTPResponse(eo, httpResp)
	case "http_status":
		return matchHTTPStatus(eo, httpResp)
	case "ws_message":
		return matchWSMessage(eo, wsMessages)
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
	case "not_contains":
		return !contains(actual, expected)
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

func collectBrainInfo(brain Brain) *schema.BrainRunInfo {
	if ba, ok := brain.(BrainArtifacts); ok {
		info := ba.BrainRunInfo()
		return &info
	}
	return nil
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

// ---------------------------------------------------------------------------
// HTTP / WebSocket helpers
// ---------------------------------------------------------------------------

func executeHTTP(ctx context.Context, action Action) (*HTTPResponse, error) {
	method := action.Method
	if method == "" {
		method = "GET"
	}
	var bodyReader io.Reader
	if len(action.Body) > 0 {
		bodyReader = strings.NewReader(string(action.Body))
	}
	req, err := http.NewRequestWithContext(ctx, method, action.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	for k, v := range action.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http %s %s: %w", method, action.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http read body: %w", err)
	}
	h := &HTTPResponse{
		StatusCode: resp.StatusCode,
		Body:       string(body),
		Headers:    make(map[string]string),
	}
	for k := range resp.Header {
		h.Headers[k] = resp.Header.Get(k)
	}
	return h, nil
}

func dialWebSocket(ctx context.Context, action Action) (*websocket.Conn, error) {
	u, err := url.Parse(action.URL)
	if err != nil {
		return nil, fmt.Errorf("ws parse url: %w", err)
	}
	header := http.Header{}
	for k, v := range action.Headers {
		header.Set(k, v)
	}
	dialer := websocket.Dialer{}
	conn, _, err := dialer.DialContext(ctx, action.URL, header)
	if err != nil {
		return nil, fmt.Errorf("ws dial %s: %w", u.Redacted(), err)
	}
	return conn, nil
}

var _ = url.Parse // keep import

func receiveWSMessages(ctx context.Context, conn *websocket.Conn, count int, timeoutMS int) ([]WSMessage, error) {
	if count <= 0 {
		count = 1
	}
	var msgs []WSMessage
	for i := 0; i < count; i++ {
		msg, err := recvOneWSMsg(ctx, conn, timeoutMS)
		if err != nil {
			return msgs, fmt.Errorf("ws recv %d/%d: %w", i+1, count, err)
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

func recvOneWSMsg(ctx context.Context, conn *websocket.Conn, timeoutMS int) (WSMessage, error) {
	if timeoutMS <= 0 {
		timeoutMS = 5000
	}
	type result struct {
		msg WSMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		_, data, err := conn.ReadMessage()
		if err != nil {
			ch <- result{err: err}
		} else {
			ch <- result{msg: WSMessage{Body: string(data)}}
		}
	}()
	select {
	case r := <-ch:
		return r.msg, r.err
	case <-ctx.Done():
		return WSMessage{}, fmt.Errorf("ws recv cancelled: %w", ctx.Err())
	case <-time.After(time.Duration(timeoutMS) * time.Millisecond):
		return WSMessage{}, fmt.Errorf("ws recv timeout after %dms", timeoutMS)
	}
}

func httpResponseAsEvent(resp HTTPResponse) bus.Event {
	body, _ := json.Marshal(map[string]any{
		"status": resp.StatusCode,
		"body":   resp.Body,
	})
	return bus.Event{Topic: "http.response", Body: body}
}

func wsMessageAsEvent(msg WSMessage) bus.Event {
	body, _ := json.Marshal(map[string]any{"body": msg.Body})
	return bus.Event{Topic: "ws.message", Body: body}
}

func matchHTTPResponse(eo schema.ExpectedOutcome, httpResp *HTTPResponse) (bool, string) {
	if httpResp == nil {
		return false, "no HTTP response available"
	}
	if matchesValue(eo.Match, httpResp.Body, eo.Value) {
		return true, fmt.Sprintf("HTTP body matched: %s", eo.Match)
	}
	return false, fmt.Sprintf("HTTP body did not match %s=%q", eo.Match, eo.Value)
}

func matchHTTPStatus(eo schema.ExpectedOutcome, httpResp *HTTPResponse) (bool, string) {
	if httpResp == nil {
		return false, "no HTTP response available"
	}
	statusStr := strconv.Itoa(httpResp.StatusCode)
	if matchesValue(eo.Match, statusStr, eo.Value) {
		return true, fmt.Sprintf("HTTP status %s matched", statusStr)
	}
	return false, fmt.Sprintf("HTTP status %s did not match %s=%q", statusStr, eo.Match, eo.Value)
}

func matchWSMessage(eo schema.ExpectedOutcome, wsMessages []WSMessage) (bool, string) {
	for _, m := range wsMessages {
		if matchesValue(eo.Match, m.Body, eo.Value) {
			return true, fmt.Sprintf("WS message matched")
		}
	}
	return false, "no WS message matched"
}
