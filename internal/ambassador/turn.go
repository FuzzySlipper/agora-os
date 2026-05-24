package ambassador

import (
	"encoding/json"
	"fmt"

	"github.com/patch/agora-os/internal/schema"
)

// TurnAction is the high-level decision for how to handle a conversation turn.
type TurnAction string

const (
	ActionDirectAnswer TurnAction = "direct_answer"
	ActionDelegate     TurnAction = "delegate"
	ActionAskFollowup  TurnAction = "ask_followup"
	ActionEscalateAdmin TurnAction = "escalate_admin"
)

// WorkerRequest describes one R2 worker assignment within a delegation.
type WorkerRequest struct {
	Profile   string          `json:"profile"`
	Objective string          `json:"objective"`
	Inputs    json.RawMessage `json:"inputs,omitempty"`
	Budget    schema.WorkBudget `json:"budget,omitempty"`
}

// TurnClassification is the structured output from the LLM classification step.
type TurnClassification struct {
	Action           TurnAction       `json:"action"`
	Context          string           `json:"context,omitempty"`
	FollowUpQuestion string           `json:"follow_up_question,omitempty"`
	WorkerRequests   []WorkerRequest  `json:"worker_requests,omitempty"`
	AdminAction      string           `json:"admin_action,omitempty"`
	AdminResource    string           `json:"admin_resource,omitempty"`
	Justification    string           `json:"justification,omitempty"`
	Result           json.RawMessage  `json:"result,omitempty"`
}

// Validate checks that a classification has the required fields for its action.
func (tc *TurnClassification) Validate() error {
	switch tc.Action {
	case ActionDirectAnswer, ActionAskFollowup, ActionEscalateAdmin:
		return nil
	case ActionDelegate:
		if len(tc.WorkerRequests) == 0 {
			return fmt.Errorf("delegate action requires at least one worker_request")
		}
		for i, wr := range tc.WorkerRequests {
			if wr.Profile == "" {
				return fmt.Errorf("worker_request[%d]: profile is required", i)
			}
			if wr.Objective == "" {
				return fmt.Errorf("worker_request[%d]: objective is required", i)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown action: %s", tc.Action)
	}
}
