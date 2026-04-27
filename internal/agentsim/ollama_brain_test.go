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

func TestOllamaBrain_SuccessfulAction(t *testing.T) {
	// Mock Ollama server that returns a valid publish action.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			w.WriteHeader(404)
			return
		}
		resp := map[string]any{
			"model": "qwen3:8b",
			"message": map[string]any{
				"role":    "assistant",
				"content": `{"kind":"publish","topic":"test.echo","body":"hello from ollama"}`,
			},
			"done": true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOllamaBrain(agentsim.OllamaConfig{
		BaseURL: srv.URL,
		Model:   "qwen3:8b",
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
	if info.Brain.Provider != "ollama" {
		t.Errorf("provider = %s, want ollama", info.Brain.Provider)
	}
	if len(info.RequestArtifacts) != 1 {
		t.Errorf("request artifacts = %d, want 1", len(info.RequestArtifacts))
	}
	if len(info.ResponseArtifacts) != 1 {
		t.Errorf("response artifacts = %d, want 1", len(info.ResponseArtifacts))
	}
}

func TestOllamaBrain_ConnectionRefused(t *testing.T) {
	// Point at a port unlikely to have an Ollama server.
	brain := agentsim.NewOllamaBrain(agentsim.OllamaConfig{
		BaseURL: "http://127.0.0.1:19999", // non-existent
		Model:   "qwen3:8b",
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

func TestOllamaBrain_Non200Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	brain := agentsim.NewOllamaBrain(agentsim.OllamaConfig{
		BaseURL: srv.URL,
		Model:   "qwen3:8b",
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

func TestOllamaBrain_InvalidJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model": "qwen3:8b",
			"message": map[string]any{
				"role":    "assistant",
				"content": "I think you should publish a message, but I'm not sure how...",
			},
			"done": true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOllamaBrain(agentsim.OllamaConfig{
		BaseURL: srv.URL,
		Model:   "qwen3:8b",
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

func TestOllamaBrain_StripCodeFences(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model": "qwen3:8b",
			"message": map[string]any{
				"role":    "assistant",
				"content": "```json\n{\"kind\":\"done\",\"done_verdict\":\"pass\"}\n```",
			},
			"done": true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOllamaBrain(agentsim.OllamaConfig{
		BaseURL: srv.URL,
		Model:   "qwen3:8b",
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

func TestOllamaBrain_WithOptions(t *testing.T) {
	var receivedSeed *int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Options *struct {
				Seed *int64 `json:"seed"`
			} `json:"options"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Options != nil {
			receivedSeed = req.Options.Seed
		}

		resp := map[string]any{
			"model": "qwen3:8b",
			"message": map[string]any{
				"role":    "assistant",
				"content": `{"kind":"done","done_verdict":"pass"}`,
			},
			"done": true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	temperature := 0.7
	seed := int64(42)

	brain := agentsim.NewOllamaBrain(agentsim.OllamaConfig{
		BaseURL: srv.URL,
		Model:   "qwen3:8b",
		Options: &agentsim.OllamaOptions{
			Temperature: &temperature,
			Seed:        &seed,
		},
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
		t.Errorf("seed not forwarded to Ollama: %v", receivedSeed)
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

func TestOllamaBrain_UnknownActionKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model": "qwen3:8b",
			"message": map[string]any{
				"role":    "assistant",
				"content": `{"kind":"launch_nukes","target":"moscow"}`,
			},
			"done": true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	brain := agentsim.NewOllamaBrain(agentsim.OllamaConfig{
		BaseURL: srv.URL,
		Model:   "qwen3:8b",
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

func TestRunner_WithOllamaBrain(t *testing.T) {
	// Set up a mock Ollama server.
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model": "qwen3:8b",
			"message": map[string]any{
				"role":    "assistant",
				"content": `{"kind":"done","done_verdict":"pass"}`,
			},
			"done": true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ollamaSrv.Close()

	socketPath, cleanup := testBus(t)
	defer cleanup()

	scenario := schema.EmpiricalScenario{
		ID: "ollama-runner-test",
		Brain: &schema.BrainConfig{
			Kind:     schema.BrainLocalLLM,
			Provider: "ollama",
			Model:    "qwen3:8b",
			BaseURL:  ollamaSrv.URL,
		},
	}

	brain := agentsim.NewOllamaBrain(agentsim.OllamaConfig{
		BaseURL: ollamaSrv.URL,
		Model:   "qwen3:8b",
	})

	cfg := agentsim.RunnerConfig{
		Scenario:  scenario,
		Brain:     brain,
		Agent:     schema.AgentInfo{Name: "test-agent", UID: 60001, Status: schema.StatusRunning},
		BusSocket: socketPath,
		RunID:     "ollama-run",
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
	if result.Brain.Brain.Provider != "ollama" {
		t.Errorf("brain provider = %s, want ollama", result.Brain.Brain.Provider)
	}
	if len(result.Brain.RequestArtifacts) != 1 {
		t.Errorf("request artifacts = %d, want 1", len(result.Brain.RequestArtifacts))
	}
	if len(result.Brain.ResponseArtifacts) != 1 {
		t.Errorf("response artifacts = %d, want 1", len(result.Brain.ResponseArtifacts))
	}
}

func TestDeterministicTestsDontDependOnOllama(t *testing.T) {
	// The deterministic brain does not implement BrainArtifacts.
	scripted := agentsim.NewScriptedBrain(nil)
	if _, ok := scripted.(agentsim.BrainArtifacts); ok {
		t.Error("NewScriptedBrain should not implement BrainArtifacts — deterministic tests should not depend on Ollama")
	}
}
