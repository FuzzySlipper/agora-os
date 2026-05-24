package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/llm"
	"github.com/patch/agora-os/internal/r2worker"
	"github.com/patch/agora-os/internal/schema"
)

// mockLLMTransport returns a fixed chat-completion response.
type mockLLMTransport struct {
	responseBody string
}

func (t *mockLLMTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(t.responseBody))),
	}, nil
}

func newMockLLMHandler(resp string) *repoInspectorHandler {
	hc := &http.Client{Transport: &mockLLMTransport{responseBody: resp}}
	return newRepoInspectorHandler(llm.WithHTTPClient(hc))
}

func TestRepoInspectorHandler(t *testing.T) {
	// Create a temporary file for the handler to read
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "sample.go")
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc Hello() string { return \"hi\" }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mockResp, _ := json.Marshal(llm.ChatCompletionResponse{
		Choices: []llm.Choice{
			{Message: llm.ChatMessage{Role: "assistant", Content: "The file defines a Hello function."}},
		},
	})
	handler := newMockLLMHandler(string(mockResp))

	ln, err := net.Listen("unix", "/tmp/test-repo-inspector.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan []schema.WorkResult, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		enc := json.NewEncoder(conn)
		dec := json.NewDecoder(conn)

		var sub bus.ClientMsg
		_ = dec.Decode(&sub)

		inputs, _ := json.Marshal(repoInspectorInputs{
			TargetPaths: []string{testFile},
			Question:    "What does this file do?",
		})
		order := schema.WorkOrder{
			TaskID:    "task-inspect-1",
			Objective: "inspect file",
			Inputs:    inputs,
			Budget:    schema.WorkBudget{MaxSteps: 10, DeadlineSeconds: 30},
		}
		body, _ := json.Marshal(order)
		_ = enc.Encode(bus.Event{Topic: "agent.work.assign.worker_inspect", Body: body})

		var results []schema.WorkResult
		for {
			var msg bus.ClientMsg
			if err := dec.Decode(&msg); err != nil {
				break
			}
			if msg.Op == bus.OpPub && msg.Topic == schema.TopicAgentWorkResult {
				var r schema.WorkResult
				_ = json.Unmarshal(msg.Body, &r)
				results = append(results, r)
				break
			}
		}
		done <- results
	}()

	time.Sleep(10 * time.Millisecond)
	client, err := bus.Dial("/tmp/test-repo-inspector.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	runner := r2worker.NewRunnerWithClient("worker_inspect", "repo-inspector", client, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go runner.Run(ctx)

	var results []schema.WorkResult
	select {
	case results = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	res := results[0]
	if res.Status != schema.WorkStatusOK {
		t.Errorf("expected ok, got %s: %s", res.Status, res.Error)
	}
	if len(res.Artifacts) == 0 {
		t.Logf("result dump: %+v", res)
		t.Fatal("expected at least one artifact")
	}
	var report inspectionReport
	if err := json.Unmarshal([]byte(res.Artifacts[0].Text), &report); err != nil {
		t.Fatalf("artifact not valid JSON: %v", err)
	}
	if report.TaskID != "task-inspect-1" {
		t.Errorf("expected task-inspect-1, got %s", report.TaskID)
	}
	if report.Analysis != "The file defines a Hello function." {
		t.Errorf("unexpected analysis: %s", report.Analysis)
	}
}

func TestRepoInspectorHandlerMissingQuestion(t *testing.T) {
	mockResp, _ := json.Marshal(llm.ChatCompletionResponse{
		Choices: []llm.Choice{
			{Message: llm.ChatMessage{Role: "assistant", Content: ""}},
		},
	})
	handler := newMockLLMHandler(string(mockResp))

	ln, err := net.Listen("unix", "/tmp/test-repo-inspector-missing.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan []schema.WorkResult, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		enc := json.NewEncoder(conn)
		dec := json.NewDecoder(conn)

		var sub bus.ClientMsg
		_ = dec.Decode(&sub)

		inputs, _ := json.Marshal(repoInspectorInputs{TargetPaths: []string{"."}})
		order := schema.WorkOrder{
			TaskID:    "task-inspect-2",
			Objective: "inspect without question",
			Inputs:    inputs,
			Budget:    schema.WorkBudget{MaxSteps: 5, DeadlineSeconds: 10},
		}
		body, _ := json.Marshal(order)
		_ = enc.Encode(bus.Event{Topic: "agent.work.assign.worker_inspect2", Body: body})

		var results []schema.WorkResult
		for {
			var msg bus.ClientMsg
			if err := dec.Decode(&msg); err != nil {
				break
			}
			if msg.Op == bus.OpPub && msg.Topic == schema.TopicAgentWorkResult {
				var r schema.WorkResult
				_ = json.Unmarshal(msg.Body, &r)
				results = append(results, r)
				break
			}
		}
		done <- results
	}()

	time.Sleep(10 * time.Millisecond)
	client, err := bus.Dial("/tmp/test-repo-inspector-missing.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	runner := r2worker.NewRunnerWithClient("worker_inspect2", "repo-inspector", client, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go runner.Run(ctx)

	var results []schema.WorkResult
	select {
	case results = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != schema.WorkStatusFailed {
		t.Errorf("expected failed, got %s", results[0].Status)
	}
}
