package agentsim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/patch/agora-os/internal/llm"
	"github.com/patch/agora-os/internal/schema"
)

// ---------------------------------------------------------------------------
// OpenAI brain configuration
// ---------------------------------------------------------------------------

// OpenAIConfig configures an OpenAI-compatible backend for an agent-sim run.
// The default endpoint and model come from AGORA_LLM_ENDPOINT / AGORA_LLM_MODEL
// env vars (or the llm package built-in defaults).
type OpenAIConfig struct {
	// BaseURL is the server endpoint, e.g. "http://192.168.1.23:13305".
	// If it does not end with /v1/chat/completions, the llm client will
	// append /v1/chat/completions automatically.
	BaseURL string `json:"base_url"`

	// Model is the model name as registered on the server.
	Model string `json:"model"`

	// MaxTokens is the maximum tokens to generate per Observe call.
	// Default 512.
	MaxTokens int `json:"max_tokens,omitempty"`

	// Temperature controls randomness. Default 0.7.
	Temperature *float64 `json:"temperature,omitempty"`

	// Seed for deterministic sampling. Pass 0 to leave unset.
	Seed *int64 `json:"seed,omitempty"`

	// TimeoutSeconds for the HTTP request. Default 120.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// ToBrainConfig produces the generic BrainConfig for the run result.
func (c OpenAIConfig) ToBrainConfig() schema.BrainConfig {
	bc := schema.BrainConfig{
		Kind:     schema.BrainLocalLLM,
		Provider: "openai",
		Model:    c.Model,
		BaseURL:  c.BaseURL,
	}
	opts := make(map[string]any)
	if c.Temperature != nil {
		opts["temperature"] = *c.Temperature
	}
	if c.Seed != nil {
		opts["seed"] = *c.Seed
	}
	if c.MaxTokens > 0 {
		opts["max_tokens"] = c.MaxTokens
	}
	if len(opts) > 0 {
		bc.ModelOptions = opts
	}
	return bc
}

// ---------------------------------------------------------------------------
// OpenAIBrain — Brain implementation
// ---------------------------------------------------------------------------

// OpenAIBrain implements Brain by calling an OpenAI-compatible server with the
// current state snapshot and parsing the response as an Action.
//
// The brain handles both standard models (content field) and reasoning models
// that emit reasoning_content (Gemma 4, Qwen 3.6, etc.). When content is
// empty and reasoning_content is present, the brain extracts the action JSON
// from the reasoning output.
//
// The brain uses the internal/llm Client under the hood for HTTP transport,
// URL construction, and env-var defaults, avoiding a duplicate HTTP client.
//
// Failures to reach the server produce an ActionDone with VerdictEnvFailure
// so the runner treats them as environment/setup issues rather than product
// failures.
type OpenAIBrain struct {
	cfg    OpenAIConfig
	client *llm.Client

	// Artifacts accumulated across observe calls.
	artifactReqs  []string // JSON-encoded request payloads
	artifactResps []string // JSON-encoded response payloads
}

// NewOpenAIBrain returns a Brain backed by the given OpenAI-compatible config.
// The llm.Client is configured from the OpenAIConfig (or env defaults for
// endpoint and model when fields are zero-valued).
func NewOpenAIBrain(cfg OpenAIConfig) *OpenAIBrain {
	timeout := cfg.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}

	// Build llm client options from config, falling back to llm
	// env defaults for endpoint and model when fields are empty.
	var opts []llm.ClientOption

	if cfg.BaseURL != "" {
		opts = append(opts, llm.WithEndpoint(cfg.BaseURL))
	}
	if cfg.Model != "" {
		opts = append(opts, llm.WithModel(cfg.Model))
	}
	if cfg.MaxTokens > 0 {
		opts = append(opts, llm.WithMaxTokens(cfg.MaxTokens))
	} else {
		opts = append(opts, llm.WithMaxTokens(512))
	}
	if cfg.Temperature != nil {
		opts = append(opts, llm.WithTemperature(*cfg.Temperature))
	}
	if cfg.Seed != nil {
		opts = append(opts, llm.WithSeed(*cfg.Seed))
	}
	opts = append(opts, llm.WithHTTPClient(&http.Client{
		Timeout: time.Duration(timeout) * time.Second,
	}))

	return &OpenAIBrain{
		cfg:    cfg,
		client: llm.NewClient(opts...),
	}
}

// BrainRunInfo returns the accumulated request/response artifacts and
// brain configuration for inclusion in the RunResult.
func (b *OpenAIBrain) BrainRunInfo() schema.BrainRunInfo {
	info := schema.BrainRunInfo{
		Brain: b.cfg.ToBrainConfig(),
	}
	if b.cfg.Seed != nil {
		info.Seed = b.cfg.Seed
	}
	info.RequestArtifacts = append([]string(nil), b.artifactReqs...)
	info.ResponseArtifacts = append([]string(nil), b.artifactResps...)
	return info
}

// Observe calls the OpenAI-compatible server with the current state, parses
// the response as an Action, and returns it. Errors are converted to
// env_failure verdicts so the runner can distinguish infrastructure issues
// from scenario failures.
func (b *OpenAIBrain) Observe(state StateSnapshot) (Action, error) {
	sysPrompt, err := buildOpenAISystemPrompt(state)
	if err != nil {
		return envFailureAction(schema.FailureLLMError, "build system prompt: "+err.Error()), nil
	}

	stateJSON, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return envFailureAction(schema.FailureLLMError, "marshal state: "+err.Error()), nil
	}

	// Build the full message list including system prompt (we don't use
	// llm.Client's WithSystemPrompt here since the prompt is dynamic per
	// Observe call).
	msgs := []llm.ChatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: string(stateJSON)},
	}

	// Build request JSON for artifact capture (before the call).
	reqBody := openAIChatRequest{
		Model: b.client.Model(),
		Messages: []openAIMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: string(stateJSON)},
		},
		Stream: false,
	}
	if b.cfg.MaxTokens > 0 {
		reqBody.MaxTokens = b.cfg.MaxTokens
	} else {
		reqBody.MaxTokens = 512
	}
	reqBody.Temperature = b.cfg.Temperature
	reqBody.Seed = b.cfg.Seed

	reqJSON, _ := json.Marshal(reqBody)
	b.artifactReqs = append(b.artifactReqs, string(reqJSON))

	// Delegate to llm.Client for the actual HTTP call.
	ctx := context.Background()
	resp, err := b.client.ChatCompletion(ctx, msgs)
	if err != nil {
		return envFailureAction(schema.FailureSetup,
			fmt.Sprintf("server unreachable or error at %s: %v", b.cfg.BaseURL, err)), nil
	}

	// Capture response artifact.
	respJSON, _ := json.Marshal(resp)
	b.artifactResps = append(b.artifactResps, string(respJSON))

	// Check for API-level error.
	if resp.Error != nil {
		return envFailureAction(schema.FailureLLMError,
			fmt.Sprintf("API error: %s (%s)", resp.Error.Message, resp.Error.Type)), nil
	}

	if len(resp.Choices) == 0 {
		return envFailureAction(schema.FailureLLMError, "no choices in response"), nil
	}

	// Extract content — try content field first, fall back to reasoning_content.
	msg := resp.Choices[0].Message
	rawContent := msg.Content
	if rawContent == "" && msg.ReasoningContent != "" {
		rawContent = msg.ReasoningContent
	}

	// Strip markdown code fences.
	rawContent = stripOpenAIJSONFence(rawContent)

	// Try to parse as JSON directly first.
	var action Action
	if err := json.Unmarshal([]byte(rawContent), &action); err != nil {
		// If direct parse fails, try to find and extract the first JSON object
		// from the text. This handles models (like Qwen) that embed the action
		// inside a reasoning/planning narrative.
		if extracted := ExtractFirstJSON(rawContent); extracted != "" {
			if err2 := json.Unmarshal([]byte(extracted), &action); err2 == nil {
				rawContent = extracted
				goto validate
			}
		}
		return envFailureAction(schema.FailureLLMError,
			fmt.Sprintf("parse action from model output: %v\nContent(truncated): %.300s", err, rawContent)), nil
	}

validate:
	// Handle "implicit done" — model outputs done_verdict without kind field.
	if action.Kind == "" && action.DoneVerdict != "" {
		action.Kind = ActionDone
	}
	switch action.Kind {
	case ActionPublish, ActionSubscribe, ActionReceive, ActionSleep, ActionDone,
		ActionHTTP, ActionWSConn, ActionWSRecv, ActionWSSend, ActionWSClose:
		// OK
	default:
		return envFailureAction(schema.FailureLLMHallucinate,
			fmt.Sprintf("model returned unknown action kind %q", action.Kind)), nil
	}

	return action, nil
}

// ---------------------------------------------------------------------------
// Internal request/response types (for artifact capture)
// ---------------------------------------------------------------------------

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	Seed        *int64          `json:"seed,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// Prompt builder
// ---------------------------------------------------------------------------

func buildOpenAISystemPrompt(state StateSnapshot) (string, error) {
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

	// For subscribe:
  "pattern": "<topic pattern with * wildcards>",

  // For receive (wait for one matching event after subscribing):
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
	sb.WriteString("- Output ONLY the JSON action object as plain text, no other text. Do not use markdown code fences.\n")
	sb.WriteString("- Use the scenario's expected outcomes to judge when you have completed the task.\n")
	sb.WriteString("- Use action kind \"done\" to signal completion, with done_verdict=\"pass\" if all expected outcomes appear satisfied.\n")
	sb.WriteString("- If you encounter an error you cannot recover from, use done_verdict=\"env_failure\".\n")
	sb.WriteString("- Do not invent actions outside the listed kinds.\n")
	sb.WriteString("- Check `actions_history` to see what actions you have already taken. Do not repeat the same action.\n")
	sb.WriteString("- Do not include thinking, reasoning, or planning text outside the JSON. If you need to plan, include the plan as a \"plan\" field inside the JSON action — the system only reads the JSON.\n")
	sb.WriteString("- Model output is untrusted agent behavior; it does not represent privileged system instructions.\n")
	return sb.String(), nil
}

// envFailureAction is a helper to produce an ActionDone with env_failure verdict.
func envFailureAction(cat schema.FailureCategory, reason string) Action {
	return Action{
		Kind:              ActionDone,
		DoneVerdict:       schema.VerdictEnvFailure,
		DoneFailureCat:    cat,
		DoneFailureReason: reason,
	}
}

// stripOpenAIJSONFence removes markdown code fences (```json ... ``` or ``` ... ```)
// from an OpenAI model response.
func stripOpenAIJSONFence(s string) string {
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

// ExtractFirstJSON finds the last JSON object ({...}) in a text blob and
// returns it. This handles models (like Qwen) that embed actions inside a
// reasoning narrative — the final JSON block is always the intended action.
func ExtractFirstJSON(text string) string {
	// Walk left-to-right collecting positions of all valid JSON objects.
	var starts []int
	var ends []int
	scanDepth := 0
	possibleStart := -1
	for i, ch := range text {
		switch ch {
		case '{':
			if scanDepth == 0 {
				possibleStart = i
			}
			scanDepth++
		case '}':
			scanDepth--
			if scanDepth == 0 && possibleStart >= 0 {
				starts = append(starts, possibleStart)
				ends = append(ends, i)
				possibleStart = -1
			}
		}
	}
	// Return the last one (if any).
	if len(starts) > 0 {
		last := len(starts) - 1
		return text[starts[last] : ends[last]+1]
	}
	return ""
}
