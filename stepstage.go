package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/panyam/mcpkit/core"
)

// ErrContextBudget means a turn grew past its compactor's budget, and
// compacting it again did not bring it back under. Run returns it wrapped.
// Like ErrMaxSteps, it comes with a partial TurnResult. Nothing went wrong
// except the budget, so a caller can keep what the turn got through.
var ErrContextBudget = errors.New("agent: context budget exceeded")

// StepScope says how long a StepStage's output lives. Every stage in the
// chain picks one. The chain runs stages in scope order, with all session
// stages first, then turn stages, then request stages.
//
// The scope is a stage's main contract rather than a detail. A wrong scope
// fails silently, and the damage compounds. Imagine a note meant for one
// request ends up in the record. It is re-sent forever. Or a summary meant
// for this turn ends up in the record. Next turn it gets summarized again
// alongside real conversation. Or an injection meant for the record reaches
// only one request. Then nothing records why the model did what it did. See
// docs/HOOKS.md, Record or view.
type StepScope int

const (
	// ScopeSession stages add messages to the turn's record. Their messages
	// join TurnResult.Messages. So the host keeps them in history and the
	// session store, and every later step and turn sees them. The record
	// holds what already happened, so it is append-only. A session stage
	// that changes or drops a message it was given fails the turn.
	// Stages run before every step. So a stage that means to add a message
	// once checks its input messages for its own earlier addition.
	ScopeSession StepScope = iota

	// ScopeTurn stages rewrite the working messages for the rest of this turn
	// and no further. Compaction is the example. The summary replaces the
	// head for every later step, so the summarizer runs once rather than on
	// every step. The record still keeps every message.
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
	// StepsLeft is how many steps the turn may still count, this one
	// included. Step counts model calls, and a free refusal doesn't use up a
	// step (#281). So a note about the step budget reads StepsLeft, not
	// MaxSteps - Step.
	StepsLeft int
	// Usage counts what the turn has spent so far, before this step.
	Usage Usage
	// Turn is this turn's record so far, oldest first (#278). It starts after
	// the history the turn was given, so it holds this turn's assistant
	// replies, tool results, turn-end continuations and session-stage notes.
	// Compaction never shrinks it, unlike StepView.Messages. It is a copy,
	// so a stage can keep it.
	Turn []Message
	// Emit sends an event to the turn's emit callback, the one Run and
	// RunTurn were given, as EventStuck does. It is never nil. Call it only
	// from inside Run, since the turn's callback may not outlive the turn.
	Emit func(Event)
}

// StepView holds the parts of one model call's request that stages may
// change.
type StepView struct {
	// Messages is the conversation the model will read.
	Messages []Message
	// Tools is the tool set offered on this call. Tools are listed afresh
	// every step, so a tool set has no record or rest-of-turn to persist
	// into. So only a change by a ScopeRequest stage reaches the model.
	Tools []core.ToolDef

	// Provider answers this one call in place of RunnerConfig.Provider when
	// set (#185). The next step starts from the configured provider again,
	// so a stage that routes several steps sets it on each. A routed
	// provider is a choice about one call. So only a ScopeRequest stage may
	// set it, and a session or turn stage that sets it fails the turn. Each
	// switch starts the new provider with a cold prompt cache. So a routing
	// policy does better keeping one provider than alternating.
	//
	// The structured or guided finalize after the last step always uses
	// RunnerConfig.Provider.
	Provider Provider

	// ProviderName labels this call for EventStep and TurnResult.UsageByModel.
	// It can be a connection name, a model id, or whatever the router calls
	// it. It may be set without Provider, to name the configured provider.
	// Like Provider, only a ScopeRequest stage may set it.
	ProviderName string

	// Guided is the decoding constraint for this one call. It starts as the
	// turn's ResponseGuided, and nil when the turn has none. A ScopeRequest
	// stage may replace it or clear it, and the next step starts from the
	// turn's grammar again. As with Provider, a session or turn stage that
	// changes it fails the turn.
	//
	// A step with no tools sends it. A step that offers tools sends no
	// constraint, and if the model answers without calling one, the guided
	// finalize writes the answer under this step's Guided. A nil Guided skips
	// that finalize. The constraint is checked against the provider that will
	// send it: the routed one for a step with no tools, and
	// RunnerConfig.Provider for the finalize. A provider that can't send it
	// fails the turn with ErrUnsupportedParam before the call. A grammar set
	// here in a turn with a ResponseSchema fails the turn too, since an
	// answer takes one constraint.
	Guided *GuidedDecoding

	// Generation holds this one call's generation params (#278). It starts
	// as the turn's, RunnerConfig.Generation merged with
	// TurnRequest.Generation. A ScopeRequest stage may change it, and the
	// next step starts from the turn's again. As with Provider, a session or
	// turn stage that changes it fails the turn. The structured or guided
	// finalize after the last step always uses the turn's params.
	Generation GenerationParams
}

// StepStage is one entry in RunnerConfig.StepStages, the chain that runs
// before every model call. It is the "before each model call" hook point in
// docs/HOOKS.md. Before StepStage, that hook point had a separate
// RunnerConfig field per feature, in an order nobody had chosen.
type StepStage struct {
	// Name identifies the stage in errors. Use a dotted, contributor-scoped
	// form ("host.step_budget").
	Name string

	// Scope says how long Run's output lives. See StepScope.
	Scope StepScope

	// Run returns the view with this stage's contribution folded in, or the
	// view unchanged when it has nothing to add. An error ends the turn. An
	// error that wraps ErrTurnStopped ends it with a partial TurnResult, as a
	// turn-end Stop does, and any other error fails it.
	Run func(ctx context.Context, info StepInfo, view StepView) (StepView, error)
}

// compactionStage is RunnerConfig.Compactor as a ScopeTurn stage. Compaction
// used to run once, before the first step. Then a single turn could grow past
// the budget with nothing to stop it (#7). As a stage it runs before every
// step. A Compactor is a no-op under budget, so a short turn never calls the
// summarizer.
//
// After the first step it also enforces the floor. Some compactors can say
// whether messages fit, and SummarizingCompactor is one. If the messages
// still don't fit after compacting, the turn ends with ErrContextBudget. It
// does not compact a summary of a summary on every step. Step 1 is exempt and
// keeps the old behavior of sending what the user gave it. The user may send
// a large first message, and only growth after that belongs to the turn.
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
// often mid-way through writing a test (#109). The user then has nothing that
// says where the turn got to. The note is per request. So it never joins the
// record, and it only uses tokens on the steps where it appears.
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

// stepChain is the ordered stage list for one Runner. It holds the Compactor
// and Selector fields as built-in stages, then RunnerConfig.StepStages. It
// sorts them stably by scope, so the configured order holds within each
// scope.
//
// The built-ins go first in their scopes. Compaction runs first among turn
// stages, so a later turn stage works on messages that already fit. The
// selector runs first among request stages. That is its old position, before
// anything a caller adds.
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
// working messages for the rest of the turn, then the messages that session
// stages added to the record, then the view to send. The view starts with
// guided, the turn's grammar, and gen, the turn's generation params.
func runStepChain(ctx context.Context, chain []StepStage, info StepInfo, working []Message, tools []core.ToolDef, guided *GuidedDecoding, gen GenerationParams) (newWorking, recorded []Message, view StepView, err error) {
	view = StepView{Messages: working, Tools: tools, Guided: guided, Generation: gen}
	for _, st := range chain {
		in := view.Messages
		// A stage gets a copy it can append to without writing into the
		// caller's backing array.
		view.Messages = slices.Clip(view.Messages)
		out, err := st.Run(ctx, info, view)
		if err != nil {
			return nil, nil, StepView{}, fmt.Errorf("step stage %q: %w", st.Name, err)
		}
		if st.Scope != ScopeRequest && (out.Provider != nil || out.ProviderName != "") {
			return nil, nil, StepView{}, fmt.Errorf("step stage %q: only a request stage may route a call to another provider", st.Name)
		}
		if st.Scope != ScopeRequest && out.Guided != view.Guided {
			return nil, nil, StepView{}, fmt.Errorf("step stage %q: only a request stage may set a call's grammar", st.Name)
		}
		if st.Scope != ScopeRequest && out.Generation != view.Generation {
			return nil, nil, StepView{}, fmt.Errorf("step stage %q: only a request stage may set a call's generation params", st.Name)
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
