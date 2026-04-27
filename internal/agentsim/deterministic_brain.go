package agentsim

import "fmt"

// scriptedBrain is a deterministic Brain that replays a fixed sequence of
// actions. When the script is exhausted it signals ActionDone so the
// runner falls through to evaluator-based verdict computation.
type scriptedBrain struct {
	actions []Action
	next    int
}

// NewScriptedBrain returns a Brain that replays the given action sequence.
// The final action should be ActionDone; if not provided, the runner
// implicitly adds one when the script is exhausted.
func NewScriptedBrain(actions []Action) Brain {
	return &scriptedBrain{
		actions: actions,
	}
}

func (b *scriptedBrain) Observe(state StateSnapshot) (Action, error) {
	if b.next >= len(b.actions) {
		return Action{Kind: ActionDone}, nil
	}
	act := b.actions[b.next]
	b.next++

	// Validate that the action kind is known.
	switch act.Kind {
	case ActionPublish, ActionSubscribe, ActionReceive, ActionSleep, ActionDone:
		// OK
	default:
		return Action{}, fmt.Errorf("unknown action kind: %s", act.Kind)
	}

	return act, nil
}
