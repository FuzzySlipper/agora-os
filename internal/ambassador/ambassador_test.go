package ambassador

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/llm"
	"github.com/patch/agora-os/internal/schema"
)

// ---------------------------------------------------------------------------
// Fake LLM
// ---------------------------------------------------------------------------

type fakeLLM struct {
	responses []llm.ChatCompletionResponse
	callCount int
	err       error
}

func (f *fakeLLM) ChatCompletion(ctx context.Context, messages []llm.ChatMessage) (*llm.ChatCompletionResponse, error) {
	f.callCount++
	if f.err != nil {
		return nil, f.err
	}
	if f.callCount-1 < len(f.responses) {
		return &f.responses[f.callCount-1], nil
	}
	// Default response: direct_answer
	return &llm.ChatCompletionResponse{
		Choices: []llm.Choice{{
			Message: llm.ChatMessage{Role: "assistant", Content: `{"action":"direct_answer"}`},
		}},
	}, nil
}

// ---------------------------------------------------------------------------
// Fake Supervisor
// ---------------------------------------------------------------------------

type fakeSupervisor struct {
	ensureWorkerResp  *schema.EnsureWorkerResponse
	ensureWorkerErr   error
	releaseWorkerResp *schema.ReleaseWorkerResponse
	listWorkersResp   *schema.ListWorkersResponse
	descProfilesResp  *schema.DescribeProfilesResponse
}

func (f *fakeSupervisor) EnsureWorker(req schema.EnsureWorkerRequest) (*schema.EnsureWorkerResponse, error) {
	if f.ensureWorkerErr != nil {
		return nil, f.ensureWorkerErr
	}
	if f.ensureWorkerResp != nil {
		return f.ensureWorkerResp, nil
	}
	return &schema.EnsureWorkerResponse{
		Assignment: schema.WorkerAssignment{
			WorkerID:        "worker_test_1",
			Created:         true,
			AssignmentTopic: "agent.work.assign.worker_test_1",
		},
	}, nil
}

func (f *fakeSupervisor) ReleaseWorker(req schema.ReleaseWorkerRequest) (*schema.ReleaseWorkerResponse, error) {
	if f.releaseWorkerResp != nil {
		return f.releaseWorkerResp, nil
	}
	return &schema.ReleaseWorkerResponse{Released: true}, nil
}

func (f *fakeSupervisor) ListWorkers(req schema.ListWorkersRequest) (*schema.ListWorkersResponse, error) {
	if f.listWorkersResp != nil {
		return f.listWorkersResp, nil
	}
	return &schema.ListWorkersResponse{}, nil
}

func (f *fakeSupervisor) DescribeProfiles() (*schema.DescribeProfilesResponse, error) {
	if f.descProfilesResp != nil {
		return f.descProfilesResp, nil
	}
	return &schema.DescribeProfilesResponse{}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func tmpDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ambassador-test-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func startFakeBus(t *testing.T, socketPath string) *bus.Broker {
	t.Helper()
	b := bus.NewBrokerWithOptions(false) // disable provenance enforcement for tests
	os.MkdirAll(filepath.Dir(socketPath), 0755)
	os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("bus listen: %v", err)
	}
	t.Cleanup(func() { ln.Close(); os.Remove(socketPath) })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = bus.ServeConn(conn, b)
			}()
		}
	}()

	// Wait for socket to be ready.
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	return b
}

// ---------------------------------------------------------------------------
// Ambassador lifecycle
// ---------------------------------------------------------------------------

func TestAmbassador_StartStop(t *testing.T) {
	dir := tmpDir(t)
	busSocket := filepath.Join(dir, "bus.sock")
	startFakeBus(t, busSocket)

	fakeLLM := &fakeLLM{}
	amb := New(Config{
		BusSocket:        busSocket,
		SupervisorSocket: filepath.Join(dir, "supervisor.sock"),
		LLMClient:        fakeLLM,
	})

	if err := amb.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	amb.Stop()
}

func TestAmbassador_Start_BusUnavailable(t *testing.T) {
	amb := New(Config{
		BusSocket:        "/nonexistent/bus.sock",
		SupervisorSocket: "/nonexistent/supervisor.sock",
		LLMClient:        &fakeLLM{},
	})

	err := amb.Start()
	if err == nil {
		t.Fatal("expected error when bus unavailable")
	}
	if !strings.Contains(err.Error(), "bus dial") {
		t.Errorf("error = %q, want bus dial error", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Turn classification
// ---------------------------------------------------------------------------

func TestParseClassification_Valid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want TurnAction
	}{
		{
			name: "direct_answer",
			raw:  `{"action":"direct_answer"}`,
			want: ActionDirectAnswer,
		},
		{
			name: "delegate",
			raw:  `{"action":"delegate","worker_requests":[{"profile":"repo-inspector","objective":"inspect"}]}`,
			want: ActionDelegate,
		},
		{
			name: "ask_followup",
			raw:  `{"action":"ask_followup","follow_up_question":"Which file?"}`,
			want: ActionAskFollowup,
		},
		{
			name: "escalate_admin",
			raw:  `{"action":"escalate_admin","admin_action":"delete user"}`,
			want: ActionEscalateAdmin,
		},
		{
			name: "with markdown fences",
			raw:  "```json\n{\"action\":\"direct_answer\"}\n```",
			want: ActionDirectAnswer,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseClassification(tc.raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Action != tc.want {
				t.Errorf("action = %q, want %q", got.Action, tc.want)
			}
		})
	}
}

func TestParseClassification_Invalid(t *testing.T) {
	_, err := parseClassification("not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestTurnClassification_Validate(t *testing.T) {
	cases := []struct {
		name string
		tc   *TurnClassification
		ok   bool
	}{
		{
			name: "direct ok",
			tc:   &TurnClassification{Action: ActionDirectAnswer},
			ok:   true,
		},
		{
			name: "delegate missing workers",
			tc:   &TurnClassification{Action: ActionDelegate},
			ok:   false,
		},
		{
			name: "delegate missing profile",
			tc:   &TurnClassification{Action: ActionDelegate, WorkerRequests: []WorkerRequest{{Objective: "x"}}},
			ok:   false,
		},
		{
			name: "delegate ok",
			tc:   &TurnClassification{Action: ActionDelegate, WorkerRequests: []WorkerRequest{{Profile: "p", Objective: "o"}}},
			ok:   true,
		},
		{
			name: "unknown action",
			tc:   &TurnClassification{Action: "unknown"},
			ok:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tc.Validate()
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Full turn processing
// ---------------------------------------------------------------------------

func TestAmbassador_ProcessTurn_DirectAnswer(t *testing.T) {
	dir := tmpDir(t)
	busSocket := filepath.Join(dir, "bus.sock")
	startFakeBus(t, busSocket)

	fakeLLM := &fakeLLM{
		responses: []llm.ChatCompletionResponse{
			// classification
			{Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: `{"action":"direct_answer"}`},
			}}},
			// direct answer
			{Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: "Hello, user!"},
			}}},
		},
	}

	amb := New(Config{
		BusSocket:        busSocket,
		SupervisorSocket: filepath.Join(dir, "supervisor.sock"),
		LLMClient:        fakeLLM,
	})
	amb.supervisorClient = &fakeSupervisor{}

	if err := amb.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer amb.Stop()
	// Small delay to ensure subscriptions are processed by the broker before publishing.
	time.Sleep(50 * time.Millisecond)

	// Set up subscriber BEFORE publishing.
	subscriber, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer subscriber.Close()
	if err := subscriber.Subscribe(schema.TopicConversationTurnResponded); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Publish a turn request.
	publisher, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer publisher.Close()

	turnReq := schema.ConversationTurnRequest{
		SessionID: "sess_01",
		TurnID:    "turn_01",
		Prompt:    "Say hello",
	}
	if err := publisher.Publish(schema.TopicConversationTurnRequested, turnReq); err != nil {
		t.Fatalf("publish turn: %v", err)
	}

	// Wait for response.
	var found bool
	deadline := time.After(2 * time.Second)
	for !found {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for turn response")
		default:
		}
		ev, err := subscriber.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if ev.Topic != schema.TopicConversationTurnResponded {
			continue
		}
		var resp schema.ConversationTurnResponse
		if err := json.Unmarshal(ev.Body, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.TurnID == "turn_01" && resp.Summary == "Hello, user!" {
			found = true
		}
	}

	if fakeLLM.callCount != 2 {
		t.Errorf("llm calls = %d, want 2", fakeLLM.callCount)
	}
}

func TestAmbassador_ProcessTurn_Delegate(t *testing.T) {
	dir := tmpDir(t)
	busSocket := filepath.Join(dir, "bus.sock")
	startFakeBus(t, busSocket)

	fakeLLM := &fakeLLM{
		responses: []llm.ChatCompletionResponse{
			// classification: delegate
			{Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: `{"action":"delegate","worker_requests":[{"profile":"repo-inspector","objective":"Find bugs","inputs":{}}]}`},
			}}},
		},
	}

	fakeSuper := &fakeSupervisor{}
	amb := New(Config{
		BusSocket:        busSocket,
		SupervisorSocket: filepath.Join(dir, "supervisor.sock"),
		LLMClient:        fakeLLM,
	})
	amb.supervisorClient = fakeSuper

	if err := amb.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer amb.Stop()
	// Small delay to ensure subscriptions are processed by the broker before publishing.
	time.Sleep(50 * time.Millisecond)

	// Set up subscriber BEFORE publishing.
	subscriber, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer subscriber.Close()
	assignmentTopic := "agent.work.assign.worker_test_1"
	if err := subscriber.Subscribe(assignmentTopic); err != nil {
		t.Fatalf("subscribe assignment topic: %v", err)
	}
	if err := subscriber.Subscribe(schema.TopicConversationTurnResponded); err != nil {
		t.Fatalf("subscribe responded: %v", err)
	}

	// Publish a turn request.
	publisher, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer publisher.Close()

	turnReq := schema.ConversationTurnRequest{
		SessionID: "sess_02",
		TurnID:    "turn_02",
		Prompt:    "Find bugs in the repo",
	}
	if err := publisher.Publish(schema.TopicConversationTurnRequested, turnReq); err != nil {
		t.Fatalf("publish turn: %v", err)
	}

	// Wait for assigned event.
	var assignedFound, ackFound bool
	deadline := time.After(2 * time.Second)
	for !(assignedFound && ackFound) {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for events; assigned=%v ack=%v", assignedFound, ackFound)
		default:
		}
		ev, err := subscriber.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		switch ev.Topic {
		case assignmentTopic:
			var order schema.WorkOrder
			if err := json.Unmarshal(ev.Body, &order); err != nil {
				t.Fatalf("unmarshal order: %v", err)
			}
			if order.Objective == "Find bugs" && order.WorkerProfile == "repo-inspector" {
				assignedFound = true
			}
		case schema.TopicConversationTurnResponded:
			var resp schema.ConversationTurnResponse
			if err := json.Unmarshal(ev.Body, &resp); err != nil {
				t.Fatalf("unmarshal resp: %v", err)
			}
			if resp.TurnID == "turn_02" && strings.Contains(resp.Summary, "assigned") {
				ackFound = true
			}
		}
	}
}

func TestAmbassador_ProcessTurn_AskFollowup(t *testing.T) {
	dir := tmpDir(t)
	busSocket := filepath.Join(dir, "bus.sock")
	startFakeBus(t, busSocket)

	fakeLLM := &fakeLLM{
		responses: []llm.ChatCompletionResponse{
			{Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: `{"action":"ask_followup","follow_up_question":"Which file do you mean?"}`},
			}}},
		},
	}

	amb := New(Config{
		BusSocket:        busSocket,
		SupervisorSocket: filepath.Join(dir, "supervisor.sock"),
		LLMClient:        fakeLLM,
	})
	amb.supervisorClient = &fakeSupervisor{}

	if err := amb.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer amb.Stop()
	// Small delay to ensure subscriptions are processed by the broker before publishing.
	time.Sleep(50 * time.Millisecond)

	// Set up subscriber BEFORE publishing.
	subscriber, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer subscriber.Close()
	if err := subscriber.Subscribe(schema.TopicConversationTurnResponded); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	publisher, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer publisher.Close()

	turnReq := schema.ConversationTurnRequest{
		SessionID: "sess_03",
		TurnID:    "turn_03",
		Prompt:    "Check that file",
	}
	if err := publisher.Publish(schema.TopicConversationTurnRequested, turnReq); err != nil {
		t.Fatalf("publish turn: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for turn response")
		default:
		}
		ev, err := subscriber.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if ev.Topic != schema.TopicConversationTurnResponded {
			continue
		}
		var resp schema.ConversationTurnResponse
		if err := json.Unmarshal(ev.Body, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.TurnID == "turn_03" && resp.Summary == "Which file do you mean?" {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Work result handling
// ---------------------------------------------------------------------------

func TestAmbassador_HandleWorkResult(t *testing.T) {
	dir := tmpDir(t)
	busSocket := filepath.Join(dir, "bus.sock")
	startFakeBus(t, busSocket)

	fakeLLM := &fakeLLM{
		responses: []llm.ChatCompletionResponse{
			// classification: delegate
			{Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: `{"action":"delegate","worker_requests":[{"profile":"repo-inspector","objective":"Find bugs","inputs":{}}]}`},
			}}},
			// synthesis
			{Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: "I found 3 bugs."},
			}}},
		},
	}

	amb := New(Config{
		BusSocket:        busSocket,
		SupervisorSocket: filepath.Join(dir, "supervisor.sock"),
		LLMClient:        fakeLLM,
	})
	amb.supervisorClient = &fakeSupervisor{}

	if err := amb.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer amb.Stop()
	// Small delay to ensure subscriptions are processed by the broker before publishing.
	time.Sleep(50 * time.Millisecond)

	// Set up subscriber BEFORE publishing.
	subscriber, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer subscriber.Close()
	if err := subscriber.Subscribe(schema.TopicConversationTurnResponded); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// First send a turn to set up pending work.
	publisher, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer publisher.Close()

	turnReq := schema.ConversationTurnRequest{
		SessionID: "sess_04",
		TurnID:    "turn_04",
		Prompt:    "Find bugs",
	}
	if err := publisher.Publish(schema.TopicConversationTurnRequested, turnReq); err != nil {
		t.Fatalf("publish turn: %v", err)
	}

	// Wait a bit for delegation to happen.
	time.Sleep(200 * time.Millisecond)

	// Find the pending task.
	amb.pendingMu.Lock()
	var taskID string
	for tid := range amb.pending {
		taskID = tid
		break
	}
	amb.pendingMu.Unlock()

	if taskID == "" {
		t.Fatal("no pending task found")
	}

	// Publish a work result for that task.
	result := schema.WorkResult{
		TaskID:  taskID,
		Status:  schema.WorkStatusOK,
		Summary: "3 bugs found",
	}
	if err := publisher.Publish(fmt.Sprintf("%s.%s", schema.TopicAgentWorkResult, taskID), result); err != nil {
		t.Fatalf("publish result: %v", err)
	}

	// Wait for synthesized response.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for synthesized response")
		default:
		}
		ev, err := subscriber.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if ev.Topic != schema.TopicConversationTurnResponded {
			continue
		}
		var resp schema.ConversationTurnResponse
		if err := json.Unmarshal(ev.Body, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.TurnID == "turn_04" && resp.Summary == "I found 3 bugs." {
			return
		}
	}
}

func TestAmbassador_HandleWorkNeeds3PO(t *testing.T) {
	dir := tmpDir(t)
	busSocket := filepath.Join(dir, "bus.sock")
	startFakeBus(t, busSocket)

	fakeLLM := &fakeLLM{
		responses: []llm.ChatCompletionResponse{
			// classification: delegate
			{Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: `{"action":"delegate","worker_requests":[{"profile":"repo-inspector","objective":"Find bugs","inputs":{}}]}`},
			}}},
		},
	}

	amb := New(Config{
		BusSocket:        busSocket,
		SupervisorSocket: filepath.Join(dir, "supervisor.sock"),
		LLMClient:        fakeLLM,
	})
	amb.supervisorClient = &fakeSupervisor{}

	if err := amb.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer amb.Stop()
	// Small delay to ensure subscriptions are processed by the broker before publishing.
	time.Sleep(50 * time.Millisecond)

	// Set up pending work directly.
	amb.pendingMu.Lock()
	amb.pending["task_test_1"] = &PendingWork{
		TurnID:    "turn_05",
		SessionID: "sess_05",
		WorkerID:  "worker_test_1",
		Profile:   "repo-inspector",
		Objective: "Find bugs",
	}
	amb.pendingMu.Unlock()

	// Set up subscriber BEFORE publishing.
	subscriber, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer subscriber.Close()
	if err := subscriber.Subscribe(schema.TopicConversationTurnResponded); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	publisher, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer publisher.Close()

	needs3po := schema.WorkNeeds3PO{
		TaskID:  "task_test_1",
		Reason:  "ambiguous file path",
		Summary: "Multiple files match the pattern",
	}
	if err := publisher.Publish(schema.TopicAgentWorkNeeds3PO, needs3po); err != nil {
		t.Fatalf("publish needs3po: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for needs_3po response")
		default:
		}
		ev, err := subscriber.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if ev.Topic != schema.TopicConversationTurnResponded {
			continue
		}
		var resp schema.ConversationTurnResponse
		if err := json.Unmarshal(ev.Body, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.TurnID == "turn_05" && strings.Contains(resp.Summary, "paused") {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Supervisor client
// ---------------------------------------------------------------------------

func TestSupervisorClientImpl_EnsureWorker(t *testing.T) {
	dir := tmpDir(t)
	supervisorSocket := filepath.Join(dir, "supervisor.sock")

	// Start a fake supervisor server.
	os.MkdirAll(filepath.Dir(supervisorSocket), 0755)
	os.Remove(supervisorSocket)
	ln, err := net.Listen("unix", supervisorSocket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req schema.Request
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}
				if req.Method == "ensure_worker" {
					resp := schema.EnsureWorkerResponse{
						Assignment: schema.WorkerAssignment{
							WorkerID:        "worker_42",
							Created:         true,
							AssignmentTopic: "agent.work.assign.worker_42",
						},
					}
					body, _ := json.Marshal(resp)
					_ = json.NewEncoder(c).Encode(schema.Response{OK: true, Body: body})
				}
			}(conn)
		}
	}()

	client := NewSupervisorClient(supervisorSocket)
	resp, err := client.EnsureWorker(schema.EnsureWorkerRequest{
		SessionID:     "sess_1",
		RequestID:     "req_1",
		WorkerProfile: "repo-inspector",
		Objective:     "test",
	})
	if err != nil {
		t.Fatalf("ensure worker: %v", err)
	}
	if resp.Assignment.WorkerID != "worker_42" {
		t.Errorf("worker id = %q, want worker_42", resp.Assignment.WorkerID)
	}
}

func TestSupervisorClientImpl_Error(t *testing.T) {
	dir := tmpDir(t)
	supervisorSocket := filepath.Join(dir, "supervisor.sock")

	os.MkdirAll(filepath.Dir(supervisorSocket), 0755)
	os.Remove(supervisorSocket)
	ln, err := net.Listen("unix", supervisorSocket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req schema.Request
				_ = json.NewDecoder(c).Decode(&req)
				body, _ := json.Marshal("profile not allowed")
				_ = json.NewEncoder(c).Encode(schema.Response{OK: false, Body: body})
			}(conn)
		}
	}()

	client := NewSupervisorClient(supervisorSocket)
	_, err = client.EnsureWorker(schema.EnsureWorkerRequest{
		SessionID:     "sess_1",
		RequestID:     "req_1",
		WorkerProfile: "repo-inspector",
		Objective:     "test",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "profile not allowed") {
		t.Errorf("error = %q, want profile not allowed", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Prompt builders
// ---------------------------------------------------------------------------

func TestBuildClassificationPrompt(t *testing.T) {
	req := schema.ConversationTurnRequest{Prompt: "hello"}
	prompt := buildClassificationPrompt(req)
	if !strings.Contains(prompt, "hello") {
		t.Error("prompt should contain user prompt")
	}
	if !strings.Contains(prompt, "direct_answer") {
		t.Error("prompt should mention direct_answer")
	}
}

func TestBuildSynthesisPrompt(t *testing.T) {
	work := &PendingWork{
		Objective: "Find bugs",
		Profile:   "repo-inspector",
		Result: &schema.WorkResult{
			Status:  schema.WorkStatusOK,
			Summary: "3 bugs found",
			Artifacts: []schema.ArtifactRef{
				{Kind: schema.ArtifactFile, Path: "/tmp/bugs.txt", Text: "bug list"},
			},
			FollowUp: []string{"fix them"},
		},
	}
	prompt := buildSynthesisPrompt(work)
	if !strings.Contains(prompt, "Find bugs") {
		t.Error("prompt should contain objective")
	}
	if !strings.Contains(prompt, "3 bugs found") {
		t.Error("prompt should contain summary")
	}
	if !strings.Contains(prompt, "bug list") {
		t.Error("prompt should contain artifact text")
	}
}

// ---------------------------------------------------------------------------
// Error paths
// ---------------------------------------------------------------------------

func TestAmbassador_ProcessTurn_ClassifyError(t *testing.T) {
	dir := tmpDir(t)
	busSocket := filepath.Join(dir, "bus.sock")
	startFakeBus(t, busSocket)

	fakeLLM := &fakeLLM{err: errors.New("llm unavailable")}

	amb := New(Config{
		BusSocket:        busSocket,
		SupervisorSocket: filepath.Join(dir, "supervisor.sock"),
		LLMClient:        fakeLLM,
	})
	amb.supervisorClient = &fakeSupervisor{}

	if err := amb.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer amb.Stop()
	// Small delay to ensure subscriptions are processed by the broker before publishing.
	time.Sleep(50 * time.Millisecond)

	// Set up subscriber BEFORE publishing.
	subscriber, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer subscriber.Close()
	if err := subscriber.Subscribe(schema.TopicConversationTurnResponded); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	publisher, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer publisher.Close()

	turnReq := schema.ConversationTurnRequest{
		SessionID: "sess_err",
		TurnID:    "turn_err",
		Prompt:    "hello",
	}
	if err := publisher.Publish(schema.TopicConversationTurnRequested, turnReq); err != nil {
		t.Fatalf("publish turn: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for error response")
		default:
		}
		ev, err := subscriber.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if ev.Topic != schema.TopicConversationTurnResponded {
			continue
		}
		var resp schema.ConversationTurnResponse
		if err := json.Unmarshal(ev.Body, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.TurnID == "turn_err" && strings.Contains(resp.Summary, "Error") {
			return
		}
	}
}

func TestAmbassador_ClassifyTurn_RejectsInvalidClassification(t *testing.T) {
	fakeLLM := &fakeLLM{
		responses: []llm.ChatCompletionResponse{
			{Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: `{"action":"delegate","worker_requests":[{"objective":"missing profile"}]}`},
			}}},
		},
	}

	amb := New(Config{
		BusSocket:        "/tmp/unused-bus.sock",
		SupervisorSocket: "/tmp/unused-supervisor.sock",
		LLMClient:        fakeLLM,
	})

	_, err := amb.classifyTurn(context.Background(), schema.ConversationTurnRequest{
		SessionID: "sess_invalid",
		TurnID:    "turn_invalid",
		Prompt:    "delegate without a worker profile",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "invalid turn classification") {
		t.Fatalf("expected invalid classification error, got %v", err)
	}
}

func TestAmbassador_ProcessTurn_Delegate_SupervisorError(t *testing.T) {
	dir := tmpDir(t)
	busSocket := filepath.Join(dir, "bus.sock")
	startFakeBus(t, busSocket)

	fakeLLM := &fakeLLM{
		responses: []llm.ChatCompletionResponse{
			{Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: `{"action":"delegate","worker_requests":[{"profile":"repo-inspector","objective":"Find bugs","inputs":{}}]}`},
			}}},
		},
	}

	fakeSuper := &fakeSupervisor{ensureWorkerErr: errors.New("supervisor down")}
	amb := New(Config{
		BusSocket:        busSocket,
		SupervisorSocket: filepath.Join(dir, "supervisor.sock"),
		LLMClient:        fakeLLM,
	})
	amb.supervisorClient = fakeSuper

	if err := amb.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer amb.Stop()
	// Small delay to ensure subscriptions are processed by the broker before publishing.
	time.Sleep(50 * time.Millisecond)

	// Set up subscriber BEFORE publishing.
	subscriber, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer subscriber.Close()
	if err := subscriber.Subscribe(schema.TopicConversationTurnResponded); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	publisher, err := bus.Dial(busSocket)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer publisher.Close()

	turnReq := schema.ConversationTurnRequest{
		SessionID: "sess_de",
		TurnID:    "turn_de",
		Prompt:    "Find bugs",
	}
	if err := publisher.Publish(schema.TopicConversationTurnRequested, turnReq); err != nil {
		t.Fatalf("publish turn: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for error response")
		default:
		}
		ev, err := subscriber.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if ev.Topic != schema.TopicConversationTurnResponded {
			continue
		}
		var resp schema.ConversationTurnResponse
		if err := json.Unmarshal(ev.Body, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.TurnID == "turn_de" && strings.Contains(resp.Summary, "Error") {
			return
		}
	}
}
