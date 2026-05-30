// Package agentsim_test tests the OpenAIBrain implementation (and related
// exported helpers) with deterministic httptest mock servers.
//
// The OpenAIBrain reuses internal/llm.Client for HTTP transport and URL
// construction (the same client used by the shell and webview) rather than
// duplicating a parallel HTTP client. These tests verify:
//   - Successful action parsing (publish, subscribe, done, etc.)
//   - Connection errors → env_failure verdicts
//   - Non-2xx and malformed response handling
//   - Markdown code fence stripping
//   - Options forwarding (seed, temperature)
//   - Implicit done detection (kind="" + done_verdict)
//   - Reasoning content fallback (reasoning_content → content)
//   - JSON extraction from narrative text (extractFirstJSON)
//   - API error field in response
//   - Empty choices handling
//   - Full runner integration
//
// Live LLM integration tests are in *_test.go files with //go:build integration
// and documented in test/phase4/README.md.
package agentsim_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/agentsim"
	"github.com/patch/agora-os/internal/schema"
)

func TestOpenAIBrain_SuccessfulAction(t *testing.T) {
	// Mock OpenAI-compatible server that returns a valid publish action.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			return
		}
		resp := map[string]any{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   "test-model",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"kind":"publish","topic":"test.echo","body":"hello from openai"}`,
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent: schema.AgentInfo{Name: "agent-1", UID: 60001, Status: schema.StatusRunning},
		Scenario: schema.EmpiricalScenario{
			ID:         "test",
			TaskPrompt: "publish a hello message",
			RunCount:   1,
		},
		Step: 0,
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if action.Kind != agentsim.ActionPublish {
		t.Errorf("kind = %s, want publish", action.Kind)
	}
	if action.Topic != "test.echo" {
		t.Errorf("topic = %s, want test.echo", action.Topic)
	}

	// Verify artifacts were recorded.
	info := brain.BrainRunInfo()
	if info.Brain.Kind != schema.BrainLocalLLM {
		t.Errorf("brain kind = %s, want local_llm", info.Brain.Kind)
	}
	if info.Brain.Provider != "openai" {
		t.Errorf("provider = %s, want openai", info.Brain.Provider)
	}
	if len(info.RequestArtifacts) != 1 {
		t.Errorf("request artifacts = %d, want 1", len(info.RequestArtifacts))
	}
	if len(info.ResponseArtifacts) != 1 {
		t.Errorf("response artifacts = %d, want 1", len(info.ResponseArtifacts))
	}
}

func TestOpenAIBrain_ConnectionRefused(t *testing.T) {
	// Point at a port unlikely to have a server.
	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: "http://127.0.0.1:19998", // non-existent
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe should not return Go error: %v", err)
	}

	// Should return an env_failure, not panic or error.
	if action.Kind != agentsim.ActionDone {
		t.Errorf("kind = %s, want done", action.Kind)
	}
	if action.DoneVerdict != schema.VerdictEnvFailure {
		t.Errorf("verdict = %s, want env_failure", action.DoneVerdict)
	}
	if action.DoneFailureCat != schema.FailureSetup {
		t.Errorf("category = %s, want setup", action.DoneFailureCat)
	}
}

func TestOpenAIBrain_Non200Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe should not return Go error: %v", err)
	}

	if action.DoneVerdict != schema.VerdictEnvFailure {
		t.Errorf("verdict = %s, want env_failure", action.DoneVerdict)
	}
	if action.DoneFailureCat != schema.FailureSetup {
		t.Errorf("category = %s, want setup (llm.Client wraps non-2xx as error)", action.DoneFailureCat)
	}
}

func TestOpenAIBrain_InvalidJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":     "chatcmpl-123",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "I think you should publish a message, but I'm not sure how...",
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if action.DoneVerdict != schema.VerdictEnvFailure {
		t.Errorf("verdict = %s, want env_failure", action.DoneVerdict)
	}
	if action.DoneFailureCat != schema.FailureLLMError {
		t.Errorf("category = %s, want llm_error", action.DoneFailureCat)
	}
}

func TestOpenAIBrain_StripCodeFences(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":     "chatcmpl-123",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "```json\n{\"kind\":\"done\",\"done_verdict\":\"pass\"}\n```",
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if action.Kind != agentsim.ActionDone {
		t.Errorf("kind = %s, want done", action.Kind)
	}
	if action.DoneVerdict != schema.VerdictPass {
		t.Errorf("verdict = %s, want pass", action.DoneVerdict)
	}
}

func TestOpenAIBrain_WithOptions(t *testing.T) {
	var receivedSeed *int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Seed *int64 `json:"seed,omitempty"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		receivedSeed = req.Seed

		resp := map[string]any{
			"id":     "chatcmpl-123",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"kind":"done","done_verdict":"pass"}`,
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	temperature := 0.7
	seed := int64(42)

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL:     srv.URL,
		Model:       "test-model",
		Temperature: &temperature,
		Seed:        &seed,
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	_, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if receivedSeed == nil || *receivedSeed != 42 {
		t.Errorf("seed not forwarded: %v", receivedSeed)
	}

	info := brain.BrainRunInfo()
	if info.Seed == nil || *info.Seed != 42 {
		t.Errorf("seed not in BrainRunInfo: %v", info.Seed)
	}
	if info.Brain.ModelOptions == nil {
		t.Error("ModelOptions should not be nil")
	} else if info.Brain.ModelOptions["seed"] != int64(42) {
		t.Errorf("seed in ModelOptions: %v", info.Brain.ModelOptions["seed"])
	}
}

func TestOpenAIBrain_UnknownActionKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":     "chatcmpl-123",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"kind":"launch_nukes","target":"moscow"}`,
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if action.DoneFailureCat != schema.FailureLLMHallucinate {
		t.Errorf("category = %s, want llm_hallucinate", action.DoneFailureCat)
	}
}

func TestOpenAIBrain_ImplicitDone(t *testing.T) {
	// Model outputs done_verdict without kind field — should infer ActionDone.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":     "chatcmpl-123",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"done_verdict":"pass"}`,
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if action.Kind != agentsim.ActionDone {
		t.Errorf("kind = %s, want done (implicit)", action.Kind)
	}
	if action.DoneVerdict != schema.VerdictPass {
		t.Errorf("verdict = %s, want pass", action.DoneVerdict)
	}
}

func TestOpenAIBrain_ReasoningContentFallback(t *testing.T) {
	// Model that emits action JSON in reasoning_content instead of content.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":     "chatcmpl-123",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":             "assistant",
						"content":          "",
						"reasoning_content": `{"kind":"subscribe","pattern":"test.*"}`,
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if action.Kind != agentsim.ActionSubscribe {
		t.Errorf("kind = %s, want subscribe", action.Kind)
	}
	if action.Pattern != "test.*" {
		t.Errorf("pattern = %s, want test.*", action.Pattern)
	}
}

func TestOpenAIBrain_ExtractFirstJSONFromNarrative(t *testing.T) {
	// Model that embeds the action JSON inside a reasoning narrative text.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":     "chatcmpl-123",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "I'll subscribe to the topic first.\n\n{\"kind\":\"subscribe\",\"pattern\":\"compositor.surface.*\"}\n\nThen I'll wait for events.",
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if action.Kind != agentsim.ActionSubscribe {
		t.Errorf("kind = %s, want subscribe", action.Kind)
	}
	if action.Pattern != "compositor.surface.*" {
		t.Errorf("pattern = %s, want compositor.surface.*", action.Pattern)
	}
}

func TestOpenAIBrain_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"error": map[string]any{
				"message": "model overloaded",
				"type":    "server_error",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200) // some APIs return 200 with error field
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe should not return Go error from API error: %v", err)
	}

	if action.DoneVerdict != schema.VerdictEnvFailure {
		t.Errorf("verdict = %s, want env_failure", action.DoneVerdict)
	}
	if action.DoneFailureCat != schema.FailureLLMError {
		t.Errorf("category = %s, want llm_error", action.DoneFailureCat)
	}
}

func TestOpenAIBrain_NoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"choices": []map[string]any{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	state := agentsim.StateSnapshot{
		Agent:    schema.AgentInfo{Name: "agent-1", UID: 60001},
		Scenario: schema.EmpiricalScenario{ID: "test"},
	}

	action, err := brain.Observe(state)
	if err != nil {
		t.Fatalf("Observe should not return Go error: %v", err)
	}

	if action.DoneVerdict != schema.VerdictEnvFailure {
		t.Errorf("verdict = %s, want env_failure", action.DoneVerdict)
	}
	if action.DoneFailureCat != schema.FailureLLMError {
		t.Errorf("category = %s, want llm_error", action.DoneFailureCat)
	}
}

func TestOpenAIBrain_RunnerIntegration(t *testing.T) {
	// Full runner + OpenAIBrain with mock server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":     "chatcmpl-123",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"kind":"done","done_verdict":"pass"}`,
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	socketPath, cleanup := testBus(t)
	defer cleanup()

	scenario := schema.EmpiricalScenario{
		ID: "openai-runner-test",
		Brain: &schema.BrainConfig{
			Kind:     schema.BrainLocalLLM,
			Provider: "openai",
			Model:    "test-model",
			BaseURL:  srv.URL,
		},
	}

	brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	cfg := agentsim.RunnerConfig{
		Scenario:  scenario,
		Brain:     brain,
		Agent:     schema.AgentInfo{Name: "test-agent", UID: 60001, Status: schema.StatusRunning},
		BusSocket: socketPath,
		RunID:     "openai-run",
		Attempt:   1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := agentsim.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.Verdict != schema.VerdictPass {
		t.Errorf("verdict = %s, want pass", result.Verdict)
	}

	// Verify brain info is populated.
	if result.Brain == nil {
		t.Fatal("Brain field should be populated")
	}
	if result.Brain.Brain.Provider != "openai" {
		t.Errorf("brain provider = %s, want openai", result.Brain.Brain.Provider)
	}
	if len(result.Brain.RequestArtifacts) != 1 {
		t.Errorf("request artifacts = %d, want 1", len(result.Brain.RequestArtifacts))
	}
	if len(result.Brain.ResponseArtifacts) != 1 {
		t.Errorf("response artifacts = %d, want 1", len(result.Brain.ResponseArtifacts))
	}
}

func TestExtractFirstJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     string
	}{
		{
			name:  "simple JSON string",
			input: `some text {"kind":"test"} more text`,
			want:  `{"kind":"test"}`,
		},
		{
			name:  "nested JSON",
			input: `prefix {"a":{"b":"c"}} suffix`,
			want:  `{"a":{"b":"c"}}`,
		},
		{
			name:  "no JSON",
			input: `just plain text with no braces`,
			want:  "",
		},
		{
			name:  "multiple JSON objects — returns last",
			input: `{"first":1} then {"second":2}`,
			want:  `{"second":2}`,
		},
		{
			name:  "narrative with embedded JSON",
			input: `I'll subscribe.\n\n{"kind":"subscribe","pattern":"test.*"}\n\nThen receive.`,
			want:  `{"kind":"subscribe","pattern":"test.*"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentsim.ExtractFirstJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractFirstJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
