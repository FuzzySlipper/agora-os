package admin

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

// testEscalationRequest returns a minimal EscalationRequest for testing.
func testEscalationRequest() schema.EscalationRequest {
	return schema.EscalationRequest{
		AgentUID:          60001,
		TaskContext:       "test task",
		RequestedAction:   "read",
		RequestedResource: "/etc/passwd",
		Justification:     "need to check config",
	}
}

func TestEvaluate_TimeoutDefaultsToEscalate(t *testing.T) {
	// Server that delays longer than the timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(200)
		fmt.Fprint(w, `{"content":[{"text":"{\"decision\":\"approve\"}"}]}`)
	}))
	defer srv.Close()

	logPath := filepath.Join(t.TempDir(), "admin.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a := New(Config{
		SystemPrompt: "test",
		APIKey:       "test-key",
		APIURL:       srv.URL,
		LogFile:      logFile,
		LLMTimeout:   500 * time.Millisecond, // very short timeout
	})

	req := testEscalationRequest()
	resp := a.evaluate(req)

	if resp.Decision != schema.DecisionEscalate {
		t.Errorf("expected DecisionEscalate on timeout, got %q", resp.Decision)
	}
	if !strings.Contains(resp.Reasoning, "LLM evaluation failed") {
		t.Errorf("expected reasoning about LLM failure, got %q", resp.Reasoning)
	}
}

func TestEvaluate_LLMErrorDefaultsToEscalate(t *testing.T) {
	// Server that returns a 500 error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":"internal"}`)
	}))
	defer srv.Close()

	logPath := filepath.Join(t.TempDir(), "admin.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a := New(Config{
		SystemPrompt: "test",
		APIKey:       "test-key",
		APIURL:       srv.URL,
		LogFile:      logFile,
	})

	req := testEscalationRequest()
	resp := a.evaluate(req)

	if resp.Decision != schema.DecisionEscalate {
		t.Errorf("expected DecisionEscalate on API error, got %q", resp.Decision)
	}
}

func TestEvaluate_SuccessfulResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"content":[{"text":"{\"decision\":\"deny\",\"reasoning\":\"too risky\"}"}]}`)
	}))
	defer srv.Close()

	logPath := filepath.Join(t.TempDir(), "admin.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a := New(Config{
		SystemPrompt: "test",
		APIKey:       "test-key",
		APIURL:       srv.URL,
		LogFile:      logFile,
	})

	req := testEscalationRequest()
	resp := a.evaluate(req)

	if resp.Decision != schema.DecisionDeny {
		t.Errorf("expected DecisionDeny, got %q", resp.Decision)
	}
	if resp.Reasoning != "too risky" {
		t.Errorf("expected reasoning 'too risky', got %q", resp.Reasoning)
	}
}

// errWriter is a writer that always returns an error.
type errWriter struct{}

func TestLogEntry_WriteError(t *testing.T) {
	// Set up a test server that returns a valid LLM response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"content":[{"text":"{\"decision\":\"approve\",\"reasoning\":\"ok\"}"}]}`)
	}))
	defer srv.Close()

	// Create a pipe and close the read end so log writes fail.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()

	a := New(Config{
		SystemPrompt: "test",
		APIKey:       "test-key",
		APIURL:       srv.URL,
		LogFile:      w,
	})

	// Test logEntry failure directly — HandleConn requires SO_PEERCRED (root).
	req := testEscalationRequest()
	resp := a.evaluate(req)
	_, _, err = a.logEntry(req, resp)
	if err == nil {
		t.Fatal("expected logEntry to fail")
	}

	// Verify the error mentions durability (write or sync failure).
	if !strings.Contains(err.Error(), "log entry durability") {
		t.Errorf("expected 'log entry durability' error, got %q", err.Error())
	}
}

func TestLogEntry_Success(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "admin.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a := &Agent{
		logFile:   logFile,
		llmClient: &http.Client{Timeout: 30 * time.Second},
	}

	req := testEscalationRequest()
	resp := schema.EscalationResponse{
		Decision:  schema.DecisionEscalate,
		Reasoning: "test",
	}

	id, _, err := a.logEntry(req, resp)
	if err != nil {
		t.Fatalf("logEntry failed: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty event id")
	}

	// Verify the log file contains a valid JSON entry.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("log file is empty after logEntry")
	}

	var entry struct {
		Timestamp time.Time
		Request   schema.EscalationRequest
		Response  schema.EscalationResponse
	}
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil { // strip trailing newline
		t.Fatalf("unmarshal log entry: %v", err)
	}
	if entry.Request.AgentUID != req.AgentUID {
		t.Errorf("expected agent_uid %d, got %d", req.AgentUID, entry.Request.AgentUID)
	}
	if entry.Response.Decision != resp.Decision {
		t.Errorf("expected decision %q, got %q", resp.Decision, entry.Response.Decision)
	}
}

func TestLLMTimeout_DefaultsTo30s(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "admin.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a := New(Config{
		SystemPrompt: "test",
		APIKey:       "test-key",
		LogFile:      logFile,
		// LLMTimeout not set — should default to 30s
	})

	if a.llmClient.Timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", a.llmClient.Timeout)
	}
}

func TestLLMTimeout_CustomValue(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "admin.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a := New(Config{
		SystemPrompt: "test",
		APIKey:       "test-key",
		LogFile:      logFile,
		LLMTimeout:   10 * time.Second,
	})

	if a.llmClient.Timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", a.llmClient.Timeout)
	}
}

// TestCallLLM_TimeoutReached tests that callLLM returns an error on timeout.
func TestCallLLM_TimeoutReached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(200)
		fmt.Fprint(w, `{"content":[{"text":"ok"}]}`)
	}))
	defer srv.Close()

	logPath := filepath.Join(t.TempDir(), "admin.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a := New(Config{
		SystemPrompt: "test",
		APIKey:       "test-key",
		APIURL:       srv.URL,
		LogFile:      logFile,
		LLMTimeout:   100 * time.Millisecond,
	})

	_, err = a.callLLM("test prompt")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// TestHandleConn_Integration tests the full flow with a real Unix socket.
// This requires SO_PEERCRED, so we use a real Unix socket pair.
func TestHandleConn_LogFailureNoSuccessResponse(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("SO_PEERCRED test requires root")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"content":[{"text":"{\"decision\":\"approve\",\"reasoning\":\"ok\"}"}]}`)
	}))
	defer srv.Close()

	// Create a pipe and close read end so writes fail.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()

	a := New(Config{
		SystemPrompt: "test",
		APIKey:       "test-key",
		APIURL:       srv.URL,
		LogFile:      w,
	})

	// Create a real Unix socket pair.
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var respReceived atomic.Value
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			a.HandleConn(conn)
		}
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Send escalation request.
	escReq := testEscalationRequest()
	body, _ := json.Marshal(escReq)
	req := schema.Request{
		Method: schema.MethodEscalate,
		Body:   body,
	}
	json.NewEncoder(client).Encode(req)

	// Read response.
	var resp schema.Response
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	respReceived.Store(resp)

	// When log write fails, OK should be false.
	if resp.OK {
		t.Error("expected OK=false when log write fails, got OK=true")
	}
}

func TestPublishPending(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "bus.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	broker := bus.NewBrokerWithOptions(false)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_ = bus.ServeConn(c, broker)
			}(conn)
		}
	}()

	subscriber, err := bus.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Close()

	if err := subscriber.Subscribe("admin.escalation.pending"); err != nil {
		t.Fatal(err)
	}
	// Give the broker time to register the subscription.
	time.Sleep(50 * time.Millisecond)

	a := &Agent{busSocket: sock}
	req := testEscalationRequest()
	resp := schema.EscalationResponse{
		Decision:  schema.DecisionEscalate,
		Reasoning: "needs human review",
	}

	a.publishPending("test-pending-id", req, resp)

	evCh := make(chan bus.Event, 1)
	errCh := make(chan error, 1)
	go func() {
		ev, err := subscriber.Receive()
		if err != nil {
			errCh <- err
			return
		}
		evCh <- ev
	}()

	select {
	case err := <-errCh:
		t.Fatalf("Receive: %v", err)
	case ev := <-evCh:
		if ev.Topic != schema.TopicAdminEscalationPending {
			t.Errorf("got topic %q, want %q", ev.Topic, schema.TopicAdminEscalationPending)
		}
		var event schema.AdminEscalationEvent
		if err := json.Unmarshal(ev.Body, &event); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if event.ID != "test-pending-id" {
			t.Errorf("got id %q, want test-pending-id", event.ID)
		}
		if event.Response.Decision != schema.DecisionEscalate {
			t.Errorf("got decision %q, want escalate", event.Response.Decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pending event")
	}
}

func TestLogHumanDecision(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "admin.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a := &Agent{logFile: logFile}

	decision := schema.HumanEscalationDecision{
		ID:          "decision-id",
		Timestamp:   time.Now(),
		ReviewedBy:  0,
		Decision:    schema.DecisionApprove,
		Constraints: []string{"pointer"},
		Notes:       "approved with constraint",
		Request:     testEscalationRequest(),
		Response:    schema.EscalationResponse{Decision: schema.DecisionEscalate, Reasoning: "test"},
	}

	if err := a.LogHumanDecision(decision); err != nil {
		t.Fatalf("LogHumanDecision failed: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	line := strings.TrimSpace(string(data))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatalf("unmarshal log line: %v", err)
	}
	if _, ok := raw["decision"]; !ok {
		t.Error("expected log entry to contain 'decision' field")
	}
}

func TestLogHumanDecisionDeduplicatesDecisionID(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "admin.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a := &Agent{logFile: logFile}
	decision := schema.HumanEscalationDecision{
		ID:         "decision-id",
		Timestamp:  time.Now(),
		ReviewedBy: 0,
		Decision:   schema.DecisionApprove,
		Request:    testEscalationRequest(),
		Response:   schema.EscalationResponse{Decision: schema.DecisionEscalate, Reasoning: "test"},
	}

	if err := a.LogHumanDecision(decision); err != nil {
		t.Fatalf("first LogHumanDecision failed: %v", err)
	}
	if err := a.LogHumanDecision(decision); err != nil {
		t.Fatalf("duplicate LogHumanDecision failed: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if got, want := len(lines), 1; got != want {
		t.Fatalf("duplicate decision log entries = %d, want %d; log:\n%s", got, want, data)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &raw); err != nil {
		t.Fatalf("unmarshal log line: %v", err)
	}
	var entryID string
	if err := json.Unmarshal(raw["id"], &entryID); err != nil {
		t.Fatalf("log entry id missing or invalid: %v", err)
	}
	if entryID != decision.ID {
		t.Fatalf("log entry id = %q, want %q", entryID, decision.ID)
	}
}
