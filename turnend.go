package agent

import "context"

// TurnEndHook runs when the model answers without calling a tool, before the
// turn is accepted (#150). It accepts by returning the zero verdict, or
// refuses with a continuation message: "you said done but the tests are
// red", a required artifact that's missing, a cheaper model's completion
// check. An error fails the turn.
type TurnEndHook func(ctx context.Context, info TurnEndInfo) (TurnEndVerdict, error)

// TurnEndInfo is what a TurnEndHook judges.
type TurnEndInfo struct {
	// Step is the step that produced the answer, and MaxSteps the turn's
	// cap. A refusal at Step == MaxSteps ends the turn with ErrMaxSteps.
	Step, MaxSteps int
	// Text is the model's final answer: with RunnerConfig.ResponseGuided set,
	// the constrained answer, which is what the turn would return.
	Text string
	// Draft is the model's free answer when a constraint rewrote it, that is
	// when ResponseGuided needed a call of its own after a step that offered
	// tools. Empty otherwise, including when the step itself was guided.
	Draft string
	// Messages is everything the turn has recorded so far, the answer last.
	// Hooks must not modify it.
	Messages []Message
	// Usage is what the turn has spent so far.
	Usage Usage
}

// TurnEndVerdict is a TurnEndHook's decision. The zero value accepts.
type TurnEndVerdict struct {
	// Continue, when not empty, refuses the answer: the text is appended to
	// the turn as a RoleSystem note, which is record rather than view so the
	// reason the model kept going survives, and the loop takes another step.
	Continue string
}

// turnEnd runs the hooks in order and returns the first refusal's message,
// "" when every hook accepts.
func (r *Runner) turnEnd(ctx context.Context, info TurnEndInfo) (string, error) {
	for _, h := range r.cfg.TurnEnd {
		v, err := h(ctx, info)
		if err != nil {
			return "", err
		}
		if v.Continue != "" {
			return v.Continue, nil
		}
	}
	return "", nil
}
