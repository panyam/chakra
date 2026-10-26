package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/panyam/mcpkit/core"
)

// ErrContextBudget is returned (wrapped) by Run when a turn has grown past its
// compactor's budget and compacting it again did not bring it back under. Like
// ErrMaxSteps it comes with a partial TurnResult, so a caller can keep what the
// turn got through: nothing went wrong but the budget.
var ErrContextBudget = errors.New("agent: context budget exceeded")

// StepScope is how long what a StepStage produces lives. Every stage in the
// chain picks one, and the chain runs them in scope order: all session stages,
// then turn stages, then request stages.
//
// The scope is a stage's main contract rather than a detail, because getting
// it wrong is silent and compounding. A note meant for one request that lands
// in the record is re-sent forever. A summary meant for this turn that lands
// in the record gets summarized again next turn alongside real conversation.
// An injection meant for the record that only reaches one request leaves no
// trace of why the model did what it did. See docs/HOOKS.md, Record or view.
type StepScope int

const (
	// ScopeSession stages add messages to the turn's record. What they add
	// joins TurnResult.Messages, so the host keeps it in history and the
	// session store, and every later step and turn sees it. Append-only: a
	// session stage that changes or drops a message it was given fails the
	// turn, since the record is what already happened.
	// Stages run before every step, so one that means to add a message once
	// checks the messages it is given for its own earlier addition.
	ScopeSession StepScope = iota

	// ScopeTurn stages rewrite the working messages for the rest of this turn
	// and no further. Compaction is the example: the summary replaces the
	// head for every later step, so the summarizer runs once rather than on
	// every step, but the record keeps every message.
	ScopeTurn

	// ScopeRequest stages shape one model call. Nothing they return is kept,
	// so the next step starts from the working messages again. A tool
	// selector and a note about the step budget are examples.
	ScopeRequest
)

// StepInfo is where in the turn a stage is running.
type StepInfo struct {
	// Step is the model call about to be made, counting from one.
	Step int
	// MaxSteps is the turn's step cap.
	MaxSteps int
	// Usage is what the turn has spent so far, before this step.
	Usage Usage
}

// StepView is what one model call will send, as far as stages may change it.
type StepView struct {
	// Messages is the conversation the model will read.
	Messages []Message
	// Tools is the tool set offered on this call. Only a ScopeRequest stage's
	// change to it reaches the model; tools are listed afresh every step, so
	// there is no record or rest-of-turn for a tool set to persist into.
	Tools []core.ToolDef
}

// StepStage is one entry in the chain that runs before every model call
// (RunnerConfig.StepStages). It is the "before each model call" hook point in
// docs/HOOKS.md, which before this had a separate RunnerConfig field per
// feature and an order nobody had chosen.
type StepStage struct {
	// Name identifies the stage in errors. Use a dotted, contributor-scoped
	// form ("host.step_budget").
	Name string

	// Scope is how long what Run returns lives. See StepScope.
	Scope StepScope

	// Run returns the view with this stage's contribution folded in, or the
	// view unchanged when it has nothing to add. An error ends the turn.
	Run func(ctx context.Context, info StepInfo, view StepView) (StepView, error)
}

// compactionStage is RunnerConfig.Compactor as a ScopeTurn stage. It used to
// run once, before the first step, which left a single turn that grows past
// the budget (#7) uncovered. As a stage it runs before every step; a Compactor
// is a no-op under budget, so a short turn never calls the summarizer.
//
// After the first step it also enforces the floor: if the compactor can say
// whether messages fit (SummarizingCompactor can) and they still don't after
// compacting, the turn ends with ErrContextBudget instead of compacting a
// summary of a summary on every step. Step 1 is exempt and keeps the old
// behavior, sending what the user gave it, because a large first message is
// the user's to send, while growth after that is the turn's own.
func compactionStage(c Compactor, emit func(Event)) StepStage {
	return StepStage{
		Name:  "agent.compaction",
		Scope: ScopeTurn,
		Run: func(ctx context.Context, info StepInfo, v StepView) (StepView, error) {
			before := len(v.Messages)
			compacted, err := c.Compact(ctx, v.Messages)
			if err != nil {
				return v, fmt.Errorf("agent: compaction: %w", err)
			}
			if len(compacted) != before {
				v.Messages = compacted
				emit(Event{Kind: EventCompaction, Step: info.Step, Compaction: &CompactionInfo{Before: before, After: len(compacted)}})
			}
			if f, ok := c.(interface{ Fits([]Message) bool }); ok && info.Step > 1 && !f.Fits(v.Messages) {
				return v, fmt.Errorf("%w at step %d, even after compacting", ErrContextBudget, info.Step)
			}
			return v, nil
		},
	}
}

// selectorStage is RunnerConfig.Selector as a ScopeRequest stage.
func selectorStage(sel ToolSelector) StepStage {
	return StepStage{
		Name:  "agent.selector",
		Scope: ScopeRequest,
		Run: func(ctx context.Context, _ StepInfo, v StepView) (StepView, error) {
			tools, err := sel(ctx, v.Messages, v.Tools)
			if err != nil {
				return v, fmt.Errorf("agent: tool selector: %w", err)
			}
			v.Tools = tools
			return v, nil
		},
	}
}

// StepBudgetStage tells the model how many steps it has left once that number
// is at or below within. Without it a turn runs into MaxSteps without warning,
// often mid-way through writing a test (#109), and leaves the user nothing
// that says where it got to. The note is per request, so it never joins the
// record and only costs tokens on the steps it appears in.
//
// Within of zero or less means a quarter of MaxSteps, and at least 3.
func StepBudgetStage(within int) StepStage {
	return StepStage{
		Name:  "agent.step_budget",
		Scope: ScopeRequest,
		Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
			w := within
			if w <= 0 {
				w = max(info.MaxSteps/4, 3)
			}
			left := info.MaxSteps - info.Step
			if left > w {
				return v, nil
			}
			text := fmt.Sprintf("Step %d of %d in this turn: %d left after this one.", info.Step, info.MaxSteps, left)
			if left == 0 {
				text = fmt.Sprintf("Step %d of %d: this is the last step in this turn.", info.Step, info.MaxSteps)
			}
			text += " If the task won't fit, finish the step you're on and tell the user where you got to and what's left, rather than starting something new."
			v.Messages = append(slices.Clip(v.Messages), Message{Role: RoleSystem, Text: text})
			return v, nil
		},
	}
}

// stepChain is the ordered stage list for one Runner: the Compactor and
// Selector fields as built-in stages, then RunnerConfig.StepStages, sorted
// stably by scope so the configured order holds within each scope.
//
// The built-ins go first in their scopes. Compaction first among turn stages
// means a later turn stage works on messages that already fit. The selector
// first among request stages keeps its old position, before anything a
// caller adds.
func (r *Runner) stepChain(emit func(Event)) []StepStage {
	var chain []StepStage
	if r.cfg.Compactor != nil {
		chain = append(chain, compactionStage(r.cfg.Compactor, emit))
	}
	if r.cfg.Selector != nil {
		chain = append(chain, selectorStage(r.cfg.Selector))
	}
	chain = append(chain, r.cfg.StepStages...)
	slices.SortStableFunc(chain, func(a, b StepStage) int { return int(a.Scope) - int(b.Scope) })
	return chain
}

// runStepChain applies the chain before one model call. It returns the
// working messages for the rest of the turn, the messages session stages
// added to the record, and the view to send.
func runStepChain(ctx context.Context, chain []StepStage, info StepInfo, working []Message, tools []core.ToolDef) (newWorking, recorded []Message, view StepView, err error) {
	view = StepView{Messages: working, Tools: tools}
	for _, st := range chain {
		in := view.Messages
		// A stage gets a copy it can append to without writing into the
		// caller's backing array.
		view.Messages = slices.Clip(view.Messages)
		out, err := st.Run(ctx, info, view)
		if err != nil {
			return nil, nil, StepView{}, fmt.Errorf("step stage %q: %w", st.Name, err)
		}
		switch st.Scope {
		case ScopeSession:
			if len(out.Messages) < len(in) || !slices.EqualFunc(out.Messages[:len(in)], in, sameMessage) {
				return nil, nil, StepView{}, fmt.Errorf("step stage %q: a session stage may only append to the record", st.Name)
			}
			recorded = append(recorded, out.Messages[len(in):]...)
			working = out.Messages
			out.Tools = view.Tools
		case ScopeTurn:
			working = out.Messages
			out.Tools = view.Tools
		}
		view = out
	}
	return working, recorded, view, nil
}

// sameMessage compares two messages by identity of content, which is enough
// to tell an append from a rewrite.
func sameMessage(a, b Message) bool {
	return a.Role == b.Role && a.Text == b.Text && a.ToolCallID == b.ToolCallID && len(a.ToolCalls) == len(b.ToolCalls)
}
