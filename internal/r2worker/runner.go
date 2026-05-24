// Package r2worker provides a shared event-loop framework for R2 worker
// binaries. It handles event-bus subscription, work-order parsing, budget
// enforcement, and structured progress/result publication.
package r2worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

// WorkHandler is the profile-specific logic that processes a single work order.
type WorkHandler interface {
	// HandleWork is called with a context scoped to the work order budget.
	// The handler may call PublishProgress via the provided Runner.
	HandleWork(ctx context.Context, r *Runner, order schema.WorkOrder) error
}

// Runner wraps an event-bus client and provides the lifecycle for an R2 worker.
type Runner struct {
	client         *bus.Client
	workerID       string
	profile        string
	handler        WorkHandler
	busSocket      string
	replyTopic     string
	step           int
	maxSteps       int
	deadline       time.Time
	resultPublished bool
}

// NewRunner creates a Runner from environment variables set by the supervisor.
//
// Expected environment:
//   - AGORA_BUS_SOCKET  — path to the event bus Unix socket
//   - AGORA_WORKER_ID   — worker id (e.g. worker_1)
//   - AGORA_PROFILE     — profile name (e.g. repo-search)
//
// The assignment topic is derived from the worker id.
func NewRunner(handler WorkHandler) (*Runner, error) {
	busSocket := os.Getenv("AGORA_BUS_SOCKET")
	if busSocket == "" {
		busSocket = schema.BusSocket
	}
	workerID := os.Getenv("AGORA_WORKER_ID")
	if workerID == "" {
		return nil, fmt.Errorf("AGORA_WORKER_ID is required")
	}
	profile := os.Getenv("AGORA_PROFILE")
	if profile == "" {
		return nil, fmt.Errorf("AGORA_PROFILE is required")
	}

	client, err := bus.Dial(busSocket)
	if err != nil {
		return nil, fmt.Errorf("dial bus: %w", err)
	}

	return &Runner{
		client:   client,
		workerID: workerID,
		profile:  profile,
		handler:  handler,
	}, nil
}

// NewRunnerWithClient is a test-friendly constructor that accepts an
// existing bus client.
func NewRunnerWithClient(workerID, profile string, client *bus.Client, handler WorkHandler) *Runner {
	return &Runner{
		client:   client,
		workerID: workerID,
		profile:  profile,
		handler:  handler,
	}
}

// Run subscribes to the worker's assignment topic and blocks processing
// work orders until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	assignmentTopic := fmt.Sprintf("agent.work.assign.%s", r.workerID)
	if err := r.client.Subscribe(assignmentTopic); err != nil {
		return fmt.Errorf("subscribe %s: %w", assignmentTopic, err)
	}
	log.Printf("r2worker %s (%s) listening on %s", r.workerID, r.profile, assignmentTopic)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		ev, err := r.client.Receive()
		if err != nil {
			log.Printf("receive: %v", err)
			continue
		}

		if ev.Topic != assignmentTopic {
			continue
		}

		var order schema.WorkOrder
		if err := json.Unmarshal(ev.Body, &order); err != nil {
			log.Printf("unmarshal work order: %v", err)
			continue
		}

		if err := r.processWorkOrder(ctx, order); err != nil {
			log.Printf("process work order %s: %v", order.TaskID, err)
		}
	}
}

func (r *Runner) processWorkOrder(ctx context.Context, order schema.WorkOrder) error {
	// Reset per-order state
	r.step = 0
	r.maxSteps = 0
	r.deadline = time.Time{}
	r.replyTopic = order.ReplyTopic
	if r.replyTopic == "" {
		r.replyTopic = schema.TopicAgentWorkResult
	}

	budget := order.Budget
	if budget.MaxSteps > 0 {
		r.maxSteps = budget.MaxSteps
	}
	if budget.DeadlineSeconds > 0 {
		r.deadline = time.Now().Add(time.Duration(budget.DeadlineSeconds) * time.Second)
	}

	workCtx := ctx
	if !r.deadline.IsZero() {
		var cancel context.CancelFunc
		workCtx, cancel = context.WithDeadline(ctx, r.deadline)
		defer cancel()
	}

	r.PublishProgress(order.TaskID, "started", "work order received", 0)
	r.resultPublished = false

	err := r.handler.HandleWork(workCtx, r, order)

	if err != nil {
		if !r.resultPublished {
			if workCtx.Err() == context.DeadlineExceeded {
				r.PublishResult(order.TaskID, schema.WorkStatusFailed, "deadline exceeded", nil, nil, "")
				return fmt.Errorf("deadline exceeded for task %s", order.TaskID)
			}
			r.PublishResult(order.TaskID, schema.WorkStatusFailed, err.Error(), nil, nil, err.Error())
		}
		return err
	}

	if !r.resultPublished {
		r.PublishResult(order.TaskID, schema.WorkStatusOK, "completed", nil, nil, "")
	}
	return nil
}

// PublishProgress sends a progress event on the bus.
func (r *Runner) PublishProgress(taskID, stage, message string, step int) {
	r.step = step
	if r.maxSteps > 0 && step > r.maxSteps {
		log.Printf("warning: step %d exceeds max steps %d", step, r.maxSteps)
	}

	prog := schema.WorkProgress{
		TaskID:   taskID,
		Stage:    stage,
		Message:  message,
		Step:     step,
		MaxSteps: r.maxSteps,
	}

	topic := schema.TopicAgentWorkProgress
	if r.replyTopic != "" && r.replyTopic != schema.TopicAgentWorkResult {
		// If a custom reply topic is set, also emit progress there for
		// simple topic-routing integrations.
		_ = r.client.Publish(topic, prog)
		_ = r.client.Publish(r.replyTopic+".progress", prog)
		return
	}
	_ = r.client.Publish(topic, prog)
}

// PublishResult sends the final work result on the bus.
func (r *Runner) PublishResult(taskID string, status schema.WorkResultStatus, summary string, artifacts []schema.ArtifactRef, followUp []string, errStr string) {
	r.resultPublished = true
	result := schema.WorkResult{
		TaskID:    taskID,
		Status:    status,
		Summary:   summary,
		Artifacts: artifacts,
		FollowUp:  followUp,
		Error:     errStr,
	}
	_ = r.client.Publish(r.replyTopic, result)
}

// CheckBudget returns an error if the work order has exceeded its step or
// time budget. Handlers should call this before expensive work.
func (r *Runner) CheckBudget(ctx context.Context) error {
	if r.maxSteps > 0 && r.step >= r.maxSteps {
		return fmt.Errorf("step budget exhausted (%d/%d)", r.step, r.maxSteps)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled: %w", err)
	}
	return nil
}

// Client exposes the underlying bus client for direct use by handlers.
func (r *Runner) Client() *bus.Client {
	return r.client
}

// WorkerID returns the runner's worker id.
func (r *Runner) WorkerID() string { return r.workerID }

// Profile returns the runner's profile name.
func (r *Runner) Profile() string { return r.profile }
