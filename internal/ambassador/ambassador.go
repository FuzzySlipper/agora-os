// Package ambassador implements the 3PO ambassador: the human-facing frontier
// agent that receives conversation turns from the shell UI, classifies them,
// delegates to R2 workers via the supervisor, and synthesizes results back
// into human-readable responses.
//
// The ambassador is intentionally not privileged. It does not create Linux
// users, does not talk to systemd directly, and does not bypass the admin
// agent. All worker lifecycle requests flow through the deterministic
// agent-supervisor service.
package ambassador

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/llm"
	"github.com/patch/agora-os/internal/schema"
)

// LLMClient is the subset of llm.Client used by the ambassador.
type LLMClient interface {
	ChatCompletion(ctx context.Context, messages []llm.ChatMessage) (*llm.ChatCompletionResponse, error)
}

// SupervisorClient is the subset of supervisor operations the ambassador needs.
type SupervisorClient interface {
	EnsureWorker(req schema.EnsureWorkerRequest) (*schema.EnsureWorkerResponse, error)
	ReleaseWorker(req schema.ReleaseWorkerRequest) (*schema.ReleaseWorkerResponse, error)
	ListWorkers(req schema.ListWorkersRequest) (*schema.ListWorkersResponse, error)
	DescribeProfiles() (*schema.DescribeProfilesResponse, error)
}

// Config holds runtime configuration for the ambassador.
type Config struct {
	BusSocket        string
	SupervisorSocket string
	LLMClient        LLMClient
	// MaxConcurrentTurns limits how many turns can be processed at once.
	MaxConcurrentTurns int
}

// Ambassador is the 3PO ambassador daemon.
type Ambassador struct {
	cfg Config

	busClient        *bus.Client
	supervisorClient SupervisorClient

	// pending tracks in-flight work orders by task ID.
	pending   map[string]*PendingWork
	pendingMu sync.Mutex

	// ctx/cancel control the background event-loop goroutine.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// turnSem limits concurrent turn processing.
	turnSem chan struct{}
}

// PendingWork tracks the state of a delegated work order.
type PendingWork struct {
	TurnID     string
	SessionID  string
	WorkerID   string
	Profile    string
	Objective  string
	ReplyTopic string
	AssignedAt time.Time
	Result     *schema.WorkResult
	Needs3PO   *schema.WorkNeeds3PO
}

// New creates an Ambassador with the given configuration.
// The caller must call Start before the ambassador begins processing events.
func New(cfg Config) *Ambassador {
	if cfg.MaxConcurrentTurns <= 0 {
		cfg.MaxConcurrentTurns = 8
	}
	return &Ambassador{
		cfg:              cfg,
		pending:          make(map[string]*PendingWork),
		turnSem:          make(chan struct{}, cfg.MaxConcurrentTurns),
		supervisorClient: NewSupervisorClient(cfg.SupervisorSocket),
	}
}

// Start connects to the event bus, subscribes to relevant topics, and starts
// the event-loop goroutine.
func (a *Ambassador) Start() error {
	client, err := bus.Dial(a.cfg.BusSocket)
	if err != nil {
		return fmt.Errorf("bus dial: %w", err)
	}
	a.busClient = client

	// Subscribe to conversation turns, work results (all sub-topics), and
	// mid-flight needs_3po signals.
	topics := []string{
		schema.TopicConversationTurnRequested,
		schema.TopicAgentWorkResult + ".*",
		schema.TopicAgentWorkNeeds3PO,
	}
	for _, topic := range topics {
		if err := a.busClient.Subscribe(topic); err != nil {
			_ = a.busClient.Close()
			return fmt.Errorf("subscribe %s: %w", topic, err)
		}
	}

	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.wg.Add(1)
	go a.eventLoop()
	return nil
}

// Stop signals the event loop to exit and waits for it to finish.
func (a *Ambassador) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	// Close the bus client first to unblock Receive(), then wait for goroutines.
	if a.busClient != nil {
		_ = a.busClient.Close()
	}
	a.wg.Wait()
}

// eventLoop reads events from the bus and dispatches them.
func (a *Ambassador) eventLoop() {
	defer a.wg.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		default:
		}

		ev, err := a.busClient.Receive()
		if err != nil {
			if a.ctx.Err() != nil {
				return
			}
			log.Printf("bus receive: %v", err)
			continue
		}
		a.dispatch(ev)
	}
}

// dispatch routes an incoming bus event to the appropriate handler.
func (a *Ambassador) dispatch(ev bus.Event) {
	switch {
	case ev.Topic == schema.TopicConversationTurnRequested:
		a.handleTurn(ev)
	case strings.HasPrefix(ev.Topic, schema.TopicAgentWorkResult):
		a.handleWorkResult(ev)
	case ev.Topic == schema.TopicAgentWorkNeeds3PO:
		a.handleWorkNeeds3PO(ev)
	}
}

// handleTurn processes a conversation turn request.
func (a *Ambassador) handleTurn(ev bus.Event) {
	select {
	case a.turnSem <- struct{}{}:
	case <-a.ctx.Done():
		return
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() { <-a.turnSem }()

		var req schema.ConversationTurnRequest
		if err := json.Unmarshal(ev.Body, &req); err != nil {
			log.Printf("unmarshal turn request: %v", err)
			return
		}

		if err := a.processTurn(a.ctx, req); err != nil {
			log.Printf("process turn %s: %v", req.TurnID, err)
			_ = a.publishTurnResponse(req.SessionID, req.TurnID,
				fmt.Sprintf("Error processing your request: %v", err), nil)
		}
	}()
}

// processTurn classifies the turn and either answers directly or delegates.
func (a *Ambassador) processTurn(ctx context.Context, req schema.ConversationTurnRequest) error {
	classification, err := a.classifyTurn(ctx, req)
	if err != nil {
		return err
	}

	switch classification.Action {
	case ActionDirectAnswer:
		resp, err := a.directAnswer(ctx, req, classification)
		if err != nil {
			return err
		}
		return a.publishTurnResponse(req.SessionID, req.TurnID, resp, classification.Result)

	case ActionDelegate:
		return a.delegateWork(ctx, req, classification)

	case ActionAskFollowup:
		return a.publishTurnResponse(req.SessionID, req.TurnID, classification.FollowUpQuestion, nil)

	case ActionEscalateAdmin:
		return a.escalateToAdmin(ctx, req, classification)

	default:
		return fmt.Errorf("unknown action: %s", classification.Action)
	}
}

// publishTurnResponse sends a ConversationTurnResponse on the bus.
func (a *Ambassador) publishTurnResponse(sessionID, turnID, summary string, result json.RawMessage) error {
	resp := schema.ConversationTurnResponse{
		SessionID: sessionID,
		TurnID:    turnID,
		Summary:   summary,
		Result:    result,
	}
	if err := a.busClient.Publish(schema.TopicConversationTurnResponded, resp); err != nil {
		return fmt.Errorf("publish turn response: %w", err)
	}
	return nil
}

// handleWorkResult processes a work result from an R2 worker.
func (a *Ambassador) handleWorkResult(ev bus.Event) {
	var result schema.WorkResult
	if err := json.Unmarshal(ev.Body, &result); err != nil {
		log.Printf("unmarshal work result: %v", err)
		return
	}

	a.pendingMu.Lock()
	work, ok := a.pending[result.TaskID]
	a.pendingMu.Unlock()
	if !ok {
		// Result for a task we are not tracking; ignore.
		return
	}

	work.Result = &result

	// Synthesize the worker result into a human-readable response.
	summary, err := a.synthesizeWorkResult(a.ctx, work)
	if err != nil {
		log.Printf("synthesize result for task %s: %v", result.TaskID, err)
		summary = fmt.Sprintf("Worker %s completed with status %s: %s", work.WorkerID, result.Status, result.Summary)
	}

	_ = a.publishTurnResponse(work.SessionID, work.TurnID, summary, nil)

	// Clean up pending tracking.
	a.pendingMu.Lock()
	delete(a.pending, result.TaskID)
	a.pendingMu.Unlock()
}

// handleWorkNeeds3PO processes a mid-flight signal from an R2 requesting 3PO help.
func (a *Ambassador) handleWorkNeeds3PO(ev bus.Event) {
	var signal schema.WorkNeeds3PO
	if err := json.Unmarshal(ev.Body, &signal); err != nil {
		log.Printf("unmarshal needs_3po: %v", err)
		return
	}

	a.pendingMu.Lock()
	work, ok := a.pending[signal.TaskID]
	a.pendingMu.Unlock()
	if !ok {
		return
	}

	work.Needs3PO = &signal

	// For now, notify the user that the task needs more capability.
	summary := fmt.Sprintf("The worker handling this task has paused and requested additional reasoning: %s", signal.Reason)
	if signal.Summary != "" {
		summary += "\n\nSummary from worker:\n" + signal.Summary
	}

	_ = a.publishTurnResponse(work.SessionID, work.TurnID, summary, nil)
}

// classifyTurn uses the LLM to decide how to handle a conversation turn.
func (a *Ambassador) classifyTurn(ctx context.Context, req schema.ConversationTurnRequest) (*TurnClassification, error) {
	prompt := buildClassificationPrompt(req)
	msgs := []llm.ChatMessage{{Role: "user", Content: prompt}}

	resp, err := a.cfg.LLMClient.ChatCompletion(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("classify turn: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("classify turn: no choices returned")
	}

	classification, err := parseClassification(resp.Choices[0].Message.Content)
	if err != nil {
		return nil, err
	}
	if err := classification.Validate(); err != nil {
		return nil, fmt.Errorf("invalid turn classification: %w", err)
	}
	return classification, nil
}

// directAnswer uses the LLM to generate a direct human-facing response.
func (a *Ambassador) directAnswer(ctx context.Context, req schema.ConversationTurnRequest, classification *TurnClassification) (string, error) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: req.Prompt},
	}
	if classification.Context != "" {
		msgs = append([]llm.ChatMessage{
			{Role: "system", Content: classification.Context},
		}, msgs...)
	}

	resp, err := a.cfg.LLMClient.ChatCompletion(ctx, msgs)
	if err != nil {
		return "", fmt.Errorf("direct answer: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("direct answer: no choices returned")
	}
	return resp.Choices[0].Message.Content, nil
}

// delegateWork sends an EnsureWorker request to the supervisor and publishes a
// WorkOrder on the event bus.
func (a *Ambassador) delegateWork(ctx context.Context, req schema.ConversationTurnRequest, classification *TurnClassification) error {
	if len(classification.WorkerRequests) == 0 {
		return fmt.Errorf("delegate action but no worker requests")
	}

	for _, wr := range classification.WorkerRequests {
		taskID := fmt.Sprintf("%s-%s-%d", req.SessionID, req.TurnID, time.Now().UnixNano())
		ensureReq := schema.EnsureWorkerRequest{
			SessionID:     req.SessionID,
			RequestID:     taskID,
			RequesterRole: schema.RequesterRole3PO,
			WorkerProfile: wr.Profile,
			Objective:     wr.Objective,
			Inputs:        wr.Inputs,
			ReplyTopic:    fmt.Sprintf("agent.work.result.%s", taskID),
		}

		superResp, err := a.supervisorClient.EnsureWorker(ensureReq)
		if err != nil {
			return fmt.Errorf("ensure worker %s: %w", wr.Profile, err)
		}

		assignment := superResp.Assignment

		// Track pending work.
		a.pendingMu.Lock()
		a.pending[taskID] = &PendingWork{
			TurnID:     req.TurnID,
			SessionID:  req.SessionID,
			WorkerID:   assignment.WorkerID,
			Profile:    wr.Profile,
			Objective:  wr.Objective,
			ReplyTopic: ensureReq.ReplyTopic,
			AssignedAt: time.Now(),
		}
		a.pendingMu.Unlock()

		// Publish the work order on the event bus so the worker receives it.
		order := schema.WorkOrder{
			TaskID:        taskID,
			SessionID:     req.SessionID,
			WorkerID:      assignment.WorkerID,
			AssignedRole:  wr.Profile,
			WorkerProfile: wr.Profile,
			Objective:     wr.Objective,
			Inputs:        wr.Inputs,
			Budget:        wr.Budget,
			ReplyTopic:    ensureReq.ReplyTopic,
		}
		assignmentTopic := assignment.AssignmentTopic
		if assignmentTopic == "" {
			assignmentTopic = fmt.Sprintf("agent.work.assign.%s", assignment.WorkerID)
		}
		if err := a.busClient.Publish(assignmentTopic, order); err != nil {
			return fmt.Errorf("publish work order: %w", err)
		}

		log.Printf("delegated task %s to worker %s (profile %s)", taskID, assignment.WorkerID, wr.Profile)
	}

	// Publish an interim acknowledgment to the user.
	ack := fmt.Sprintf("I've assigned this to %d worker(s). I'll let you know when they finish.", len(classification.WorkerRequests))
	return a.publishTurnResponse(req.SessionID, req.TurnID, ack, nil)
}

// synthesizeWorkResult uses the LLM to turn a worker result into a
// human-readable response.
func (a *Ambassador) synthesizeWorkResult(ctx context.Context, work *PendingWork) (string, error) {
	if work.Result == nil {
		return "", fmt.Errorf("no result to synthesize")
	}

	prompt := buildSynthesisPrompt(work)
	msgs := []llm.ChatMessage{{Role: "user", Content: prompt}}

	resp, err := a.cfg.LLMClient.ChatCompletion(ctx, msgs)
	if err != nil {
		return "", fmt.Errorf("synthesize: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("synthesize: no choices returned")
	}
	return resp.Choices[0].Message.Content, nil
}

// escalateToAdmin publishes an escalation request to the admin agent topic.
func (a *Ambassador) escalateToAdmin(ctx context.Context, req schema.ConversationTurnRequest, classification *TurnClassification) error {
	escalation := schema.EscalationRequest{
		AgentUID:          0, // 3PO uid; will be stamped by peer creds
		TaskContext:       req.Prompt,
		RequestedAction:   classification.AdminAction,
		RequestedResource: classification.AdminResource,
		Justification:     classification.Justification,
	}

	if err := a.busClient.Publish(schema.TopicAdminEscalationRequested, escalation); err != nil {
		return fmt.Errorf("publish escalation: %w", err)
	}

	resp := fmt.Sprintf("This request requires admin approval. I've submitted an escalation for: %s", classification.AdminAction)
	return a.publishTurnResponse(req.SessionID, req.TurnID, resp, nil)
}

// buildClassificationPrompt crafts the prompt sent to the LLM for turn
// classification.
func buildClassificationPrompt(req schema.ConversationTurnRequest) string {
	return fmt.Sprintf(`You are the 3PO ambassador routing layer. Analyze the following user request and return a JSON object with no markdown formatting.

Request: %s

Respond with a JSON object containing exactly these fields:
- "action": one of "direct_answer", "delegate", "ask_followup", "escalate_admin"
- "context": optional system context for the answer (only for direct_answer)
- "follow_up_question": question to ask the user (only for ask_followup)
- "worker_requests": array of worker request objects (only for delegate), each with:
  - "profile": worker profile name (repo-inspector, patch-writer, ui-observer, etc.)
  - "objective": clear bounded task description
  - "inputs": JSON object of parameters
  - "budget": object with optional "max_steps" and "deadline_seconds"
- "admin_action": description of privileged action (only for escalate_admin)
- "admin_resource": resource being requested (only for escalate_admin)
- "justification": why this needs admin (only for escalate_admin)

Return ONLY the JSON object.`, req.Prompt)
}

// buildSynthesisPrompt crafts the prompt sent to the LLM to synthesize a
// worker result into a human response.
func buildSynthesisPrompt(work *PendingWork) string {
	result := work.Result
	artifactText := ""
	for _, art := range result.Artifacts {
		artifactText += fmt.Sprintf("- %s: %s\n", art.Kind, art.Path)
		if art.Text != "" {
			artifactText += fmt.Sprintf("  Content: %s\n", art.Text)
		}
	}

	return fmt.Sprintf(`You are the 3PO ambassador synthesis layer. A worker has completed a task. Summarize the result for the human user in a clear, concise way.

Task objective: %s
Worker profile: %s
Status: %s
Summary: %s
Error: %s
Artifacts:
%s
Follow-up items: %v

Write a natural, helpful response that synthesizes this information. Do not make up facts not present in the worker result.`,
		work.Objective,
		work.Profile,
		result.Status,
		result.Summary,
		result.Error,
		artifactText,
		result.FollowUp,
	)
}

// parseClassification parses the LLM's classification JSON response.
func parseClassification(raw string) (*TurnClassification, error) {
	// Clean up possible markdown code fences.
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var tc TurnClassification
	if err := json.Unmarshal([]byte(raw), &tc); err != nil {
		return nil, fmt.Errorf("parse classification: %w (raw: %s)", err, raw)
	}
	return &tc, nil
}
