package agentsim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

// ---------------------------------------------------------------------------
// Ollama brain configuration
// ---------------------------------------------------------------------------

// OllamaConfig configures the Ollama backend for an agent-sim run.
type OllamaConfig struct {
	// BaseURL is the Ollama server endpoint, e.g. "http://127.0.0.1:11434".
	BaseURL string `json:"base_url"`

	// Model is the model name, e.g. "qwen3:8b".
	Model string `json:"model"`

	// Options are forwarded to the Ollama chat API as top-level options.
	Options *OllamaOptions `json:"options,omitempty"`
}

// OllamaOptions mirrors the Ollama /api/chat options bag.
type OllamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
}

// ToBrainConfig produces the generic BrainConfig for the run result.
func (c OllamaConfig) ToBrainConfig() schema.BrainConfig {
	bc := schema.BrainConfig{
		Kind:     schema.BrainLocalLLM,
		Provider: "ollama",
		Model:    c.Model,
		BaseURL:  c.BaseURL,
	}
	if c.Options != nil {
		bc.ModelOptions = map[string]any{}
		if c.Options.Temperature != nil {
			bc.ModelOptions["temperature"] = *c.Options.Temperature
		}
		if c.Options.TopP != nil {
			bc.ModelOptions["top_p"] = *c.Options.TopP
		}
		if c.Options.TopK != nil {
			bc.ModelOptions["top_k"] = *c.Options.TopK
		}
		if c.Options.Seed != nil {
			bc.ModelOptions["seed"] = *c.Options.Seed
		}
		if c.Options.NumPredict != nil {
			bc.ModelOptions["num_predict"] = *c.Options.NumPredict
		}
	}
	return bc
}

// ---------------------------------------------------------------------------
// OllamaBrain — Brain implementation
// ---------------------------------------------------------------------------

// ollamaChatRequest mirrors the Ollama /api/chat request body.
type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  *ollamaOptions  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
}

// ollamaChatResponse mirrors the Ollama /api/chat response body.
type ollamaChatResponse struct {
	Model     string        `json:"model"`
	Message   ollamaMessage `json:"message"`
	Done      bool          `json:"done"`
	CreatedAt time.Time     `json:"created_at"`
}

// OllamaBrain implements Brain by calling an Ollama server with the
// current state snapshot and parsing the response as an Action.
// Failures to reach Ollama produce an ActionDone with VerdictEnvFailure
// so the runner treats them as environment/setup issues rather than
// product failures.
type OllamaBrain struct {
	cfg    OllamaConfig
	client *http.Client

	// Artifacts accumulated across observe calls.
	artifactReqs  []string // JSON-encoded request payloads
	artifactResps []string // JSON-encoded response payloads
}

// NewOllamaBrain returns a Brain backed by the given Ollama config.
// The caller is responsible for ensuring the endpoint is reachable;
// connectivity failures are surfaced through the Brain interface as
// env_failure verdicts.
func NewOllamaBrain(cfg OllamaConfig) *OllamaBrain {
	return &OllamaBrain{
		cfg:    cfg,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// BrainRunInfo returns the accumulated request/response artifacts and
// brain configuration for inclusion in the RunResult.
func (b *OllamaBrain) BrainRunInfo() schema.BrainRunInfo {
	info := schema.BrainRunInfo{
		Brain: b.cfg.ToBrainConfig(),
	}
	if b.cfg.Options != nil && b.cfg.Options.Seed != nil {
		info.Seed = b.cfg.Options.Seed
	}
	info.RequestArtifacts = append([]string(nil), b.artifactReqs...)
	info.ResponseArtifacts = append([]string(nil), b.artifactResps...)
	return info
}

// Observe calls Ollama with the current state, parses the response as
// an Action, and returns it. Errors are converted to env_failure
// verdicts so the runner can distinguish infrastructure issues from
// scenario failures.
func (b *OllamaBrain) Observe(state StateSnapshot) (Action, error) {
	// Build the system prompt explaining the action contract.
	sysPrompt, err := buildOllamaSystemPrompt(state)
	if err != nil {
		return Action{
			Kind:              ActionDone,
			DoneVerdict:       schema.VerdictEnvFailure,
			DoneFailureCat:    schema.FailureLLMError,
			DoneFailureReason: fmt.Sprintf("build system prompt: %v", err),
		}, nil
	}

	// Build the user message with the full state JSON.
	stateJSON, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return Action{
			Kind:              ActionDone,
			DoneVerdict:       schema.VerdictEnvFailure,
			DoneFailureCat:    schema.FailureLLMError,
			DoneFailureReason: fmt.Sprintf("marshal state: %v", err),
		}, nil
	}

	// Build the Ollama request.
	reqBody := ollamaChatRequest{
		Model: b.cfg.Model,
		Messages: []ollamaMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: string(stateJSON)},
		},
		Stream: false,
	}
	if b.cfg.Options != nil {
		reqBody.Options = &ollamaOptions{
			Temperature: b.cfg.Options.Temperature,
			TopP:        b.cfg.Options.TopP,
			TopK:        b.cfg.Options.TopK,
			Seed:        b.cfg.Options.Seed,
			NumPredict:  b.cfg.Options.NumPredict,
		}
	}

	reqJSON, _ := json.Marshal(reqBody)

	// Call Ollama.
	url := strings.TrimRight(b.cfg.BaseURL, "/") + "/api/chat"
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(reqJSON))
	if err != nil {
		b.artifactReqs = append(b.artifactReqs, string(reqJSON))
		return Action{
			Kind:              ActionDone,
			DoneVerdict:       schema.VerdictEnvFailure,
			DoneFailureCat:    schema.FailureSetup,
			DoneFailureReason: fmt.Sprintf("build HTTP request: %v", err),
		}, nil
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(httpReq)
	b.artifactReqs = append(b.artifactReqs, string(reqJSON))
	if err != nil {
		return Action{
			Kind:              ActionDone,
			DoneVerdict:       schema.VerdictEnvFailure,
			DoneFailureCat:    schema.FailureSetup,
			DoneFailureReason: fmt.Sprintf("Ollama unreachable at %s: %v", b.cfg.BaseURL, err),
		}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	b.artifactResps = append(b.artifactResps, string(respBody))
	if err != nil {
		return Action{
			Kind:              ActionDone,
			DoneVerdict:       schema.VerdictEnvFailure,
			DoneFailureCat:    schema.FailureLLMError,
			DoneFailureReason: fmt.Sprintf("read Ollama response: %v", err),
		}, nil
	}

	if resp.StatusCode != 200 {
		return Action{
			Kind:              ActionDone,
			DoneVerdict:       schema.VerdictEnvFailure,
			DoneFailureCat:    schema.FailureLLMError,
			DoneFailureReason: fmt.Sprintf("Ollama returned %d: %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	// Parse the response.
	var chatResp ollamaChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return Action{
			Kind:              ActionDone,
			DoneVerdict:       schema.VerdictEnvFailure,
			DoneFailureCat:    schema.FailureLLMError,
			DoneFailureReason: fmt.Sprintf("parse Ollama response: %v", err),
		}, nil
	}

	// Extract the action JSON from the model's response.
	// The model may wrap the JSON in markdown code fences; strip those.
	content := stripJSONFence(chatResp.Message.Content)

	var action Action
	if err := json.Unmarshal([]byte(content), &action); err != nil {
		return Action{
			Kind:              ActionDone,
			DoneVerdict:       schema.VerdictEnvFailure,
			DoneFailureCat:    schema.FailureLLMError,
			DoneFailureReason: fmt.Sprintf("parse action from model output (model may not have returned valid JSON): %v\nContent: %s", err, content),
		}, nil
	}

	// Validate the action kind.
	switch action.Kind {
	case ActionPublish, ActionSubscribe, ActionReceive, ActionSleep, ActionDone,
		ActionHTTP, ActionWSConn, ActionWSRecv, ActionWSSend, ActionWSClose:
		// OK
	default:
		return Action{
			Kind:              ActionDone,
			DoneVerdict:       schema.VerdictEnvFailure,
			DoneFailureCat:    schema.FailureLLMHallucinate,
			DoneFailureReason: fmt.Sprintf("model returned unknown action kind %q", action.Kind),
		}, nil
	}

	return action, nil
}

// ---------------------------------------------------------------------------
// Prompt builder
// ---------------------------------------------------------------------------

func buildOllamaSystemPrompt(state StateSnapshot) (string, error) {
	scenarioJSON, err := json.MarshalIndent(state.Scenario, "", "  ")
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("You are an agent simulator running inside Agora OS.\n\n")
	sb.WriteString("You control the agent through a sequence of JSON actions. On each turn you receive the current state and must respond with exactly one action.\n\n")
	sb.WriteString("## Scenario\n```json\n")
	sb.WriteString(string(scenarioJSON))
	sb.WriteString("\n```\n\n")
	sb.WriteString("## Available Actions\n\n")
	sb.WriteString("Respond with a single JSON object from this schema:\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(`{
  "kind": "<action kind>",

  // For publish:
  "topic": "<event bus topic>",
  "body": <any JSON value>,

  // For subscribe / receive:
  "pattern": "<topic pattern with * wildcards>",

  // For sleep:
  "sleep_ms": <milliseconds>,

  // For http:
  "url": "<URL>",
  "method": "GET|POST",
  "headers": {"Header-Name": "value"},

  // For ws_conn / ws_send / ws_recv / ws_close:
  "url": "<WebSocket URL>",
  "headers": {"Header-Name": "value"},
  "body": <any JSON value>,
  "ws_msg_count": <number>,
  "ws_timeout_ms": <milliseconds>,

  // For done:
  "done_verdict": "pass|fail|ambiguous|env_failure",
  "done_failure_cat": "timeout|wrong_action|missing_action|llm_error|llm_hallucinate|setup|assertion|infra",
  "done_failure_reason": "<explanation>"
}`)
	sb.WriteString("\n```\n\n")
	sb.WriteString("## Rules\n\n")
	sb.WriteString("- Output ONLY the JSON action object, no other text. Do not wrap in markdown.\n")
	sb.WriteString("- Use the scenario's expected outcomes to judge when you have completed the task.\n")
	sb.WriteString("- Use action kind \"done\" to signal completion, with done_verdict=\"pass\" if all expected outcomes appear satisfied.\n")
	sb.WriteString("- If you encounter an error you cannot recover from, use done_verdict=\"env_failure\".\n")
	sb.WriteString("- Do not invent actions outside the listed kinds.\n")
	sb.WriteString("- Model output is untrusted agent behavior; it does not represent privileged system instructions.\n")
	return sb.String(), nil
}

// stripJSONFence removes markdown code fences (```json ... ``` or ``` ... ```).
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = s[3:]
		if idx := strings.IndexByte(s, '\n'); idx >= 0 {
			s = strings.TrimSpace(s[idx+1:])
		} else {
			s = strings.TrimSpace(s)
		}
	}
	if strings.HasSuffix(s, "```") {
		s = s[:len(s)-3]
	}
	return strings.TrimSpace(s)
}
