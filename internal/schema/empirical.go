// Package schema defines the shared types and domain constants for Phase 4
// empirical agent validation. These types capture the contract between scenario
// definitions, brain adapters, agent-sim runners, and result artifacts.
//
// Phase 4 tests are stochastic when backed by an LLM brain and emit
// pass-rate/statistics across repeated runs. Deterministic Phase 1/2/3 tests
// remain the authoritative first gate.
package schema

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Brain interface
// ---------------------------------------------------------------------------

// BrainKind discriminates the backend that drives the agent under test.
type BrainKind string

const (
	BrainDeterministic BrainKind = "deterministic" // scripted, no LLM
	BrainLocalLLM      BrainKind = "local_llm"     // Ollama or similar
)

// BrainConfig captures the model/backend identity and options for a run.
type BrainConfig struct {
	Kind         BrainKind      `json:"kind"`
	Provider     string         `json:"provider,omitempty"`      // e.g. "ollama"
	Model        string         `json:"model,omitempty"`         // e.g. "qwen3:8b"
	BaseURL      string         `json:"base_url,omitempty"`      // endpoint override
	ModelOptions map[string]any `json:"model_options,omitempty"` // temperature, top-p, seed, etc.
}

// ---------------------------------------------------------------------------
// Scenario definition
// ---------------------------------------------------------------------------

// ExpectedOutcome describes one concrete, observable success criterion for a
// scenario. The evaluator checks these against run artifacts rather than
// relying on model self-report.
type ExpectedOutcome struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	// Source specifies where the evaluator looks: log, event_bus_topic,
	// event_bus_payload, audit_log, shell_output, or surface_state.
	Source string `json:"source"`
	// Match is the assertion: contains, equals, regex, count_gte, count_lte,
	// json_path_exists.
	Match string `json:"match"`
	// Value is the expected text, regex pattern, or numeric threshold.
	Value string `json:"value"`
}

// SetupRequirement declares an environmental precondition. The runner checks
// these before starting any scenario run.
type SetupRequirement struct {
	Description string `json:"description"`
	// Check is an optional command that, if present, the runner executes to
	// verify the precondition. A zero exit code means satisfied.
	Check string `json:"check,omitempty"`
}

// EmpiricalScenario defines one Phase 4 test. The schema is designed so that
// scenarios are self-contained and evaluable without embedding LLM output into
// the definition — success criteria are deterministic assertions on observable
// system state and event trails.
type EmpiricalScenario struct {
	ID                string             `json:"id"`
	Title             string             `json:"title"`
	Description       string             `json:"description"`
	TaskPrompt        string             `json:"task_prompt"`
	SetupRequirements []SetupRequirement `json:"setup_requirements,omitempty"`
	AllowedTools      []string           `json:"allowed_tools,omitempty"`
	ExpectedOutcomes  []ExpectedOutcome  `json:"expected_outcomes"`
	// NegativeVariant signals this is an adversarial or negative scenario
	// where the expected outcome is denial, block, or other restriction.
	NegativeVariant bool `json:"negative_variant,omitempty"`
	// TimeoutSeconds per-run wall-clock limit. Default 120.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// RunCount is the number of independent attempts for the scenario.
	// Default 1 for deterministic brains; typically 5-20 for LLM brains.
	RunCount int `json:"run_count,omitempty"`
	// SuccessThresholdPercent is the minimum pass rate (0-100) required
	// for the scenario to be considered passing overall. Flaky runs
	// (below threshold) are reported as a distinct status.
	SuccessThresholdPercent int `json:"success_threshold_percent,omitempty"`
	// DeterministicPrerequisites lists Phase 1/2/3 test scripts that must
	// pass before empirical runs begin.
	DeterministicPrerequisites []string `json:"deterministic_prerequisites,omitempty"`
	// Brain specifies which backend to use. When empty the runner's
	// default (deterministic) is used.
	Brain *BrainConfig `json:"brain,omitempty"`
}

// ---------------------------------------------------------------------------
// Run result (single attempt)
// ---------------------------------------------------------------------------

// RunVerdict is the outcome of one scenario run as judged by the evaluator
// against the scenario's ExpectedOutcomes.
type RunVerdict string

const (
	VerdictPass       RunVerdict = "pass"
	VerdictFail       RunVerdict = "fail"
	VerdictAmbiguous  RunVerdict = "ambiguous"
	VerdictEnvFailure RunVerdict = "env_failure"
)

// FailureCategory classifies failed or ambiguous runs to help operators
// triage the root cause.
type FailureCategory string

const (
	FailureTimeout        FailureCategory = "timeout"
	FailureWrongAction    FailureCategory = "wrong_action"
	FailureMissingAction  FailureCategory = "missing_action"
	FailureLLMError       FailureCategory = "llm_error"
	FailureLLMHallucinate FailureCategory = "llm_hallucinate"
	FailureSetup          FailureCategory = "setup"
	FailureAssertion      FailureCategory = "assertion"
	FailureInfra          FailureCategory = "infra"
)

// EvaluatorObservation records what the evaluator saw for a single expected
// outcome.
type EvaluatorObservation struct {
	OutcomeID string `json:"outcome_id"`
	Satisfied bool   `json:"satisfied"`
	Actual    string `json:"actual,omitempty"` // what was observed
	Note      string `json:"note,omitempty"`   // evaluator commentary
}

// BrainRunInfo records the model identity and request/response metadata for
// a single LLM interaction during a run.
type BrainRunInfo struct {
	Brain BrainConfig `json:"brain"`
	Seed  *int64      `json:"seed,omitempty"`
	// RequestArtifacts is a list of file paths containing the raw prompt
	// and related request payloads sent to the brain.
	RequestArtifacts []string `json:"request_artifacts,omitempty"`
	// ResponseArtifacts is a list of file paths containing raw responses
	// from the brain.
	ResponseArtifacts []string `json:"response_artifacts,omitempty"`
}

// RunResult is the structured artifact produced after a single scenario
// attempt completes and is evaluated.
type RunResult struct {
	RunID      string    `json:"run_id"`
	ScenarioID string    `json:"scenario_id"`
	Attempt    int       `json:"attempt"` // 1-based attempt number
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// TranscriptRef is the path to a human-readable transcript (agent
	// stdout/stderr, brain interaction log).
	TranscriptRef string `json:"transcript_ref,omitempty"`
	// EventLogRef is the path to a structured event log (JSONL).
	EventLogRef string `json:"event_log_ref,omitempty"`
	// Brain is populated when the brain is LLM-backed.
	Brain *BrainRunInfo `json:"brain,omitempty"`
	// ActionsAttempted is a human-readable summary of what the agent
	// tried during the run.
	ActionsAttempted []string `json:"actions_attempted,omitempty"`
	// Observations records evaluator judgments for each expected outcome.
	Observations []EvaluatorObservation `json:"observations,omitempty"`
	// Verdict is the evaluator's final call for this run.
	Verdict RunVerdict `json:"verdict"`
	// FailureCategory is set when verdict is fail, ambiguous, or
	// env_failure.
	FailureCategory FailureCategory `json:"failure_category,omitempty"`
	// FailureReason is a detailed explanation of the failure or
	// ambiguity.
	FailureReason string `json:"failure_reason,omitempty"`
	// RawJSON holds the full evaluator output for archival/debugging.
	RawJSON json.RawMessage `json:"raw_json,omitempty"`
}

// Satisfied returns the count of satisfied observations.
func (r *RunResult) Satisfied() int {
	n := 0
	for _, o := range r.Observations {
		if o.Satisfied {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Scenario aggregate report
// ---------------------------------------------------------------------------

// ScenarioReport aggregates RunResults across all attempts for one scenario.
type ScenarioReport struct {
	ScenarioID            string                  `json:"scenario_id"`
	TotalRuns             int                     `json:"total_runs"`
	Passes                int                     `json:"passes"`
	Failures              int                     `json:"failures"`
	Ambiguous             int                     `json:"ambiguous"`
	EnvFailures           int                     `json:"env_failures"`
	PassRatePercent       float64                 `json:"pass_rate_percent"`
	AboveThreshold        bool                    `json:"above_threshold"`
	ThresholdPercent      int                     `json:"threshold_percent"`
	FailureCategoryCounts map[FailureCategory]int `json:"failure_category_counts,omitempty"`
	Runs                  []RunResult             `json:"runs"`
}

// ComputePassRate derives PassRatePercent and AboveThreshold from the raw
// counts and the given threshold. Passes are counted as successful runs.
// Ambiguous and env_failure runs are reported separately and do not count
// toward the pass-rate numerator or denominator. This keeps the pass rate
// meaningful: it reflects runs that definitively passed or failed the
// scenario, while flaky/runners (ambiguous) and environment issues are
// surfaced without contaminating the statistic.
//
// An empty set of conclusive runs (zero passes + zero failures) returns a
// zero pass rate with AboveThreshold = false, regardless of threshold, so
// that "all ambiguous" or "all env failure" is never mistaken for a pass.
func (r *ScenarioReport) ComputePassRate() {
	r.TotalRuns = len(r.Runs)
	r.Passes = 0
	r.Failures = 0
	r.Ambiguous = 0
	r.EnvFailures = 0
	r.FailureCategoryCounts = make(map[FailureCategory]int)

	for _, run := range r.Runs {
		switch run.Verdict {
		case VerdictPass:
			r.Passes++
		case VerdictFail:
			r.Failures++
			r.FailureCategoryCounts[run.FailureCategory]++
		case VerdictAmbiguous:
			r.Ambiguous++
			r.FailureCategoryCounts[run.FailureCategory]++
		case VerdictEnvFailure:
			r.EnvFailures++
			r.FailureCategoryCounts[run.FailureCategory]++
		}
	}

	conclusive := r.Passes + r.Failures
	if conclusive == 0 {
		r.PassRatePercent = 0
		r.AboveThreshold = false
	} else {
		r.PassRatePercent = float64(r.Passes) / float64(conclusive) * 100
		r.AboveThreshold = r.PassRatePercent >= float64(r.ThresholdPercent)
	}
}

// ConclusiveRuns returns the number of runs that produced a definitive
// pass or fail, excluding ambiguous and env_failure runs.
func (r *ScenarioReport) ConclusiveRuns() int {
	return r.Passes + r.Failures
}

// ---------------------------------------------------------------------------
// Suite aggregate (multiple scenarios)
// ---------------------------------------------------------------------------

// SuiteReport aggregates ScenarioReports across multiple scenarios.
type SuiteReport struct {
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt time.Time        `json:"finished_at"`
	Scenarios  []ScenarioReport `json:"scenarios"`
}

// OverallPassRate returns the pass rate across all scenarios weighted by
// their conclusive runs.
func (s *SuiteReport) OverallPassRate() float64 {
	var totalPasses, totalConclusive int
	for _, sc := range s.Scenarios {
		totalPasses += sc.Passes
		totalConclusive += sc.ConclusiveRuns()
	}
	if totalConclusive == 0 {
		return 0
	}
	return float64(totalPasses) / float64(totalConclusive) * 100
}

// ScenarioIDsAboveThreshold returns the IDs of scenarios whose pass rate
// met or exceeded their configured threshold.
func (s *SuiteReport) ScenarioIDsAboveThreshold() []string {
	var ids []string
	for _, sc := range s.Scenarios {
		if sc.AboveThreshold {
			ids = append(ids, sc.ScenarioID)
		}
	}
	return ids
}

// ScenarioIDsBelowThreshold returns the IDs of scenarios whose pass rate
// fell below their configured threshold.
func (s *SuiteReport) ScenarioIDsBelowThreshold() []string {
	var ids []string
	for _, sc := range s.Scenarios {
		if !sc.AboveThreshold {
			ids = append(ids, sc.ScenarioID)
		}
	}
	return ids
}
