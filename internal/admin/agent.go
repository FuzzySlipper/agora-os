// Package admin implements the stateless privilege escalation evaluator.
//
// Each escalation request is evaluated independently against a system prompt
// using an LLM API. The agent is intentionally stateless between requests —
// no conversation history, no memory, no learning from prior outcomes.
package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/peercred"
	"github.com/patch/agora-os/internal/schema"
)

// Config holds the values needed to construct an Agent. All fields are
// required except APIURL which defaults to the Anthropic messages endpoint.
type Config struct {
	SystemPrompt string
	APIKey       string
	APIURL       string        // defaults to https://api.anthropic.com/v1/messages
	LogFile      *os.File      // opened for append-only writing
	LLMTimeout   time.Duration // HTTP timeout for LLM calls; defaults to 30s
	BusSocket    string        // optional event bus socket for publishing pending escalations
}

// Agent evaluates escalation requests. It is safe for concurrent use.
type Agent struct {
	systemPrompt string
	apiKey       string
	apiURL       string
	logFile      *os.File
	logMu        sync.Mutex
	llmClient    *http.Client
	busSocket    string
}

// New creates an Agent from the given configuration.
func New(cfg Config) *Agent {
	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = "https://api.anthropic.com/v1/messages"
	}
	timeout := cfg.LLMTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Agent{
		systemPrompt: cfg.SystemPrompt,
		apiKey:       cfg.APIKey,
		apiURL:       apiURL,
		logFile:      cfg.LogFile,
		llmClient:    &http.Client{Timeout: timeout},
		busSocket:    cfg.BusSocket,
	}
}

// HandleConn reads a single escalation request from conn, evaluates it,
// logs the decision, and writes the response. Identity is established from
// SO_PEERCRED at the start — the self-reported agent_uid in the request
// body is overridden with the kernel-verified value.
func (a *Agent) HandleConn(conn net.Conn) {
	defer conn.Close()

	// Identity comes from the kernel, not from the request.
	peerUID, err := peercred.PeerUID(conn)
	if err != nil {
		log.Printf("peer credentials: %v", err)
		return
	}

	var req schema.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	if req.Method != schema.MethodEscalate {
		writeJSON(conn, schema.Response{OK: false})
		return
	}

	var escReq schema.EscalationRequest
	if err := json.Unmarshal(req.Body, &escReq); err != nil {
		writeJSON(conn, schema.Response{OK: false})
		return
	}

	// Override self-reported uid with kernel-verified uid.
	if escReq.AgentUID != uint32(peerUID) {
		log.Printf("uid mismatch: peer=%d self-reported=%d (overridden)", peerUID, escReq.AgentUID)
	}
	escReq.AgentUID = uint32(peerUID)

	resp := a.evaluate(escReq)
	id, _, err := a.logEntry(escReq, resp)
	if err != nil {
		log.Printf("admin log write failed: %v", err)
		errResp := schema.EscalationResponse{
			Decision:  schema.DecisionEscalate,
			Reasoning: fmt.Sprintf("audit log write failed: %v — cannot confirm request was logged", err),
		}
		b, _ := json.Marshal(errResp)
		writeJSON(conn, schema.Response{OK: false, Body: b})
		return
	}

	if resp.Decision == schema.DecisionEscalate {
		a.publishPending(id, escReq, resp)
	}

	b, _ := json.Marshal(resp)
	writeJSON(conn, schema.Response{OK: true, Body: b})
}

func (a *Agent) evaluate(req schema.EscalationRequest) schema.EscalationResponse {
	// Build a single-turn prompt. No history. The system prompt is the only
	// instruction channel; the request body is data to evaluate, not instructions.
	userContent := fmt.Sprintf(
		"Evaluate this escalation request from agent uid %d.\n\n"+
			"Task context: %s\n"+
			"Requested action: %s\n"+
			"Requested resource: %s\n"+
			"Agent's justification: %s\n\n"+
			"Respond with a JSON object: {\"decision\": \"approve\"|\"deny\"|\"escalate\", "+
			"\"reasoning\": \"...\", \"constraints\": [...]}",
		req.AgentUID, req.TaskContext, req.RequestedAction,
		req.RequestedResource, req.Justification,
	)

	llmResp, err := a.callLLM(userContent)
	if err != nil {
		// On LLM failure, default to escalate (needs human review).
		// Never default to approve.
		return schema.EscalationResponse{
			Decision:  schema.DecisionEscalate,
			Reasoning: fmt.Sprintf("LLM evaluation failed: %v — defaulting to human review", err),
		}
	}

	var decision schema.EscalationResponse
	if err := json.Unmarshal([]byte(llmResp), &decision); err != nil {
		return schema.EscalationResponse{
			Decision:  schema.DecisionEscalate,
			Reasoning: "Failed to parse LLM response — defaulting to human review",
		}
	}
	return decision
}

func (a *Agent) callLLM(userContent string) (string, error) {
	body := map[string]any{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 1024,
		"system":     a.systemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userContent},
		},
	}
	b, _ := json.Marshal(body)

	httpReq, _ := http.NewRequest("POST", a.apiURL, bytes.NewReader(b))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.llmClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("api %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response")
	}
	return result.Content[0].Text, nil
}

// logEntry appends the request/response pair to the append-only log.
// Returns the derived event ID, the entry timestamp, and an error if the
// write fails or is a short write.
func (a *Agent) logEntry(req schema.EscalationRequest, resp schema.EscalationResponse) (string, time.Time, error) {
	ts := time.Now()
	entry := struct {
		Timestamp time.Time                 `json:"timestamp"`
		Request   schema.EscalationRequest  `json:"request"`
		Response  schema.EscalationResponse `json:"response"`
	}{
		Timestamp: ts,
		Request:   req,
		Response:  resp,
	}

	b, _ := json.Marshal(entry)
	b = append(b, '\n')

	a.logMu.Lock()
	n, err := a.logFile.Write(b)
	if err == nil {
		err = a.logFile.Sync()
	}
	a.logMu.Unlock()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("log entry durability: %w", err)
	}
	if n != len(b) {
		return "", time.Time{}, fmt.Errorf("short write log entry: wrote %d of %d bytes", n, len(b))
	}
	return eventID(b), ts, nil
}

func eventID(line []byte) string {
	sum := sha256.Sum256(bytes.TrimSpace(line))
	return hex.EncodeToString(sum[:8])
}

// publishPending sends an admin.escalation.pending event to the event bus
// when the model evaluation returns escalate.
func (a *Agent) publishPending(id string, req schema.EscalationRequest, resp schema.EscalationResponse) {
	if a.busSocket == "" {
		return
	}
	client, err := bus.Dial(a.busSocket)
	if err != nil {
		log.Printf("bus connect for pending publish: %v", err)
		return
	}
	defer client.Close()

	event := schema.AdminEscalationEvent{
		ID:        id,
		Timestamp: time.Now(),
		Request:   req,
		Response:  resp,
	}
	if err := client.Publish(schema.TopicAdminEscalationPending, event); err != nil {
		log.Printf("bus publish pending: %v", err)
	}
}

// LogHumanDecision appends a human decision to the same append-only log
// used for model evaluations. This keeps the audit trail unified.
func (a *Agent) LogHumanDecision(decision schema.HumanEscalationDecision) error {
	entry := struct {
		Timestamp time.Time                      `json:"timestamp"`
		Decision  schema.HumanEscalationDecision `json:"decision"`
	}{
		Timestamp: time.Now(),
		Decision:  decision,
	}
	b, _ := json.Marshal(entry)
	b = append(b, '\n')

	a.logMu.Lock()
	n, err := a.logFile.Write(b)
	if err == nil {
		err = a.logFile.Sync()
	}
	a.logMu.Unlock()
	if err != nil {
		return fmt.Errorf("log entry durability: %w", err)
	}
	if n != len(b) {
		return fmt.Errorf("short write log entry: wrote %d of %d bytes", n, len(b))
	}
	return nil
}

// RunDecisionLogger connects to the event bus and listens for human
// escalation decisions, appending each one to the agent's log. It blocks
// until the bus connection fails and should be run in a goroutine.
func (a *Agent) RunDecisionLogger() {
	if a.busSocket == "" {
		return
	}
	for {
		if err := a.runDecisionLoggerOnce(); err != nil {
			log.Printf("decision logger: %v", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func (a *Agent) runDecisionLoggerOnce() error {
	client, err := bus.Dial(a.busSocket)
	if err != nil {
		return fmt.Errorf("bus connect: %w", err)
	}
	defer client.Close()

	if err := client.Subscribe(schema.TopicAdminEscalationDecided); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	for {
		ev, err := client.Receive()
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}
		if ev.Topic != schema.TopicAdminEscalationDecided {
			continue
		}
		var decision schema.HumanEscalationDecision
		if err := json.Unmarshal(ev.Body, &decision); err != nil {
			log.Printf("decision logger: decode: %v", err)
			continue
		}
		if err := a.LogHumanDecision(decision); err != nil {
			log.Printf("decision logger: log: %v", err)
		}
	}
}

func writeJSON(conn net.Conn, v any) {
	json.NewEncoder(conn).Encode(v)
}
