//go:build integration

// Run: go test -tags integration -run TestIntegration_QwenOllama -timeout 300s ./internal/agentsim/
package agentsim_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/agentsim"
	"github.com/patch/agora-os/internal/schema"
)

// TestIntegration_QwenOllama compares OpenAIBrain vs OllamaBrain for Qwen
// on the admin-escalate scenario. Tests if the Ollama endpoint's cleaner
// content/thinking separation affects wall time.
func TestIntegration_QwenOllama(t *testing.T) {
	scenario := schema.EmpiricalScenario{
		ID:          "qwen-endpoint-compare",
		Title:       "Admin escalation — approve",
		RunCount:    1,
		SuccessThresholdPercent: 100,
		TimeoutSeconds:          60,
		ExpectedOutcomes: []schema.ExpectedOutcome{
			{ID: "submitted", Source: "action", Match: "contains", Value: "admin.escalation.requested"},
			{ID: "received", Source: "event_bus_topic", Match: "equals", Value: "admin.escalation.decided"},
			{ID: "handled", Source: "action", Match: "contains", Value: "agent.work"},
		},
		TaskPrompt: `Admin escalation approve flow.
1: Publish admin.escalation.requested with justification.
2: Subscribe admin.escalation.*.
3: Receive the admin decision.
4: Publish result to agent.work.result.
5: done pass.`,
	}

	brains := []struct {
		name  string
		brain agentsim.Brain
	}{
		{
			"OpenAI-v1", agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
				BaseURL:   DefaultOpenAIEndpoint,
				Model:     "Qwen3.6-35B-A3B-GGUF",
				MaxTokens: 2048,
			}),
		},
		{
			"Ollama", agentsim.NewOllamaBrain(agentsim.OllamaConfig{
				BaseURL: DefaultEndpoint,
				Model:   "Qwen3.6-35B-A3B-GGUF",
			}),
		},
	}

	for _, b := range brains {
		t.Run(b.name, func(t *testing.T) {
			socketPath, cleanup := testBus(t)
			defer cleanup()

			sub := newSubscriber()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				publishWhenReady(t, socketPath, sub.wait(), 200*time.Millisecond,
					"admin.escalation.decided", map[string]any{
						"id": "esc-q-1", "decision": "approve", "reason": "ok",
					})
			}()

			wrapped := &wrappedBrain{
				inner: b.brain,
				onAfterObserve: func(a agentsim.Action) {
					if a.Kind == agentsim.ActionSubscribe {
						sub.markReady()
					}
				},
			}

			runCfg := agentsim.RunnerConfig{
				Scenario:  scenario,
				Brain:     wrapped,
				Agent:     schema.AgentInfo{Name: "agent-sim", UID: 60010, Status: schema.StatusRunning},
				BusSocket: socketPath,
				RunID:     "qwen-" + b.name,
				Attempt:   1,
			}

			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()

			result, err := agentsim.Run(ctx, runCfg)
			wg.Wait()
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("run: %v", err)
			}

			t.Logf("Wall time: %v", elapsed.Round(time.Millisecond))
			t.Logf("Verdict: %s", result.Verdict)
			for _, obs := range result.Observations {
				m := "✅"
				if !obs.Satisfied {
					m = "❌"
				}
				t.Logf("  %s %s: %s", m, obs.OutcomeID, obs.Actual)
			}
			for i, a := range result.ActionsAttempted {
				t.Logf("  Act[%d]: %s", i, a)
			}

			if result.Brain != nil && len(result.Brain.RequestArtifacts) > 0 {
				reqLen := len(result.Brain.RequestArtifacts[0])
				t.Logf("Avg request size: ~%d bytes", reqLen/len(result.Brain.RequestArtifacts))
			}

			if result.Verdict == schema.VerdictEnvFailure {
				t.Errorf("env_failure: %s", result.FailureReason)
			}
		})
	}
}
