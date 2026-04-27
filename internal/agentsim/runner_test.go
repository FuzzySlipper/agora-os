package agentsim_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/agentsim"
	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

// testBus starts a bus broker on a temp Unix socket and returns the socket
// path. The caller must call cleanup to stop the broker and remove the socket.
func testBus(t *testing.T) (socketPath string, cleanup func()) {
	t.Helper()

	dir := t.TempDir()
	socketPath = filepath.Join(dir, "bus.sock")

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	broker := bus.NewBroker()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				// For testing, register with uid 0 (root).
				_ = bus.ServeConn(conn, broker)
			}()
		}
	}()

	return socketPath, func() {
		l.Close()
	}
}

// busClient connects to the test bus and returns a client convenient for
// verifying events published by the sim runner.
func busClient(t *testing.T, socketPath string) *bus.Client {
	t.Helper()
	c, err := bus.Dial(socketPath)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	return c
}

func TestScriptedBrain_ReplaysActions(t *testing.T) {
	script := []agentsim.Action{
		{Kind: agentsim.ActionSubscribe, Pattern: "test.*"},
		{Kind: agentsim.ActionPublish, Topic: "test.hello", Body: json.RawMessage(`"world"`)},
		{Kind: agentsim.ActionDone},
	}

	brain := agentsim.NewScriptedBrain(script)

	for i, want := range script {
		act, err := brain.Observe(agentsim.StateSnapshot{Step: i})
		if err != nil {
			t.Fatalf("step %d: unexpected error: %v", i, err)
		}
		if act.Kind != want.Kind {
			t.Errorf("step %d: kind = %s, want %s", i, act.Kind, want.Kind)
		}
	}
}

func TestScriptedBrain_ExhaustedReturnsDone(t *testing.T) {
	brain := agentsim.NewScriptedBrain(nil)
	act, err := brain.Observe(agentsim.StateSnapshot{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if act.Kind != agentsim.ActionDone {
		t.Errorf("kind = %s, want done", act.Kind)
	}
}

func TestRunner_PublishAndReceive(t *testing.T) {
	socketPath, cleanup := testBus(t)
	defer cleanup()

	scenario := schema.EmpiricalScenario{
		ID:       "pub-recv-test",
		Title:    "Publish and receive test",
		RunCount: 1,
		ExpectedOutcomes: []schema.ExpectedOutcome{
			{
				ID:          "recv-echo",
				Description: "received echo response",
				Source:      "event_bus_topic",
				Match:       "equals",
				Value:       "test.echo",
			},
			{
				ID:          "recv-payload",
				Description: "payload contains hello",
				Source:      "event_bus_payload",
				Match:       "contains",
				Value:       "hello",
			},
		},
	}

	// Build a deterministic script: subscribe, then receive the echo.
	script := []agentsim.Action{
		{Kind: agentsim.ActionSubscribe, Pattern: "test.echo"},
		{Kind: agentsim.ActionReceive},
		{Kind: agentsim.ActionDone},
	}

	brain := agentsim.NewScriptedBrain(script)

	cfg := agentsim.RunnerConfig{
		Scenario:  scenario,
		Brain:     brain,
		Agent:     schema.AgentInfo{Name: "test-agent", UID: 60001, Status: schema.StatusRunning},
		BusSocket: socketPath,
		RunID:     "test-run-1",
		Attempt:   1,
	}

	// Publish the echo event from a separate client after a short delay,
	// so the runner's Receive picks it up.
	go func() {
		time.Sleep(50 * time.Millisecond)
		c := busClient(t, socketPath)
		defer c.Close()
		if err := c.Publish("test.echo", json.RawMessage(`"hello"`)); err != nil {
			t.Errorf("publish echo: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := agentsim.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.Verdict != schema.VerdictPass {
		t.Errorf("verdict = %s, want pass. fail reason: %s", result.Verdict, result.FailureReason)
	}

	if len(result.Observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(result.Observations))
	}
	for _, obs := range result.Observations {
		if !obs.Satisfied {
			t.Errorf("outcome %s not satisfied: %s", obs.OutcomeID, obs.Actual)
		}
	}
}

func TestRunner_ExplicitDoneVerdict(t *testing.T) {
	socketPath, cleanup := testBus(t)
	defer cleanup()

	scenario := schema.EmpiricalScenario{
		ID:       "explicit-done",
		RunCount: 1,
	}

	script := []agentsim.Action{
		{
			Kind:              agentsim.ActionDone,
			DoneVerdict:       schema.VerdictFail,
			DoneFailureCat:    schema.FailureTimeout,
			DoneFailureReason: "brain signalled timeout",
		},
	}

	brain := agentsim.NewScriptedBrain(script)

	cfg := agentsim.RunnerConfig{
		Scenario:  scenario,
		Brain:     brain,
		Agent:     schema.AgentInfo{Name: "test-agent", UID: 60001, Status: schema.StatusRunning},
		BusSocket: socketPath,
		RunID:     "test-run-2",
		Attempt:   1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := agentsim.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.Verdict != schema.VerdictFail {
		t.Errorf("verdict = %s, want fail", result.Verdict)
	}
	if result.FailureCategory != schema.FailureTimeout {
		t.Errorf("failure category = %s, want timeout", result.FailureCategory)
	}
}

func TestEvaluator_AllPass(t *testing.T) {
	observations := []schema.EvaluatorObservation{
		{OutcomeID: "o1", Satisfied: true, Actual: "matched"},
		{OutcomeID: "o2", Satisfied: true, Actual: "matched"},
	}
	v, cat, _ := computeVerdictForTest(observations)
	if v != schema.VerdictPass {
		t.Errorf("verdict = %s, want pass", v)
	}
	if cat != "" {
		t.Errorf("category = %s, want empty", cat)
	}
}

func TestEvaluator_SomeFail(t *testing.T) {
	observations := []schema.EvaluatorObservation{
		{OutcomeID: "o1", Satisfied: true, Actual: "ok"},
		{OutcomeID: "o2", Satisfied: false, Actual: "no event matched topic equals=\"missing\""},
	}
	v, cat, reason := computeVerdictForTest(observations)
	if v != schema.VerdictFail {
		t.Errorf("verdict = %s, want fail", v)
	}
	if cat != schema.FailureAssertion {
		t.Errorf("category = %s, want assertion", cat)
	}
	if reason == "" {
		t.Error("expected non-empty failure reason")
	}
}

// computeVerdictForTest mirrors the internal computeVerdict in runner.go.
func computeVerdictForTest(obs []schema.EvaluatorObservation) (schema.RunVerdict, schema.FailureCategory, string) {
	failures := 0
	var failDetails []string
	for _, o := range obs {
		if !o.Satisfied {
			failures++
			failDetails = append(failDetails, o.OutcomeID+": "+o.Actual)
		}
	}
	if failures == 0 {
		return schema.VerdictPass, "", ""
	}
	return schema.VerdictFail, schema.FailureAssertion,
		"failures: " + joinStrings(failDetails, ", ")
}

func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	r := ss[0]
	for _, s := range ss[1:] {
		r += sep + s
	}
	return r
}

func TestRunner_Artifacts(t *testing.T) {
	socketPath, cleanup := testBus(t)
	defer cleanup()

	dir := t.TempDir()

	scenario := schema.EmpiricalScenario{
		ID:       "artifact-test",
		RunCount: 1,
		ExpectedOutcomes: []schema.ExpectedOutcome{
			{ID: "o1", Description: "always pass", Source: "event_bus_topic", Match: "contains", Value: "x"},
		},
	}

	// Publish an event we can receive.
	go func() {
		time.Sleep(30 * time.Millisecond)
		c := busClient(t, socketPath)
		defer c.Close()
		c.Publish("test.x", json.RawMessage(`"data"`))
	}()

	script := []agentsim.Action{
		{Kind: agentsim.ActionSubscribe, Pattern: "test.*"},
		{Kind: agentsim.ActionReceive},
		{Kind: agentsim.ActionDone},
	}

	cfg := agentsim.RunnerConfig{
		Scenario:    scenario,
		Brain:       agentsim.NewScriptedBrain(script),
		Agent:       schema.AgentInfo{Name: "test-agent", UID: 60001, Status: schema.StatusRunning},
		BusSocket:   socketPath,
		RunID:       "artifact-run",
		Attempt:     1,
		ArtifactDir: dir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := agentsim.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.TranscriptRef == "" {
		t.Error("expected transcript ref")
	} else {
		if _, err := os.Stat(result.TranscriptRef); err != nil {
			t.Errorf("transcript not found: %v", err)
		}
	}
	if result.EventLogRef == "" {
		t.Error("expected event log ref")
	} else {
		if _, err := os.Stat(result.EventLogRef); err != nil {
			t.Errorf("event log not found: %v", err)
		}
	}
}

func TestRunner_ReceiveTimeout(t *testing.T) {
	socketPath, cleanup := testBus(t)
	defer cleanup()

	scenario := schema.EmpiricalScenario{
		ID:       "timeout-test",
		RunCount: 1,
	}

	// Script: subscribe then receive — but no event is published, so
	// the receive should be cancelled by the context deadline.
	script := []agentsim.Action{
		{Kind: agentsim.ActionSubscribe, Pattern: "no.one.will.publish"},
		{Kind: agentsim.ActionReceive},
		{Kind: agentsim.ActionDone},
	}

	cfg := agentsim.RunnerConfig{
		Scenario:  scenario,
		Brain:     agentsim.NewScriptedBrain(script),
		Agent:     schema.AgentInfo{Name: "test-agent", UID: 60001, Status: schema.StatusRunning},
		BusSocket: socketPath,
		RunID:     "timeout-run",
		Attempt:   1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result, err := agentsim.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.Verdict != schema.VerdictEnvFailure {
		t.Errorf("verdict = %s, want env_failure (cancelled receive)", result.Verdict)
	}
}

func TestRunner_CountMatchers(t *testing.T) {
	socketPath, cleanup := testBus(t)
	defer cleanup()

	scenario := schema.EmpiricalScenario{
		ID:       "count-test",
		RunCount: 1,
		ExpectedOutcomes: []schema.ExpectedOutcome{
			{
				ID:          "at-least-2",
				Description: "at least 2 events received",
				Source:      "event_bus_topic",
				Match:       "count_gte",
				Value:       "2",
			},
			{
				ID:          "at-most-5",
				Description: "at most 5 events received",
				Source:      "event_bus_topic",
				Match:       "count_lte",
				Value:       "5",
			},
		},
	}

	// Publish 3 events.
	go func() {
		time.Sleep(20 * time.Millisecond)
		c := busClient(t, socketPath)
		defer c.Close()
		c.Publish("test.a", json.RawMessage(`1`))
		time.Sleep(10 * time.Millisecond)
		c.Publish("test.b", json.RawMessage(`2`))
		time.Sleep(10 * time.Millisecond)
		c.Publish("test.c", json.RawMessage(`3`))
	}()

	script := []agentsim.Action{
		{Kind: agentsim.ActionSubscribe, Pattern: "test.*"},
		{Kind: agentsim.ActionReceive},
		{Kind: agentsim.ActionReceive},
		{Kind: agentsim.ActionReceive},
		{Kind: agentsim.ActionDone},
	}

	cfg := agentsim.RunnerConfig{
		Scenario:  scenario,
		Brain:     agentsim.NewScriptedBrain(script),
		Agent:     schema.AgentInfo{Name: "test-agent", UID: 60001, Status: schema.StatusRunning},
		BusSocket: socketPath,
		RunID:     "count-run",
		Attempt:   1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := agentsim.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.Verdict != schema.VerdictPass {
		t.Errorf("verdict = %s, want pass. fail reason: %s", result.Verdict, result.FailureReason)
	}
	for _, obs := range result.Observations {
		if !obs.Satisfied {
			t.Errorf("outcome %s not satisfied: %s", obs.OutcomeID, obs.Actual)
		}
	}
}

func TestRunner_Phase3ProtocolTopics(t *testing.T) {
	// Exercise the same conversation.turn.* topic family that the Phase 3
	// shell and event-bus-web use. This proves the runner operates on
	// real protocol paths, not just test-only topics.
	socketPath, cleanup := testBus(t)
	defer cleanup()

	scenario := schema.EmpiricalScenario{
		ID:       "phase3-conversation",
		Title:    "Phase 3 conversation turn round-trip",
		RunCount: 1,
		ExpectedOutcomes: []schema.ExpectedOutcome{
			{
				ID:          "recv-turn-request",
				Description: "received conversation.turn.requested event",
				Source:      "event_bus_topic",
				Match:       "equals",
				Value:       "conversation.turn.requested",
			},
			{
				ID:          "payload-contains-prompt",
				Description: "conversation turn payload contains the prompt text",
				Source:      "event_bus_payload",
				Match:       "contains",
				Value:       "hello agent",
			},
			{
				ID:          "published-response",
				Description: "response published to conversation.turn.responded",
				Source:      "action",
				Match:       "contains",
				Value:       "conversation.turn.responded",
			},
			{
				ID:          "at-least-2-events",
				Description: "at least 2 events exchanged",
				Source:      "event_bus_topic",
				Match:       "count_gte",
				Value:       "2",
			},
		},
	}

	// Publish a conversation turn request from another client.
	go func() {
		time.Sleep(30 * time.Millisecond)
		c := busClient(t, socketPath)
		defer c.Close()
		reqBody, _ := json.Marshal(schema.ConversationTurnRequest{
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Prompt:    "hello agent, please report status",
		})
		c.Publish(schema.TopicConversationTurnRequested, json.RawMessage(reqBody))
		// Publish a second event so the count_gte matcher has enough events.
		time.Sleep(10 * time.Millisecond)
		c.Publish("agent.work.assigned", json.RawMessage(`{"task_id":"t1"}`))
	}()

	// Script: subscribe, receive both the turn request and the work event,
	// publish a turn response.
	respBody, _ := json.Marshal(schema.ConversationTurnResponse{
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Summary:   "status: all systems nominal",
	})

	script := []agentsim.Action{
		{Kind: agentsim.ActionSubscribe, Pattern: "conversation.turn.*"},
		{Kind: agentsim.ActionSubscribe, Pattern: "agent.work.*"},
		{Kind: agentsim.ActionReceive},
		{Kind: agentsim.ActionReceive},
		{Kind: agentsim.ActionPublish, Topic: schema.TopicConversationTurnResponded, Body: json.RawMessage(respBody)},
		{Kind: agentsim.ActionDone},
	}

	cfg := agentsim.RunnerConfig{
		Scenario:  scenario,
		Brain:     agentsim.NewScriptedBrain(script),
		Agent:     schema.AgentInfo{Name: "test-agent", UID: 60001, Status: schema.StatusRunning},
		BusSocket: socketPath,
		RunID:     "phase3-run",
		Attempt:   1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := agentsim.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.Verdict != schema.VerdictPass {
		t.Errorf("verdict = %s, want pass. fail reason: %s", result.Verdict, result.FailureReason)
	}
	for _, obs := range result.Observations {
		if !obs.Satisfied {
			t.Errorf("outcome %s not satisfied: %s", obs.OutcomeID, obs.Actual)
		}
	}
}

func TestRunner_CheckedInWorkerLifecycleScenario(t *testing.T) {
	// Load the checked-in scenario and script from disk, proving the
	// runner can execute a reusable Phase 3 protocol-path scenario
	// without an LLM. This is the CI-style smoke hook.
	socketPath, cleanup := testBus(t)
	defer cleanup()

	scenarioData, err := os.ReadFile("../../test/phase4/scenarios/worker_lifecycle.json")
	if err != nil {
		t.Fatalf("read scenario: %v", err)
	}
	var scenario schema.EmpiricalScenario
	if err := json.Unmarshal(scenarioData, &scenario); err != nil {
		t.Fatalf("parse scenario: %v", err)
	}

	scriptData, err := os.ReadFile("../../test/phase4/scripts/worker_lifecycle_script.json")
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	var script []agentsim.Action
	if err := json.Unmarshal(scriptData, &script); err != nil {
		t.Fatalf("parse script: %v", err)
	}

	// Publish the Phase 3 lifecycle events from another client.
	go func() {
		time.Sleep(30 * time.Millisecond)
		c := busClient(t, socketPath)
		defer c.Close()

		spawned, _ := json.Marshal(schema.AgentLifecycleEvent{
			Agent: schema.AgentInfo{
				Name: "worker-1", UID: 60002,
				Status: schema.StatusRunning,
				Slice:  "agent-60002.slice",
			},
		})
		c.Publish(schema.TopicAgentLifecycleSpawned, json.RawMessage(spawned))

		time.Sleep(10 * time.Millisecond)
		assigned, _ := json.Marshal(schema.WorkerLifecycleEvent{
			Lease: schema.WorkerLease{
				WorkerID:       "w1",
				AgentUID:       60002,
				Profile:        "coder",
				OwnerSessionID: "sess-1",
				State:          schema.LeaseRunning,
			},
		})
		c.Publish(schema.TopicAgentLifecycleAssigned, json.RawMessage(assigned))
	}()

	cfg := agentsim.RunnerConfig{
		Scenario:  scenario,
		Brain:     agentsim.NewScriptedBrain(script),
		Agent:     schema.AgentInfo{Name: "agent-sim-smoke", UID: 60010, Status: schema.StatusRunning},
		BusSocket: socketPath,
		RunID:     "checked-in-smoke",
		Attempt:   1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := agentsim.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.Verdict != schema.VerdictPass {
		t.Errorf("verdict = %s, want pass. fail reason: %s", result.Verdict, result.FailureReason)
		for _, obs := range result.Observations {
			t.Logf("  outcome %s: satisfied=%v actual=%s", obs.OutcomeID, obs.Satisfied, obs.Actual)
		}
	}
}

func TestPeerUIDAgent(t *testing.T) {
	agent := agentsim.PeerUIDAgent("test-agent")
	if agent.Name != "test-agent" {
		t.Errorf("name = %s, want test-agent", agent.Name)
	}
	if agent.UID == 0 {
		t.Error("uid should not be 0")
	}
	if agent.Status != schema.StatusRunning {
		t.Errorf("status = %s, want running", agent.Status)
	}
}
