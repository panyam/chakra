package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// repeatRig is a Runner with a read-only "look" and a writing "bump" tool,
// each counting its runs, behind a repeat cache.
type repeatRig struct {
	runner       *Runner
	stub         *StubProvider
	looks, bumps atomic.Int32
}

func newRepeatRig(t *testing.T, cfg RepeatCacheConfig, maxSteps int, turns ...StubTurn) *repeatRig {
	t.Helper()
	g := &repeatRig{stub: NewStubProvider(turns...)}
	src := NewFuncSource()
	type in struct {
		X string `json:"x"`
	}
	if err := AddFunc(src, "look", "looks", func(_ context.Context, i in) (string, error) {
		n := g.looks.Add(1)
		if i.X == "fail" {
			return "", fmt.Errorf("failed %d", n)
		}
		return fmt.Sprintf("look %s #%d", i.X, n), nil
	}, ReadOnly()); err != nil {
		t.Fatal(err)
	}
	if err := AddFunc(src, "bump", "bumps", func(_ context.Context, i in) (string, error) {
		return fmt.Sprintf("bump #%d", g.bumps.Add(1)), nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(RunnerConfig{Provider: g.stub, Tools: src, MaxSteps: maxSteps,
		ToolMiddleware: []ToolMiddleware{NewRepeatCache(cfg)}})
	if err != nil {
		t.Fatal(err)
	}
	g.runner = r
	return g
}

func (g *repeatRig) turn(t *testing.T) (*TurnResult, []Event) {
	t.Helper()
	var events []Event
	res, err := g.runner.Run(context.Background(), []Message{{Role: RoleUser, Text: "q"}}, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	return res, events
}

func lookCall(x string) StubTurn {
	return StubTurn{ToolCalls: []ToolCall{toolCall("c", "look", `{"x":"`+x+`"}`)}}
}

// A repeat of an idempotent call is answered from the first result, isn't
// run, and costs no step.
func TestARepeatedIdempotentCallIsAnsweredFree(t *testing.T) {
	g := newRepeatRig(t, RepeatCacheConfig{}, 2, lookCall("a"), lookCall("a"), StubTurn{Text: "done"})
	res, events := g.turn(t)
	if g.looks.Load() != 1 {
		t.Errorf("look ran %d times", g.looks.Load())
	}
	if res.Steps != 2 || res.FreeRefusals != 1 || len(kindsOf(events, EventRefusalFree)) != 1 {
		t.Errorf("steps %d, free %d", res.Steps, res.FreeRefusals)
	}
	if got := lastToolText(t, g.stub, 2); got != "look a #1\n\n"+defaultRepeatNote {
		t.Errorf("model read %q", got)
	}
}

// Arguments in another key order are the same call. Other arguments aren't.
func TestRepeatCacheKeysOnCanonicalArgs(t *testing.T) {
	two := StubTurn{ToolCalls: []ToolCall{toolCall("c", "look", `{"x":"a","y":1}`)}}
	swapped := StubTurn{ToolCalls: []ToolCall{toolCall("c", "look", `{"y":1,"x":"a"}`)}}
	g := newRepeatRig(t, RepeatCacheConfig{}, 5, two, swapped, lookCall("b"), StubTurn{Text: "done"})
	g.turn(t)
	if g.looks.Load() != 2 {
		t.Errorf("look ran %d times, want 2", g.looks.Load())
	}
}

// A tool that isn't idempotent always runs.
func TestANonIdempotentCallAlwaysRuns(t *testing.T) {
	bump := StubTurn{ToolCalls: []ToolCall{toolCall("c", "bump", `{}`)}}
	g := newRepeatRig(t, RepeatCacheConfig{}, 5, bump, bump, StubTurn{Text: "done"})
	res, _ := g.turn(t)
	if g.bumps.Load() != 2 || res.FreeRefusals != 0 {
		t.Errorf("bump ran %d times, free %d", g.bumps.Load(), res.FreeRefusals)
	}
}

// The next turn starts empty, so the tool runs again.
func TestRepeatCacheForgetsAtTheTurnEnd(t *testing.T) {
	g := newRepeatRig(t, RepeatCacheConfig{}, 5, lookCall("a"), StubTurn{Text: "one"}, lookCall("a"), StubTurn{Text: "two"})
	g.turn(t)
	g.turn(t)
	if g.looks.Load() != 2 {
		t.Errorf("look ran %d times across two turns, want 2", g.looks.Load())
	}
}

// A call that failed runs again.
func TestRepeatCacheDoesNotKeepFailures(t *testing.T) {
	g := newRepeatRig(t, RepeatCacheConfig{}, 5, lookCall("fail"), lookCall("fail"), StubTurn{Text: "done"})
	g.turn(t)
	if g.looks.Load() != 2 {
		t.Errorf("look ran %d times, want 2", g.looks.Load())
	}
}

// Past Max a repeat runs and counts.
func TestRepeatCachePastMaxRuns(t *testing.T) {
	g := newRepeatRig(t, RepeatCacheConfig{Max: 1}, 5, lookCall("a"), lookCall("a"), lookCall("a"), StubTurn{Text: "done"})
	res, _ := g.turn(t)
	if g.looks.Load() != 2 || res.FreeRefusals != 1 || res.Steps != 3 {
		t.Errorf("look ran %d, free %d, steps %d; want 2, 1, 3", g.looks.Load(), res.FreeRefusals, res.Steps)
	}
}

// A host's key and note decide what counts as the same call and what the
// model reads.
func TestRepeatCacheHostKeyAndNote(t *testing.T) {
	key := func(info ToolCallInfo) (string, bool) {
		return info.Call.Name, !strings.Contains(string(info.Call.Args.Raw()), "skip")
	}
	g := newRepeatRig(t, RepeatCacheConfig{Key: key, Note: "same as before"}, 5,
		lookCall("a"), lookCall("b"), lookCall("skip"), StubTurn{Text: "done"})
	g.turn(t)
	if g.looks.Load() != 2 {
		t.Errorf("look ran %d times, want 2", g.looks.Load())
	}
	if got := lastToolText(t, g.stub, 2); got != "look a #1\n\nsame as before" {
		t.Errorf("model read %q", got)
	}
}
