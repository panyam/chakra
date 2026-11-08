package agent

import (
	"context"
	"errors"
)

// TurnEndHook runs when the model answers without calling a tool, before the
// turn is accepted (#150). It accepts by returning the zero verdict. It
// refuses by returning a continuation message. Examples are "you said done
// but the tests are red", a required artifact that's missing, or a cheaper
// model's completion check. An error fails the turn.
type TurnEndHook func(ctx context.Context, info TurnEndInfo) (TurnEndVerdict, error)

// TurnEndInfo holds what a TurnEndHook judges.
type TurnEndInfo struct {
	// Step is the model call that produced the answer, counting from one,
	// and MaxSteps is the turn's cap. A free refusal doesn't use up a step,
	// so Step can pass MaxSteps (#281).
	Step, MaxSteps int
	// StepsLeft is how many steps the turn may still count, this one
	// included. A counted Continue at StepsLeft == 1 ends the turn with
	// ErrMaxSteps.
	StepsLeft int
	// Text is the model's final answer. With RunnerConfig.ResponseGuided set,
	// it is the constrained answer the turn would return.
	Text string
	// Draft is the model's free answer when a constraint rewrote it. With
	// ResponseSchema set, Text is the finalized JSON document and Draft is
	// the prose it came from. With ResponseGuided, Draft is set when the
	// constraint needed a call of its own after a step that offered tools.
	// Otherwise it is empty, including when the step itself was guided.
	Draft string
	// StructuredError says why the finalized document doesn't validate
	// against ResponseSchema after every finalizing attempt. A hook can then
	// refuse it and send the model back. It is empty when the document
	// validates or no schema is set. If the answer is accepted, it becomes
	// TurnResult.StructuredError.
	StructuredError string
	// Messages is everything the turn has recorded so far, the answer last.
	// Hooks must not modify it.
	Messages []Message
	// Usage counts what the turn has spent so far.
	Usage Usage
}

// TurnEndVerdict is a TurnEndHook's decision. The zero value accepts. At
// most one of Continue and Stop may be set, and a hook that sets both fails
// the turn.
type TurnEndVerdict struct {
	// Continue refuses the answer when not empty. The text is appended to
	// the turn as a note, and the loop takes another step. The note goes in
	// the record rather than the view. So the record keeps the reason that
	// the model kept going.
	Continue string
	// AsUser sends the Continue note as a RoleUser message instead of a
	// RoleSystem one. Some chat templates reject or drop a system message
	// that isn't the first. Gemma's and Mistral's on llama.cpp are among
	// them. Feedback on a draft often reads better from the user anyway.
	// Ignored without Continue.
	AsUser bool
	// Free makes a Continue cost no step (#281), up to
	// RunnerConfig.MaxFreeRefusals per turn. Past the cap the Continue
	// counts. Use it when the answer was refused for something cheap to
	// check, such as a draft that repeats the last one. Ignored without
	// Continue.
	Free bool
	// Stop refuses the answer when not empty, and ends the turn without
	// another step. The turn returns an error wrapping ErrTurnStopped, with
	// Stop as the reason. It also returns a partial TurnResult, and its
	// Messages end with the refused answer. So the caller can see what was
	// refused. Use Stop when asking again won't help. For example, the model
	// resent a draft that it was told was wrong, or the caller counts answer
	// tries apart from MaxSteps.
	Stop string
}

// ErrTurnStopped means a TurnEndHook's verdict set Stop, or a StepStage
// returned an error wrapping it, such as NewStuckDetector under StuckStop
// (#278). Run returns it wrapped, with the reason. Like ErrMaxSteps, it comes
// with a partial TurnResult, and Text is empty. After a turn-end Stop,
// Messages holds the turn's record with the refused answer last. After a
// stage stop, it holds the record up to the step the stage ran before, and
// that step's model call never happens.
var ErrTurnStopped = errors.New("agent: turn stopped")

// turnEnd runs the hooks in order and returns the first verdict that doesn't
// accept. When every hook accepts, it returns the zero verdict.
func (r *Runner) turnEnd(ctx context.Context, info TurnEndInfo) (TurnEndVerdict, error) {
	for _, h := range r.cfg.TurnEnd {
		v, err := h(ctx, info)
		if err != nil {
			return TurnEndVerdict{}, err
		}
		if v.Continue != "" && v.Stop != "" {
			return TurnEndVerdict{}, errors.New("verdict sets both Continue and Stop")
		}
		if v.Continue != "" || v.Stop != "" {
			return v, nil
		}
	}
	return TurnEndVerdict{}, nil
}
