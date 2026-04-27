package schema

import (
	"encoding/json"
	"testing"
)

func TestRunResultSatisfied(t *testing.T) {
	t.Parallel()

	r := &RunResult{
		Observations: []EvaluatorObservation{
			{OutcomeID: "o1", Satisfied: true},
			{OutcomeID: "o2", Satisfied: false},
			{OutcomeID: "o3", Satisfied: true},
		},
	}
	if got := r.Satisfied(); got != 2 {
		t.Fatalf("Satisfied() = %d, want 2", got)
	}

	empty := &RunResult{}
	if got := empty.Satisfied(); got != 0 {
		t.Fatalf("Satisfied() on empty = %d, want 0", got)
	}
}

func TestScenarioReportComputePassRateAllPass(t *testing.T) {
	t.Parallel()

	r := &ScenarioReport{
		ScenarioID:       "test-scenario",
		ThresholdPercent: 70,
		Runs: []RunResult{
			{Verdict: VerdictPass},
			{Verdict: VerdictPass},
			{Verdict: VerdictPass},
		},
	}
	r.ComputePassRate()

	if r.Passes != 3 || r.Failures != 0 {
		t.Fatalf("passes=%d failures=%d", r.Passes, r.Failures)
	}
	if r.PassRatePercent != 100.0 {
		t.Fatalf("PassRatePercent = %f, want 100", r.PassRatePercent)
	}
	if !r.AboveThreshold {
		t.Fatal("expected AboveThreshold true")
	}
}

func TestScenarioReportComputePassRateMixed(t *testing.T) {
	t.Parallel()

	r := &ScenarioReport{
		ScenarioID:       "mixed",
		ThresholdPercent: 50,
		Runs: []RunResult{
			{Verdict: VerdictPass},
			{Verdict: VerdictFail, FailureCategory: FailureTimeout},
			{Verdict: VerdictAmbiguous, FailureCategory: FailureLLMError},
			{Verdict: VerdictEnvFailure, FailureCategory: FailureSetup},
			{Verdict: VerdictPass},
		},
	}
	r.ComputePassRate()

	if r.TotalRuns != 5 {
		t.Fatalf("TotalRuns = %d, want 5", r.TotalRuns)
	}
	if r.Passes != 2 {
		t.Fatalf("Passes = %d, want 2", r.Passes)
	}
	if r.Failures != 1 {
		t.Fatalf("Failures = %d, want 1", r.Failures)
	}
	if r.Ambiguous != 1 {
		t.Fatalf("Ambiguous = %d, want 1", r.Ambiguous)
	}
	if r.EnvFailures != 1 {
		t.Fatalf("EnvFailures = %d, want 1", r.EnvFailures)
	}
	// 2 passes out of 3 conclusive → 66.67%
	if r.PassRatePercent < 66.6 || r.PassRatePercent > 66.7 {
		t.Fatalf("PassRatePercent = %f, want ~66.67", r.PassRatePercent)
	}
	if !r.AboveThreshold {
		t.Fatal("expected AboveThreshold true for 66.67% >= 50%")
	}
}

func TestScenarioReportComputePassRateAllAmbiguous(t *testing.T) {
	t.Parallel()

	r := &ScenarioReport{
		ScenarioID:       "all-ambiguous",
		ThresholdPercent: 70,
		Runs: []RunResult{
			{Verdict: VerdictAmbiguous, FailureCategory: FailureLLMHallucinate},
			{Verdict: VerdictAmbiguous, FailureCategory: FailureLLMHallucinate},
		},
	}
	r.ComputePassRate()

	if r.ConclusiveRuns() != 0 {
		t.Fatalf("ConclusiveRuns = %d, want 0", r.ConclusiveRuns())
	}
	if r.PassRatePercent != 0 {
		t.Fatalf("PassRatePercent = %f, want 0", r.PassRatePercent)
	}
	if r.AboveThreshold {
		t.Fatal("expected AboveThreshold false for all-ambiguous")
	}
}

func TestScenarioReportComputePassRateBelowThreshold(t *testing.T) {
	t.Parallel()

	r := &ScenarioReport{
		ScenarioID:       "below",
		ThresholdPercent: 80,
		Runs: []RunResult{
			{Verdict: VerdictPass},
			{Verdict: VerdictFail, FailureCategory: FailureWrongAction},
			{Verdict: VerdictFail, FailureCategory: FailureMissingAction},
			{Verdict: VerdictPass},
		},
	}
	r.ComputePassRate()

	// 2/4 = 50% below 80%
	if r.PassRatePercent != 50.0 {
		t.Fatalf("PassRatePercent = %f, want 50", r.PassRatePercent)
	}
	if r.AboveThreshold {
		t.Fatal("expected AboveThreshold false")
	}
}

func TestScenarioReportComputePassRateEmpty(t *testing.T) {
	t.Parallel()

	r := &ScenarioReport{
		ScenarioID:       "empty",
		ThresholdPercent: 70,
		Runs:             nil,
	}
	r.ComputePassRate()

	if r.TotalRuns != 0 {
		t.Fatalf("TotalRuns = %d, want 0", r.TotalRuns)
	}
	if r.PassRatePercent != 0 {
		t.Fatalf("PassRatePercent = %f, want 0", r.PassRatePercent)
	}
	if r.AboveThreshold {
		t.Fatal("expected AboveThreshold false for empty")
	}
}

func TestSuiteReportOverallPassRate(t *testing.T) {
	t.Parallel()

	suite := &SuiteReport{
		Scenarios: []ScenarioReport{
			{ScenarioID: "s1", Passes: 3, Failures: 1},
			{ScenarioID: "s2", Passes: 2, Failures: 2},
			{ScenarioID: "s3", Passes: 0, Failures: 0, Ambiguous: 5},
		},
	}
	// s1: 3/4=75%, s2: 2/4=50%, s3: 0/0 excluded
	// total: 5/8 = 62.5%
	if got := suite.OverallPassRate(); got != 62.5 {
		t.Fatalf("OverallPassRate = %f, want 62.5", got)
	}
}

func TestSuiteReportOverallPassRateAllInconclusive(t *testing.T) {
	t.Parallel()

	suite := &SuiteReport{
		Scenarios: []ScenarioReport{
			{ScenarioID: "s1", Ambiguous: 3},
			{ScenarioID: "s2", EnvFailures: 2},
		},
	}
	if got := suite.OverallPassRate(); got != 0 {
		t.Fatalf("OverallPassRate = %f, want 0", got)
	}
}

func TestSuiteReportScenarioIDs(t *testing.T) {
	t.Parallel()

	suite := &SuiteReport{
		Scenarios: []ScenarioReport{
			{ScenarioID: "pass-1", AboveThreshold: true},
			{ScenarioID: "fail-1", AboveThreshold: false},
			{ScenarioID: "pass-2", AboveThreshold: true},
			{ScenarioID: "fail-2", AboveThreshold: false},
		},
	}

	above := suite.ScenarioIDsAboveThreshold()
	if len(above) != 2 || above[0] != "pass-1" || above[1] != "pass-2" {
		t.Fatalf("ScenarioIDsAboveThreshold = %v, want [pass-1 pass-2]", above)
	}

	below := suite.ScenarioIDsBelowThreshold()
	if len(below) != 2 || below[0] != "fail-1" || below[1] != "fail-2" {
		t.Fatalf("ScenarioIDsBelowThreshold = %v, want [fail-1 fail-2]", below)
	}
}

func TestScenarioJSONRoundTrip(t *testing.T) {
	t.Parallel()

	orig := EmpiricalScenario{
		ID:          "agent-coordination",
		Title:       "Two-Agent Coordination",
		Description: "Sender asks receiver to summarize data via event-bus-web.",
		TaskPrompt:  "Send a message to agent-2 requesting a summary.",
		SetupRequirements: []SetupRequirement{
			{Description: "event bus running", Check: "systemctl is-active agora-event-bus"},
		},
		AllowedTools: []string{"event_bus_web", "shell"},
		ExpectedOutcomes: []ExpectedOutcome{
			{
				ID:          "receiver-got-message",
				Description: "Receiver agent received the message",
				Source:      "event_bus_topic",
				Match:       "contains",
				Value:       "agent.message.60001.60002",
			},
		},
		TimeoutSeconds:             120,
		RunCount:                   5,
		SuccessThresholdPercent:    70,
		DeterministicPrerequisites: []string{"test/phase3.sh"},
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}

	var parsed EmpiricalScenario
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed.ID != orig.ID {
		t.Fatalf("ID = %q, want %q", parsed.ID, orig.ID)
	}
	if len(parsed.ExpectedOutcomes) != 1 {
		t.Fatalf("ExpectedOutcomes len = %d", len(parsed.ExpectedOutcomes))
	}
	if parsed.ExpectedOutcomes[0].ID != "receiver-got-message" {
		t.Fatalf("outcome id = %q", parsed.ExpectedOutcomes[0].ID)
	}
	if parsed.SuccessThresholdPercent != 70 {
		t.Fatalf("threshold = %d", parsed.SuccessThresholdPercent)
	}
}

func TestRunResultJSONRoundTrip(t *testing.T) {
	t.Parallel()

	orig := RunResult{
		RunID:      "scenario-1-run-001",
		ScenarioID: "agent-coordination",
		Attempt:    1,
		ActionsAttempted: []string{
			"connected to event bus",
			"sent message to agent 60002",
		},
		Observations: []EvaluatorObservation{
			{OutcomeID: "receiver-got-message", Satisfied: true, Actual: "message routed"},
		},
		Verdict:         VerdictPass,
		FailureCategory: "",
		FailureReason:   "",
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}

	var parsed RunResult
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed.RunID != "scenario-1-run-001" {
		t.Fatalf("RunID = %q", parsed.RunID)
	}
	if parsed.Verdict != VerdictPass {
		t.Fatalf("Verdict = %q", parsed.Verdict)
	}
	if parsed.Attempt != 1 {
		t.Fatalf("Attempt = %d", parsed.Attempt)
	}
}

func TestFailureCategoryCounts(t *testing.T) {
	t.Parallel()

	r := &ScenarioReport{
		ScenarioID:       "count-test",
		ThresholdPercent: 50,
		Runs: []RunResult{
			{Verdict: VerdictFail, FailureCategory: FailureTimeout},
			{Verdict: VerdictFail, FailureCategory: FailureTimeout},
			{Verdict: VerdictFail, FailureCategory: FailureWrongAction},
			{Verdict: VerdictAmbiguous, FailureCategory: FailureLLMError},
			{Verdict: VerdictEnvFailure, FailureCategory: FailureSetup},
		},
	}
	r.ComputePassRate()

	if r.FailureCategoryCounts[FailureTimeout] != 2 {
		t.Fatalf("timeout count = %d, want 2", r.FailureCategoryCounts[FailureTimeout])
	}
	if r.FailureCategoryCounts[FailureWrongAction] != 1 {
		t.Fatalf("wrong_action count = %d", r.FailureCategoryCounts[FailureWrongAction])
	}
	if r.FailureCategoryCounts[FailureLLMError] != 1 {
		t.Fatalf("llm_error count = %d", r.FailureCategoryCounts[FailureLLMError])
	}
	if r.FailureCategoryCounts[FailureSetup] != 1 {
		t.Fatalf("setup count = %d", r.FailureCategoryCounts[FailureSetup])
	}
}
