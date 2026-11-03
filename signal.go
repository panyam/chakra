package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// SignalKind classifies an upward signal from a child agent to its parent
// Runner. On the control axis, signals complement the downward Control (issue
// 936).
type SignalKind string

const (
	// SignalEscalate asks the parent to stop and handle the child's finding
	// ("decisive result" / "an error you must deal with"). With the
	// AbortOnEscalate policy the parent turn ends. Otherwise the signal is
	// injected and the parent model decides.
	SignalEscalate SignalKind = "escalate"

	// SignalCustom is an application-defined signal for a SignalPolicy or the
	// parent model to interpret. Name names it, and Data is an optional payload.
	// It is FYI-only. It is collected and injected into the parent's next step,
	// but it does NOT break an interruptible dispatch's join barrier (see
	// interrupts).
	SignalCustom SignalKind = "custom"

	// SignalPreempt is a child's *advisory* claim that its result may make the
	// parallel work unnecessary. It is deliberately NOT authoritative. Under A7
	// isolation, a child cannot know the parent's global goal or what its
	// siblings are doing. So "the parallel work is moot" is the child's
	// assertion, never ground truth. A preempt breaks the interruptible join
	// barrier ONLY when the parent grants it via RunnerConfig.PreemptGrant.
	// Breaking the barrier cancels the other in-flight calls. With no grant,
	// which is the default, a preempt is only collected and injected like any
	// signal, and the parent model decides whether to re-plan. So a rogue or
	// prompt-injected child cannot kill its siblings on its own authority. The
	// signal is non-referential either way. The child names no sibling. Only
	// the parent holds the fan-out inventory, and the parent decides.
	SignalPreempt SignalKind = "preempt"
)

// interrupts reports whether a signal of this kind is a *candidate* to break an
// interruptible dispatch's join barrier (issue 1167, piece C). SignalEscalate
// ("stop and handle my finding") and SignalPreempt qualify. SignalCustom is
// FYI-only and never breaks. For a preempt, being a candidate is necessary but
// not sufficient. The parent decides whether a preempt actually breaks,
// through RunnerConfig.PreemptGrant (see Runner.shouldBreakOn). Escalate
// breaks unconditionally, because the parent must handle it. In a
// non-interruptible turn this has no effect, since every signal is read only
// at the join.
func (k SignalKind) interrupts() bool {
	return k == SignalPreempt || k == SignalEscalate
}

// Signal is an upward message from a child agent to its parent Runner. It is
// wire-serializable (constraint A2), so a remote child raises it the same way
// as a co-located one. The parent installs a ctx-threaded sink around the
// dispatch that spawned the child. RaiseSignal writes the signal into that
// sink, and the parent reads the drained signals at the join.
type Signal struct {
	// Kind classifies the signal; a SignalPolicy switches on it.
	Kind SignalKind `json:"kind"`
	// Name names a SignalCustom signal (ignored for the built-in kinds).
	Name string `json:"name,omitempty"`
	// Note is a short reason, surfaced when the signal is injected into the
	// parent's next step.
	Note string `json:"note,omitempty"`
	// Data is an optional JSON payload for a SignalCustom signal (A5: RawJSON).
	Data core.RawJSON `json:"data,omitempty"`
	// Source is the raising child's scope path (agentScope), stamped by
	// RaiseSignal so a policy or the model knows who signalled. Callers do not
	// set it.
	Source string `json:"source,omitempty"`
}

// SignalAction is a SignalPolicy's verdict over the signals drained at one
// dispatch join. The zero value continues the turn (inject-and-proceed).
type SignalAction struct {
	// AbortTurn ends the parent turn immediately with ErrSignalAbort. Reason is
	// folded into the error. Use it for an escalation that should stop the parent.
	AbortTurn bool
	// Reason is the abort reason (and the turn error message). Ignored unless
	// AbortTurn is set.
	Reason string
}

// SignalPolicy decides how a parent Runner reacts to its children's signals
// from one dispatch. It reads them at the join. It runs after the fan-out has
// joined (issue 1165 is non-interruptible), so it chooses only whether to
// abort the parent turn. The signals are injected into the next step
// regardless. Nil means inject-and-continue. A policy must be a pure function
// of the drained signals.
type SignalPolicy func(signals []Signal) SignalAction

// AbortOnEscalate is a built-in SignalPolicy that aborts the parent turn as
// soon as a child raises SignalEscalate, folding that child's note into the
// error. Other signal kinds inject-and-continue. It is the deterministic
// counterpart to letting the parent model decide via injection.
func AbortOnEscalate(signals []Signal) SignalAction {
	for _, s := range signals {
		if s.Kind == SignalEscalate {
			reason := s.Note
			if reason == "" {
				reason = fmt.Sprintf("escalated by %s", s.Source)
			}
			return SignalAction{AbortTurn: true, Reason: reason}
		}
	}
	return SignalAction{}
}

// ErrSignalAbort is returned (wrapped) when a SignalPolicy aborts a turn via
// SignalAction.AbortTurn. A child escalated, and the parent chose to stop.
// Check with errors.Is. The wrapped message includes the policy's Reason.
var ErrSignalAbort = errors.New("agent: turn aborted by child signal")

// GrantAllPreempts is a built-in RunnerConfig.PreemptGrant that honors every
// child's preempt. It suits a parent that trusts all its sub-agents equally.
// For a finer policy, pass your own predicate instead. It could honor a
// preempt only from a given Source, or only with a given Note. Nil
// PreemptGrant (the default) honors none.
func GrantAllPreempts(Signal) bool { return true }

// signalSink collects the signals from the sub-agents that one dispatch
// spawned. Two ctx keys keep the direction right, and this is the subtle part.
// A dispatch installs its sink under dispatchSinkKey, and the sink collects
// the dispatch's OWN children's signals. When an AgentSource spawns a child,
// it snapshots that sink under parentSinkKey in the child's ctx. The child's
// signal_parent writes there, because RaiseSignal reads parentSinkKey. So a
// child raises to its SPAWNER's sink, not to its own dispatch sink. With one
// key, the child's own dispatch would shadow the spawner's sink. A grandchild
// raises to the child, never skipping a level.
type signalSink struct {
	mu      sync.Mutex
	signals []Signal

	// notify, when non-nil, is closed exactly once on the first raise that
	// breakOn admits. An interruptible dispatch (issue 1167) uses it to break
	// its join barrier the moment a child raises a barrier-breaking signal. It
	// is nil in the default non-interruptible path, where signals are only read
	// at the join.
	notify chan struct{}
	once   sync.Once

	// breakOn decides whether a given raised signal breaks the barrier. An
	// interruptible dispatch sets it to Runner.shouldBreakOn, which breaks on
	// escalate always, on preempt only when granted, and on custom never. It
	// is nil otherwise. The decision lives here rather than in raise, so the
	// parent Runner gates a preemption, not the child's kind alone.
	breakOn func(Signal) bool
}

func (s *signalSink) raise(sig Signal) {
	s.mu.Lock()
	s.signals = append(s.signals, sig)
	n := s.notify
	b := s.breakOn
	s.mu.Unlock()
	if n != nil && b != nil && b(sig) {
		s.once.Do(func() { close(n) })
	}
}

// drain returns the collected signals and clears the sink.
func (s *signalSink) drain() []Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.signals
	s.signals = nil
	return out
}

type dispatchSinkKey struct{}
type parentSinkKey struct{}

// withDispatchSink stamps a dispatch's sink on ctx. The dispatch collects its
// children's signals into that sink. AgentSource reads it via dispatchSinkFrom
// and hands it to the spawned child as the child's parent sink.
func withDispatchSink(ctx context.Context, s *signalSink) context.Context {
	return context.WithValue(ctx, dispatchSinkKey{}, s)
}

func dispatchSinkFrom(ctx context.Context) *signalSink {
	s, _ := ctx.Value(dispatchSinkKey{}).(*signalSink)
	return s
}

// withParentSink stamps a running child's parent sink on ctx. The child raises
// signals TO that sink. The spawning AgentSource snapshots it from the
// spawner's dispatch sink, so the child's own dispatch cannot shadow it.
func withParentSink(ctx context.Context, s *signalSink) context.Context {
	return context.WithValue(ctx, parentSinkKey{}, s)
}

func parentSinkFrom(ctx context.Context) *signalSink {
	s, _ := ctx.Value(parentSinkKey{}).(*signalSink)
	return s
}

// RaiseSignal delivers sig to the parent Runner's signal sink. It is the
// control-axis "up" primitive, and a child's control tool calls it. It stamps
// sig.Source with the child's scope when unset. It returns false when ctx has
// no parent sink, as for a top-level agent with no parent to signal. A control
// tool can then tell the model there is nothing to signal instead of silently
// dropping it.
func RaiseSignal(ctx context.Context, sig Signal) bool {
	sink := parentSinkFrom(ctx)
	if sink == nil {
		return false
	}
	if sig.Source == "" {
		sig.Source = agentScope(ctx).String()
	}
	sink.raise(sig)
	return true
}

// SignalParentToolName is the name of the control tool that raises a Signal to
// the parent. A child agent calls it. The name is fixed, like the
// memory/meta-tool names.
const SignalParentToolName = "signal_parent"

type signalParentArgs struct {
	// Kind is "escalate", "preempt", or "custom".
	Kind string `json:"kind"`
	// Note is a short reason surfaced to the parent.
	Note string `json:"note,omitempty"`
	// Name names a custom signal (kind="custom").
	Name string `json:"name,omitempty"`
}

// NewSignalSource returns a leaf ToolSource exposing the signal_parent control
// tool so a child agent can raise an upward Signal (issue 1165). Add it to a
// sub-agent's tool set. A top-level agent has no parent, so it gets a graceful
// "no parent to signal" result. It is model-facing, so under A6 it lives here
// rather than in mcpkit's client/.
func NewSignalSource() *FuncSource {
	fs := NewFuncSource()
	_ = AddFunc(fs, SignalParentToolName,
		"Raise a signal to the parent agent that spawned you. kind: 'escalate' (ask the parent to stop and handle your finding), 'preempt' (suggest that your result may make the parallel work unnecessary — the parent decides whether to stop the other sub-agents; you cannot stop them yourself), or 'custom' (a named, FYI signal carrying your own findings). Report only your OWN state — you have no knowledge of other sub-agents; the parent decides what to do. Use sparingly, only for a decisive result the parent must act on.",
		func(ctx context.Context, in signalParentArgs) (string, error) {
			kind := SignalKind(in.Kind)
			switch kind {
			case SignalEscalate, SignalPreempt, SignalCustom:
			default:
				return "", fmt.Errorf("unknown signal kind %q (want escalate, preempt, or custom)", in.Kind)
			}
			if RaiseSignal(ctx, Signal{Kind: kind, Name: in.Name, Note: in.Note}) {
				return fmt.Sprintf("signalled parent: %s", kind), nil
			}
			return "no parent to signal (you are the top-level agent)", nil
		})
	return fs
}

// renderSignals formats the drained signals into the RoleSystem note injected
// into the parent's next step, so the parent model sees what its children
// raised. One line per signal.
func renderSignals(signals []Signal) string {
	var b strings.Builder
	b.WriteString("Sub-agent signals received:")
	for _, s := range signals {
		b.WriteString("\n- ")
		b.WriteString(string(s.Kind))
		if s.Name != "" {
			b.WriteString(" (")
			b.WriteString(s.Name)
			b.WriteString(")")
		}
		if s.Source != "" {
			b.WriteString(" from ")
			b.WriteString(s.Source)
		}
		if s.Note != "" {
			b.WriteString(": ")
			b.WriteString(s.Note)
		}
	}
	return b.String()
}
