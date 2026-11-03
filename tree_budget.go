package agent

import (
	"context"
	"errors"
	"sync/atomic"
)

// ErrTreeBudget is returned (wrapped) by the Runner when a ctx-threaded
// TreeBudget runs out mid-turn. That means the turn hit the aggregate step or
// token cap across the whole sub-agent tree. AgentSource softens a child
// error, so in a sub-agent it surfaces as an IsError result and the parent
// turn continues. In the top-level Runner the turn fails, like ErrMaxSteps.
var ErrTreeBudget = errors.New("agent: tree budget exhausted")

// TreeBudget caps the TOTAL model steps, tokens, or both, summed over a turn's
// whole agent tree. The tree is the parent Runner plus every sub-agent,
// fan-out member, and handoff round beneath it. It is the aggregate cost
// guard. It complements the per-source depth cap, the call-count budget
// (WithAgentCallBudget), and each Runner's own MaxSteps. A zero field means
// that dimension is unbounded.
type TreeBudget struct {
	// MaxSteps caps total model calls across the tree. Zero means unbounded.
	MaxSteps int
	// MaxTokens caps total tokens across the tree, counting input plus
	// output as providers report them. Zero means unbounded. It is enforced
	// after the fact, since usage is known only after a model call. So a
	// turn can overshoot by at most one step's output.
	MaxTokens int
}

// zero reports whether the budget constrains nothing (both dimensions off).
func (b TreeBudget) zero() bool { return b.MaxSteps <= 0 && b.MaxTokens <= 0 }

type treeBudgetKey struct{}

// treeBudgetState is the live shared counter installed on ctx. ctx values
// propagate to child runs, so every Runner in a tree shares one instance.
// The step and token totals are therefore aggregate, not per-Runner.
type treeBudgetState struct {
	stepsLeft  atomic.Int64 // remaining steps; unused when maxSteps == 0
	maxSteps   int64        // 0 = unbounded
	tokensUsed atomic.Int64
	maxTokens  int64 // 0 = unbounded
}

// WithTreeBudget installs a shared aggregate budget on ctx. Every Runner that
// runs under ctx consults it, both the parent and its sub-agents. Call it once
// at the top of a turn, and child runs inherit the same live counter through
// ctx. A zero TreeBudget is a no-op and returns ctx unchanged. Installing
// again on a ctx that already has a budget replaces it. But the Runner only
// installs a budget when none is present, so the whole tree shares the
// top-level budget.
func WithTreeBudget(ctx context.Context, b TreeBudget) context.Context {
	if b.zero() {
		return ctx
	}
	st := &treeBudgetState{maxSteps: int64(b.MaxSteps), maxTokens: int64(b.MaxTokens)}
	st.stepsLeft.Store(int64(b.MaxSteps))
	return context.WithValue(ctx, treeBudgetKey{}, st)
}

func treeBudgetFrom(ctx context.Context) *treeBudgetState {
	st, _ := ctx.Value(treeBudgetKey{}).(*treeBudgetState)
	return st
}

// consumeStep charges one step. It returns false when the aggregate step budget
// is exhausted, or when prior steps already exceeded the token budget. The
// Runner calls it at the top of each step. A nil state means no budget, and
// it always passes.
func (st *treeBudgetState) consumeStep() bool {
	if st == nil {
		return true
	}
	if st.maxTokens > 0 && st.tokensUsed.Load() >= st.maxTokens {
		return false
	}
	if st.maxSteps > 0 && st.stepsLeft.Add(-1) < 0 {
		return false
	}
	return true
}

// addTokens folds a step's usage into the shared token total. Nil state is a
// no-op.
func (st *treeBudgetState) addTokens(n int) {
	if st != nil && st.maxTokens > 0 && n > 0 {
		st.tokensUsed.Add(int64(n))
	}
}

// usage snapshots consumption against the caps for RunScope. A nil state means
// no budget is installed. It reports all zeros, and RunScope documents zeros
// as unbounded.
//
// Steps are stored as a remaining count and reported as a used one, so the two
// dimensions read the same way. Each is tracked only when its cap is set.
// consumeStep and addTokens skip an uncapped dimension, because there is
// nothing to enforce. So an uncapped dimension reports 0 used.
func (st *treeBudgetState) usage() TreeUsage {
	if st == nil {
		return TreeUsage{}
	}
	u := TreeUsage{MaxSteps: int(st.maxSteps), MaxTokens: int(st.maxTokens)}
	if st.maxSteps > 0 {
		u.StepsUsed = int(st.maxSteps - st.stepsLeft.Load())
	}
	if st.maxTokens > 0 {
		u.TokensUsed = int(st.tokensUsed.Load())
	}
	return u
}
