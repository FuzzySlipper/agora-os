package main

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/r2worker"
	"github.com/patch/agora-os/internal/schema"
)

func TestRepoSearchHandler(t *testing.T) {
	ln, err := net.Listen("unix", "/tmp/test-repo-search.sock")
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

		inputs, _ := json.Marshal(repoSearchInputs{Query: "runner", Paths: []string{"."}})
		order := schema.WorkOrder{
			TaskID:    "task-search-1",
			Objective: "search for runner",
			Inputs:    inputs,
			Budget:    schema.WorkBudget{MaxSteps: 10, DeadlineSeconds: 30},
		}
		body, _ := json.Marshal(order)
		_ = enc.Encode(bus.Event{Topic: "agent.work.assign.worker_search", Body: body})

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
	client, err := bus.Dial("/tmp/test-repo-search.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	runner := r2worker.NewRunnerWithClient("worker_search", "repo-search", client, repoSearchHandler{})
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
		t.Fatal("expected at least one artifact")
	}
	// Parse artifact to verify structure
	var report searchReport
	if err := json.Unmarshal([]byte(res.Artifacts[0].Text), &report); err != nil {
		t.Fatalf("artifact not valid JSON: %v", err)
	}
	if report.TaskID != "task-search-1" {
		t.Errorf("expected task-search-1, got %s", report.TaskID)
	}
	if len(report.Results) < 2 {
		t.Fatalf("expected at least 2 tool results, got %d", len(report.Results))
	}
}

func TestRepoSearchHandlerBudgetExceeded(t *testing.T) {
	ln, err := net.Listen("unix", "/tmp/test-repo-search-budget.sock")
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

		inputs, _ := json.Marshal(repoSearchInputs{Query: "test", Paths: []string{"."}})
		order := schema.WorkOrder{
			TaskID:    "task-search-2",
			Objective: "search",
			Inputs:    inputs,
			Budget:    schema.WorkBudget{MaxSteps: 0, DeadlineSeconds: 1},
		}
		body, _ := json.Marshal(order)
		_ = enc.Encode(bus.Event{Topic: "agent.work.assign.worker_search2", Body: body})

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
	client, err := bus.Dial("/tmp/test-repo-search-budget.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	runner := r2worker.NewRunnerWithClient("worker_search2", "repo-search", client, repoSearchHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go runner.Run(ctx)

	var results []schema.WorkResult
	select {
	case results = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != schema.WorkStatusOK && results[0].Status != schema.WorkStatusFailed {
		t.Errorf("unexpected status: %s", results[0].Status)
	}
}
