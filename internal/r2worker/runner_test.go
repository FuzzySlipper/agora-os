package r2worker

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

// fakeHandler records work orders and allows controlled responses.
type fakeHandler struct {
	orders    []schema.WorkOrder
	progress  []schema.WorkProgress
	results   []schema.WorkResult
	returnErr error
}

func (h *fakeHandler) HandleWork(ctx context.Context, r *Runner, order schema.WorkOrder) error {
	h.orders = append(h.orders, order)
	if h.returnErr != nil {
		return h.returnErr
	}
	r.PublishProgress(order.TaskID, "stage1", "msg", 1)
	return nil
}

// TestRunnerProcessesWorkOrder tests that a runner receives a work order,
// invokes the handler, and publishes a result.
func TestRunnerProcessesWorkOrder(t *testing.T) {
	// Create a temporary Unix socket for the broker side.
	ln, err := net.Listen("unix", "/tmp/test-r2worker.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	handler := &fakeHandler{}

	// Server-side goroutine simulating the bus broker.
	var enc *json.Encoder
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		enc = json.NewEncoder(conn)
		dec := json.NewDecoder(conn)

		// Expect subscription message
		var sub bus.ClientMsg
		if err := dec.Decode(&sub); err != nil {
			return
		}
		if sub.Op != bus.OpSub {
			t.Errorf("expected sub op, got %s", sub.Op)
		}

		// Send a work order event
		order := schema.WorkOrder{
			TaskID:   "task-1",
			Objective: "test objective",
			Inputs:   json.RawMessage(`{"query":"foo"}`),
			Budget:   schema.WorkBudget{MaxSteps: 5, DeadlineSeconds: 60},
		}
		body, _ := json.Marshal(order)
		_ = enc.Encode(bus.Event{Topic: "agent.work.assign.worker_1", Body: body})

		// Wait for published messages from the runner
		for {
			var msg bus.ClientMsg
			if err := dec.Decode(&msg); err != nil {
				return
			}
			if msg.Op != bus.OpPub {
				continue
			}
			var bodyData json.RawMessage = msg.Body
			switch msg.Topic {
			case schema.TopicAgentWorkProgress:
				var p schema.WorkProgress
				_ = json.Unmarshal(bodyData, &p)
				handler.progress = append(handler.progress, p)
			case schema.TopicAgentWorkResult:
				var r schema.WorkResult
				_ = json.Unmarshal(bodyData, &r)
				handler.results = append(handler.results, r)
				return
			}
		}
	}()

	// Give server time to start
	time.Sleep(10 * time.Millisecond)

	client, err := bus.Dial("/tmp/test-r2worker.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	runner := NewRunnerWithClient("worker_1", "repo-search", client, handler)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run the event loop in a goroutine; it will block until context cancelled
	go runner.Run(ctx)

	// Wait for processing
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server")
	}

	if len(handler.orders) != 1 {
		t.Fatalf("expected 1 work order, got %d", len(handler.orders))
	}
	if handler.orders[0].TaskID != "task-1" {
		t.Errorf("expected task-1, got %s", handler.orders[0].TaskID)
	}

	if len(handler.results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(handler.results))
	}
	res := handler.results[0]
	if res.Status != schema.WorkStatusOK {
		t.Errorf("expected status ok, got %s", res.Status)
	}
	if len(handler.progress) < 1 {
		t.Fatalf("expected at least 1 progress event, got %d", len(handler.progress))
	}
}

func TestRunnerRespectsDeadline(t *testing.T) {
	ln, err := net.Listen("unix", "/tmp/test-r2worker-deadline.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	handler := &fakeHandler{
		returnErr: context.DeadlineExceeded,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		enc := json.NewEncoder(conn)
		dec := json.NewDecoder(conn)

		var sub bus.ClientMsg
		_ = dec.Decode(&sub)

		order := schema.WorkOrder{
			TaskID:   "task-deadline",
			Objective: "slow task",
			Budget:   schema.WorkBudget{DeadlineSeconds: 1},
		}
		body, _ := json.Marshal(order)
		_ = enc.Encode(bus.Event{Topic: "agent.work.assign.worker_deadline", Body: body})

		for {
			var msg bus.ClientMsg
			if err := dec.Decode(&msg); err != nil {
				return
			}
			if msg.Op == bus.OpPub && msg.Topic == schema.TopicAgentWorkResult {
				var r schema.WorkResult
				_ = json.Unmarshal(msg.Body, &r)
				handler.results = append(handler.results, r)
				return
			}
		}
	}()

	time.Sleep(10 * time.Millisecond)
	client, err := bus.Dial("/tmp/test-r2worker-deadline.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	runner := NewRunnerWithClient("worker_deadline", "repo-inspector", client, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go runner.Run(ctx)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	if len(handler.results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(handler.results))
	}
	if handler.results[0].Status != schema.WorkStatusFailed {
		t.Errorf("expected failed status, got %s", handler.results[0].Status)
	}
}
