//go:build integration

// Run: go test -tags integration -run TestIntegration_QwenBrevity -timeout 600s ./internal/agentsim/
package agentsim_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/agentsim"
	"github.com/patch/agora-os/internal/schema"
)

// TestIntegration_QwenBrevity compares Qwen performance with standard vs
// brevity-stressed prompts on the same scenario.
func TestIntegration_QwenBrevity(t *testing.T) {
	scenario := schema.EmpiricalScenario{
		ID:                     "qwen-brevity-test",
		Title:                  "Admin escalation — approve",
		RunCount:               1,
		SuccessThresholdPercent: 100,
		TimeoutSeconds:         60,
		ExpectedOutcomes: []schema.ExpectedOutcome{
			{ID: "submitted", Source: "action", Match: "contains", Value: "admin.escalation.requested"},
			{ID: "received", Source: "event_bus_topic", Match: "equals", Value: "admin.escalation.decided"},
			{ID: "handled", Source: "action", Match: "contains", Value: "agent.work"},
		},
	}

	type runConfig struct {
		name       string
		taskPrompt string
		maxTokens  int
	}

	configs := []runConfig{
		{
			name: "standard-prompt",
			taskPrompt: `Admin escalation approve flow.
Step 1: Publish an escalation request to "admin.escalation.requested" with justification.
Step 2: Subscribe to "admin.escalation.*".
Step 3: Receive the admin decision event.
Step 4: Publish the result to "agent.work.result".
Step 5: When done, signal with done_verdict="pass".`,
			maxTokens: 2048,
		},
		{
			name: "brevity-prompt",
			taskPrompt: `Admin escalation. Be concise — keep reply under 100 tokens.
1: publish admin.escalation.requested {justification:"...")
2: subscribe admin.escalation.*
3: receive
4: publish agent.work.result with decision
5: done pass`,
			maxTokens: 1024,
		},
	}

	for _, cfg := range configs {
		t.Run(cfg.name, func(t *testing.T) {
			socketPath, cleanup := testBus(t)
			defer cleanup()

			temperature := 0.7
			seed := int64(42)
			brain := agentsim.NewOpenAIBrain(agentsim.OpenAIConfig{
				BaseURL:    DefaultOpenAIEndpoint,
				Model:      "Qwen3.6-35B-A3B-GGUF",
				MaxTokens:  cfg.maxTokens,
				Temperature: &temperature,
				Seed:       &seed,
			})

			scenario.TaskPrompt = cfg.taskPrompt

			sub := newSubscriber()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				publishWhenReady(t, socketPath, sub.wait(), 200*time.Millisecond,
					"admin.escalation.decided", map[string]any{
						"id": "esc-br-1", "decision": "approve", "reason": "ok",
					})
			}()

			wrapped := &wrappedBrain{
				inner: brain,
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
				RunID:     "qwen-brevity-" + cfg.name,
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

			t.Logf("Wall time: %v", elapsed)
			t.Logf("Verdict: %s", result.Verdict)
			if result.FailureReason != "" {
				t.Logf("Reason: %s", result.FailureReason)
			}
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

			// Log token usage from the response artifacts
			if result.Brain != nil && len(result.Brain.ResponseArtifacts) > 0 {
				last := result.Brain.ResponseArtifacts[len(result.Brain.ResponseArtifacts)-1]
				t.Logf("Last response (%.300s...)", last)
			}

			if result.Verdict == schema.VerdictEnvFailure {
				t.Errorf("env_failure: %s", result.FailureReason)
			}
		})
	}
}
