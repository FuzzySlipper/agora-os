// Command agent-sim is a standalone agent simulator that exercises the
// event-bus, shell, and webview APIs under a real agent uid.
//
// It reads a scenario definition and (optionally) a deterministic action
// script, connects to the event bus, runs the brain observe-act loop,
// evaluates against expected outcomes, and writes the structured
// RunResult to stdout.
//
// Usage:
//
//	agent-sim --scenario scenario.json [--script script.json] [--bus /run/agent-os/bus.sock]
//
// When launched by the isolation service, the process already runs as the
// target agent uid and the bus socket is available at the default path.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/patch/agora-os/internal/agentsim"
	"github.com/patch/agora-os/internal/schema"
)

func main() {
	scenarioPath := flag.String("scenario", "", "path to scenario JSON (required)")
	scriptPath := flag.String("script", "", "path to deterministic action script JSON (optional; defaults to implicit done)")
	busSocket := flag.String("bus", schema.BusSocket, "event bus socket path")
	runID := flag.String("run-id", "", "run identifier (defaults to scenario-id-timestamp)")
	attempt := flag.Int("attempt", 1, "1-based attempt number")
	agentName := flag.String("agent-name", "agent-sim", "agent name for identity")
	artifactDir := flag.String("artifact-dir", "", "directory for per-run artifacts (transcript, events)")
	timeoutSec := flag.Int("timeout", 300, "run timeout in seconds")
	flag.Parse()

	if *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "agent-sim: --scenario is required")
		os.Exit(2)
	}

	// Load scenario.
	scenario, err := loadScenario(*scenarioPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-sim: load scenario: %v\n", err)
		os.Exit(1)
	}

	// Build brain.
	var brain agentsim.Brain
	if *scriptPath != "" {
		actions, err := loadScript(*scriptPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent-sim: load script: %v\n", err)
			os.Exit(1)
		}
		brain = agentsim.NewScriptedBrain(actions)
	} else {
		// Default: brain immediately signals done so the evaluator runs.
		brain = agentsim.NewScriptedBrain(nil)
	}

	// Resolve run ID.
	rid := *runID
	if rid == "" {
		rid = fmt.Sprintf("%s-%s", scenario.ID, time.Now().Format("20060102-150405"))
	}

	// Determine agent identity.
	agent := agentsim.PeerUIDAgent(*agentName)

	// Run.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	cfg := agentsim.RunnerConfig{
		Scenario:    scenario,
		Brain:       brain,
		Agent:       agent,
		BusSocket:   *busSocket,
		RunID:       rid,
		Attempt:     *attempt,
		ArtifactDir: *artifactDir,
	}

	result, err := agentsim.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-sim: run: %v\n", err)
		os.Exit(1)
	}

	// Write result to stdout as JSON.
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "agent-sim: encode result: %v\n", err)
		os.Exit(1)
	}

	// Exit code matches verdict.
	switch result.Verdict {
	case schema.VerdictPass:
		os.Exit(0)
	case schema.VerdictFail, schema.VerdictAmbiguous:
		os.Exit(1)
	default: // env_failure
		os.Exit(2)
	}
}

func loadScenario(path string) (schema.EmpiricalScenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return schema.EmpiricalScenario{}, err
	}
	var sc schema.EmpiricalScenario
	if err := json.Unmarshal(data, &sc); err != nil {
		return schema.EmpiricalScenario{}, fmt.Errorf("invalid scenario JSON: %w", err)
	}
	if sc.ID == "" {
		return schema.EmpiricalScenario{}, fmt.Errorf("scenario missing id")
	}
	return sc, nil
}

func loadScript(path string) ([]agentsim.Action, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var actions []agentsim.Action
	if err := json.Unmarshal(data, &actions); err != nil {
		return nil, fmt.Errorf("invalid script JSON: %w", err)
	}
	return actions, nil
}
