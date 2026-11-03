package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// twoChildParent builds a parent Runner that calls sub-agents A and B in one
// turn, then answers "re-planned". A is the signalling child. The caller
// supplies B, either blocking or normal. interruptible toggles the gate.
func twoChildParent(t *testing.T, a, b *AgentSource, interruptible bool) (*Runner, *StubProvider) {
	t.Helper()
	return twoChildParentCfg(t, a, b, RunnerConfig{Interruptible: interruptible})
}

// twoChildParentGranting builds an interruptible parent with a PreemptGrant
// that honors preempts per grant. It is the "parent authorises the
// preemption" fixture.
func twoChildParentGranting(t *testing.T, a, b *AgentSource, grant func(Signal) bool) (*Runner, *StubProvider) {
	t.Helper()
	return twoChildParentCfg(t, a, b, RunnerConfig{Interruptible: true, PreemptGrant: grant})
}

func twoChildParentCfg(t *testing.T, a, b *AgentSource, cfg RunnerConfig) (*Runner, *StubProvider) {
	t.Helper()
	tools := NewMultiSource()
	if err := tools.Add("a-src", a); err != nil {
		t.Fatal(err)
	}
	if err := tools.Add("b-src", b); err != nil {
		t.Fatal(err)
	}
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{
			{ID: "ca", Name: a.cfg.Name, Args: core.NewRawJSON(json.RawMessage(`{"task":"go"}`))},
			{ID: "cb", Name: b.cfg.Name, Args: core.NewRawJSON(json.RawMessage(`{"task":"go"}`))},
		}},
		StubTurn{Text: "re-planned"},
	)
	cfg.Provider = stub
	cfg.Tools = tools
	r, err := NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r, stub
}

func blockingChild(t *testing.T, name string) *AgentSource {
	t.Helper()
	r, err := NewRunner(RunnerConfig{Provider: blockingProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	as, err := NewAgentSource(AgentSourceConfig{Name: name, Description: "blocks", Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func toolTexts(result *TurnResult) []string {
	var out []string
	for _, m := range result.Messages {
		if m.Role == RoleTool {
			out = append(out, m.Text)
		}
	}
	return out
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestInterruptibleTurn_SignalBreaksBarrier has child A signal mid-fan-out
// while child B blocks forever. In an interruptible turn, A's signal cancels
// B, the dispatch returns partial results, and the parent re-plans. So the
// turn completes instead of hanging on B.
func TestInterruptibleTurn_SignalBreaksBarrier(t *testing.T) {
	a := signalingChild(t, "A", "escalate", "found it")
	b := blockingChild(t, "B")
	parent, _ := twoChildParent(t, a, b, true)

	var mu sync.Mutex
	var sawSignal bool
	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			mu.Lock()
			sawSignal = true
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "re-planned" {
		t.Fatalf("parent final = %q, want it to re-plan after the interrupt", result.Text)
	}
	if !sawSignal {
		t.Fatal("no EventSignal; the interrupt was not signal-driven")
	}
	// B blocks forever, so its only possible terminal result is a cancellation.
	if !containsSubstr(toolTexts(result), "cancelled by user") {
		t.Fatalf("no cancelled sibling among %v; B was not interrupted", toolTexts(result))
	}
}

// TestInterruptibleTurn_NoSignalWaitsForAll turns the gate on and sends no
// signal. The dispatch still waits for every call, as the default barrier does.
func TestInterruptibleTurn_NoSignalWaitsForAll(t *testing.T) {
	a, _ := NewAgentSource(AgentSourceConfig{Name: "A", Description: "d", Runner: childRunner(t, StubTurn{Text: "A done"})})
	b, _ := NewAgentSource(AgentSourceConfig{Name: "B", Description: "d", Runner: childRunner(t, StubTurn{Text: "B done"})})
	parent, _ := twoChildParent(t, a, b, true)

	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	texts := toolTexts(result)
	if !containsSubstr(texts, "A done") || !containsSubstr(texts, "B done") {
		t.Fatalf("tool results = %v, want both children to complete", texts)
	}
	if containsSubstr(texts, "cancelled") {
		t.Fatalf("a call was cancelled with no signal: %v", texts)
	}
}

// TestInterruptibleOff_IgnoresSignalForBarrier turns the gate OFF. A child's
// signal then doesn't break the barrier, and every call still completes. The
// signal is only recorded and injected. B is a normal child. A blocking child
// would hang, because the barrier waits for it, and the gate is what opts out
// of that wait.
func TestInterruptibleOff_IgnoresSignalForBarrier(t *testing.T) {
	a := signalingChild(t, "A", "escalate", "found it")
	b, _ := NewAgentSource(AgentSourceConfig{Name: "B", Description: "d", Runner: childRunner(t, StubTurn{Text: "B done"})})
	parent, _ := twoChildParent(t, a, b, false)

	var sawSignal bool
	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			sawSignal = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(toolTexts(result), "B done") {
		t.Fatalf("B did not complete with the gate off: %v", toolTexts(result))
	}
	if containsSubstr(toolTexts(result), "cancelled") {
		t.Fatalf("a call was cancelled with the gate off: %v", toolTexts(result))
	}
	if !sawSignal {
		t.Fatal("signal should still be recorded/injected even with the gate off")
	}
}

// TestInterruptibleTurn_PreemptGrantedBreaksBarrier has the parent grant
// preemption, with PreemptGrant returning true. A child's preempt then breaks
// the barrier like escalate. The blocking child B is cancelled, and the parent
// re-plans.
func TestInterruptibleTurn_PreemptGrantedBreaksBarrier(t *testing.T) {
	a := signalingChild(t, "A", "preempt", "enough")
	b := blockingChild(t, "B")
	parent, _ := twoChildParentGranting(t, a, b, func(Signal) bool { return true })

	var mu sync.Mutex
	var sawSignal bool
	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			mu.Lock()
			sawSignal = true
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "re-planned" {
		t.Fatalf("parent final = %q, want it to re-plan after the granted preempt", result.Text)
	}
	if !sawSignal {
		t.Fatal("no EventSignal; the interrupt was not signal-driven")
	}
	// B blocks forever, so its only possible terminal result is a cancellation.
	if !containsSubstr(toolTexts(result), "cancelled by user") {
		t.Fatalf("no cancelled sibling among %v; B was not preempted", toolTexts(result))
	}
}

// TestInterruptibleTurn_PreemptNotGrantedDoesNotBreak sets no PreemptGrant,
// which is the default. A child's preempt is then advisory only. It must NOT
// break the barrier or cancel siblings. B runs to completion, and the preempt
// is still injected. This is the safe-by-default guard, so a child can't kill
// its siblings on its own.
func TestInterruptibleTurn_PreemptNotGrantedDoesNotBreak(t *testing.T) {
	release := make(chan struct{})
	a := signalingChild(t, "A", "preempt", "enough")
	b := releasableChild(t, "B", release)
	parent, _ := twoChildParent(t, a, b, true) // interruptible, but no grant

	var mu sync.Mutex
	var sawSignal bool
	var once sync.Once
	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			mu.Lock()
			sawSignal = true
			mu.Unlock()
		}
		if e.Kind == EventToolEnd && e.ToolCall != nil && e.ToolCall.ID == "ca" {
			once.Do(func() { close(release) })
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	texts := toolTexts(result)
	if containsSubstr(texts, "cancelled by user") {
		t.Fatalf("B was cancelled by an ungranted preempt: %v", texts)
	}
	if !containsSubstr(texts, "B done") {
		t.Fatalf("B did not complete: %v", texts)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawSignal {
		t.Fatal("preempt should still be recorded/injected even without a grant")
	}
}

// TestInterruptibleTurn_CustomDoesNotBreakBarrier sends a custom (FYI) signal.
// The signal is collected and injected, but it must NOT break the
// interruptible barrier. The other in-flight sibling runs to completion and is
// never cancelled. B is released deterministically once A's call has ended,
// at A's parent-level tool-end. So B can show "cancelled by user" only after a
// wrongful barrier break. Custom used to cancel B, so this test fails before
// kind-aware breaking and passes after.
func TestInterruptibleTurn_CustomDoesNotBreakBarrier(t *testing.T) {
	release := make(chan struct{})
	a := signalingChild(t, "A", "custom", "fyi")
	b := releasableChild(t, "B", release)
	parent, _ := twoChildParent(t, a, b, true)

	var mu sync.Mutex
	var sawSignal bool
	var once sync.Once
	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			mu.Lock()
			sawSignal = true
			mu.Unlock()
		}
		// A's call ("ca") has ended, so under the fix dispatch is now waiting on
		// B. Release B so the turn finishes. If custom had wrongly broken the
		// barrier, B was already cancelled before this fired.
		if e.Kind == EventToolEnd && e.ToolCall != nil && e.ToolCall.ID == "ca" {
			once.Do(func() { close(release) })
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	texts := toolTexts(result)
	if containsSubstr(texts, "cancelled by user") {
		t.Fatalf("B was cancelled by a custom (FYI) signal: %v", texts)
	}
	if !containsSubstr(texts, "B done") {
		t.Fatalf("B did not complete: %v", texts)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawSignal {
		t.Fatal("custom signal should still be recorded/injected")
	}
}

// releasableChild is a sub-agent with a single turn that blocks. When release
// is closed, it answers "<name> done". When its ctx is cancelled, it errors.
// The test controls whether it finishes or is cancelled, so a barrier break is
// observable without a timed sleep.
func releasableChild(t *testing.T, name string, release <-chan struct{}) *AgentSource {
	t.Helper()
	r, err := NewRunner(RunnerConfig{Provider: releasableProvider{release: release, text: name + " done"}})
	if err != nil {
		t.Fatal(err)
	}
	as, err := NewAgentSource(AgentSourceConfig{Name: name, Description: "d", Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	return as
}

type releasableProvider struct {
	release <-chan struct{}
	text    string
}

func (p releasableProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	return &releasableStream{ctx: ctx, release: p.release, deltas: StubTurn{Text: p.text}.deltas()}, nil
}

func (p releasableProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	return nil, errors.New("unused")
}

type releasableStream struct {
	ctx     context.Context
	release <-chan struct{}
	deltas  []Delta
	i       int
	gated   bool
}

func (s *releasableStream) Recv() (Delta, error) {
	if !s.gated {
		select {
		case <-s.release:
		case <-s.ctx.Done():
			return Delta{}, s.ctx.Err()
		}
		s.gated = true
	}
	if s.i >= len(s.deltas) {
		return Delta{}, io.EOF
	}
	d := s.deltas[s.i]
	s.i++
	return d, nil
}

func (s *releasableStream) Close() error { return nil }
