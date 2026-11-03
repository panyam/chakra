package agent

import (
	"context"
	"errors"
)

// TurnEndHook runs when the model answers without calling a tool, before the
// turn is accepted (#150). It accepts by returning the zero verdict, or
// refuses with a continuation message: "you said done but the tests are
// red", a required artifact that's missing, a cheaper model's completion
// check. An error fails the turn.
type TurnEndHook func(ctx context.Context, info TurnEndInfo) (TurnEndVerdict, error)

// TurnEndInfo is what a TurnEndHook judges.
type TurnEndInfo struct {
	// Step is the step that produced the answer, and MaxSteps the turn's
	// cap. A Continue at Step == MaxSteps ends the turn with ErrMaxSteps.
	Step, MaxSteps int
	// Text is the model's final answer. With RunnerConfig.ResponseGuided set,
	// it is the constrained answer the turn would return.
	Text string
	// Draft is the model's free answer when a constraint rewrote it: with
	// ResponseSchema set, Text is the finalized JSON document and Draft the
	// prose it came from; with ResponseGuided, when the constraint needed a
	// call of its own after a step that offered tools. Empty otherwise,
	// including when the step itself was guided.
	Draft string
	// StructuredError says why the finalized document doesn't validate
	// against ResponseSchema, after every finalizing attempt, so a hook can
	// refuse it and send the model back. Empty when it validates or no schema
	// is set. It becomes TurnResult.StructuredError if the answer is accepted.
	StructuredError string
	// Messages is everything the turn has recorded so far, the answer last.
	// Hooks must not modify it.
	Messages []Message
	// Usage is what the turn has spent so far.
	Usage Usage
}

// TurnEndVerdict is a TurnEndHook's decision. The zero value accepts; at
// most one of Continue and Stop may be set, and a hook that sets both fails
// the turn.
type TurnEndVerdict struct {
	// Continue, when not empty, refuses the answer: the text is appended to
	// the turn as a note, which is record rather than view so the reason the
	// model kept going survives, and the loop takes another step.
	Continue string
	// AsUser sends the Continue note as a RoleUser message instead of a
	// RoleSystem one. Some chat templates (Gemma's and Mistral's on
	// llama.cpp among them) reject or drop a system message that isn't the
	// first, and feedback on a draft often reads better from the user
	// anyway. Ignored without Continue.
	AsUser bool
	// Stop, when not empty, refuses the answer and ends the turn without
	// another step: the turn returns an error wrapping ErrTurnStopped with
	// Stop as the reason, and a partial TurnResult whose Messages end with
	// the refused answer, so the caller can see what was refused. Use it
	// when asking again won't help: the model resent a draft it was told was
	// wrong, or the caller counts answer tries apart from MaxSteps.
	Stop string
}

// ErrTurnStopped is returned (wrapped, with the hook's reason) by Run when a
// TurnEndHook's verdict set Stop. Like ErrMaxSteps it comes with a partial
// TurnResult: Text is empty and Messages holds the turn's record, the
// refused answer last.
var ErrTurnStopped = errors.New("agent: turn stopped by a turn-end hook")

// turnEnd runs the hooks in order and returns the first verdict that doesn't
// accept, the zero verdict when every hook accepts.
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
