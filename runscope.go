package agent

import (
	"context"
	"strings"
)

// AgentHop is one link in a run's agent ancestry. It names the sub-agent
// invoked at that level.
//
// It is a struct rather than a bare name so a hop can grow without breaking
// every caller. A child's location is not guaranteed. The in-process
// AgentSource is the degenerate co-located case. Constraint A7 states the
// general case, a child on another host, provider, or model. When that case
// arrives, a hop gains a field for where it ran. Adding a field here is a
// minor change. Widening []string to []AgentHop later would not have been.
type AgentHop struct {
	// Name is the sub-agent's registered name, as given to AgentSource,
	// FanOutSource, Team, or AgentPool.
	Name string `json:"name"`
}

// AgentPath is a run's agent ancestry, outermost first. It records who invoked
// whom to reach the code reading it. Empty at the top level.
//
// Read it as a stack trace rather than a graph path. Each entry is a frame,
// so a cycle needs no special handling. An agent that reaches itself
// appears twice, exactly as recursion shows repeated frames, and the per-source
// depth cap (DefaultMaxAgentDepth) bounds how far that can go.
type AgentPath []AgentHop

// Depth reports how deep in the agent tree this path is: 0 at the top level.
func (p AgentPath) Depth() int { return len(p) }

// Contains reports whether name appears anywhere in the ancestry. It answers
// "am I running under X" exactly.
//
// Callers should use it instead of matching on String. Substring matching
// against a joined path silently answers yes for "research" when the ancestor
// is "researcher". Splitting a joined path is ambiguous, because a name may
// itself contain the separator.
func (p AgentPath) Contains(name string) bool {
	for _, h := range p {
		if h.Name == name {
			return true
		}
	}
	return false
}

// Child returns the path extended by one hop, without mutating the receiver.
// Each source calls it when it invokes a sub-agent.
func (p AgentPath) Child(name string) AgentPath {
	out := make(AgentPath, len(p), len(p)+1)
	copy(out, p)
	return append(out, AgentHop{Name: name})
}

// String renders the ancestry slash-joined ("researcher/summarizer") for
// display, tracing, and Signal.Source. Lossy when a name contains a slash, so
// use it to show a path and Contains to test one.
func (p AgentPath) String() string {
	if len(p) == 0 {
		return ""
	}
	names := make([]string, len(p))
	for i, h := range p {
		names[i] = h.Name
	}
	return strings.Join(names, "/")
}

// TreeUsage reports how much of a turn's aggregate TreeBudget has been
// consumed. An extension can then back off before exhaustion instead of
// running into ErrTreeBudget, which aborts.
//
// A zero Max means that dimension is unbounded. An entirely unbudgeted turn
// reports zero for every Max. Usage is only tracked for a dimension that has a
// cap, so StepsUsed and TokensUsed read 0 when their Max does. The counter
// exists to enforce a limit, and without a cap there is none to enforce.
//
// Both counts are post-hoc, like TreeBudget.MaxTokens. Usage is known only
// after a model call, so a reading can be one step stale.
type TreeUsage struct {
	StepsUsed  int `json:"stepsUsed"`
	MaxSteps   int `json:"maxSteps"`
	TokensUsed int `json:"tokensUsed"`
	MaxTokens  int `json:"maxTokens"`
}

// RunScope holds what a call knows about its run beyond its own arguments. It
// says where the call sits in the agent tree and what the tree has spent.
//
// Before RunScope, a middleware received only its call. A checkpoint extension
// could not tell it was running inside a sub-agent, so it snapshotted on every
// nested call. A budget-aware extension could not read how much tree budget
// remained.
//
// Read-only by construction. The Runner owns every field's value. Only the
// Runner and the sub-agent sources set depth or budget.
//
// It holds values only and is wire-serializable, like Event under constraint
// A2. A scope may cross a parent/child boundary that a pointer cannot cross,
// and A7 forbids handing a child a handle to parent state.
//
// The signal sinks are deliberately absent. A sink is a control mechanism, not
// a fact about the run. Exposing one would let an extension raise a signal as
// though it came from a child. The non-referential signal design exists to
// prevent that forgery. The pending Team handoff is left out for the same
// reason. It is in-flight control, not scope.
type RunScope struct {
	// Path is the agent ancestry, empty at the top level.
	Path AgentPath `json:"path,omitempty"`

	// CallBudget is the sub-agent invocations remaining under
	// WithAgentCallBudget, shared across the whole tree. -1 when unbounded.
	CallBudget int `json:"callBudget"`

	// Tree is the aggregate TreeBudget consumption for this turn.
	Tree TreeUsage `json:"tree"`
}

// Depth reports how deep in the agent tree the call is, 0 at the top level.
// Shorthand for Path.Depth(), which is the check most callers want.
func (s RunScope) Depth() int { return s.Path.Depth() }

// ScopeFrom reads the run scope from a context. A context that never crossed a
// Runner returns the zero scope with CallBudget -1, so a caller outside a run
// sees "top level, nothing bounded" rather than a false limit.
//
// Middleware does not need this, because ToolCallInfo.Scope is already
// populated. A tool implementation receives only a context, so it uses this.
func ScopeFrom(ctx context.Context) RunScope {
	s := RunScope{Path: agentScope(ctx), CallBudget: -1, Tree: treeBudgetFrom(ctx).usage()}
	if b := agentCallBudget(ctx); b != nil {
		s.CallBudget = int(b.Load())
	}
	return s
}
