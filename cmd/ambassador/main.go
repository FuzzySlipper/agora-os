// 3PO ambassador: human-facing frontier agent that translates human intent into
// structured work orders, delegates to R2 workers via the supervisor, and
// synthesizes results back into human-readable responses.
//
// This file is the composition root — config loading, bus connection, signal
// handling, and dependency wiring. All 3PO logic lives in internal/ambassador.
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/patch/agora-os/internal/ambassador"
	"github.com/patch/agora-os/internal/llm"
	"github.com/patch/agora-os/internal/schema"
)

func main() {
	promptPath := os.Getenv("AMBASSADOR_PROMPT")
	if promptPath == "" {
		promptPath = "config/ambassador-system-prompt.md"
	}

	promptBytes, err := os.ReadFile(promptPath)
	if err != nil {
		log.Printf("load system prompt: %v; using built-in prompt", err)
		promptBytes = []byte(defaultSystemPrompt)
	}

	llmClient := llm.NewClient(
		llm.WithSystemPrompt(string(promptBytes)),
	)

	amb := ambassador.New(ambassador.Config{
		BusSocket:        schema.BusSocket,
		SupervisorSocket: schema.SupervisorSocket,
		LLMClient:        llmClient,
	})

	if err := amb.Start(); err != nil {
		log.Fatalf("ambassador start: %v", err)
	}
	defer amb.Stop()

	log.Printf("3PO ambassador connected to bus %s", schema.BusSocket)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down")
}

const defaultSystemPrompt = `You are the 3PO ambassador, the human-facing agent in the Agora OS agent framework.

Your responsibilities:
1. Translate human requests into structured work orders for R2 workers
2. Decide whether a request should be answered directly, delegated to R2 workers, or escalated to admin
3. Synthesize R2 worker results into coherent human-readable responses
4. Handle ambiguity by asking follow-up questions

When classifying a request, choose one of:
- direct_answer: answer using your own reasoning (for simple questions, greetings, clarification)
- delegate: create structured work orders for R2 workers (for file operations, code review, system tasks)
- ask_followup: when the request is ambiguous or missing critical information
- escalate_admin: when the request involves privileged operations that require admin approval

When delegating, specify:
- worker_profile: which R2 profile to use (repo-inspector, patch-writer, ui-observer, etc.)
- objective: clear, bounded task description
- inputs: structured parameters the worker needs
- budget: reasonable step/time limits

You are not privileged. You do not create Linux users, do not talk to systemd directly, and do not bypass the admin agent.`
