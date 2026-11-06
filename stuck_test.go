package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func call(id, name, args string) ToolCall {
	return ToolCall{ID: id, Name: name, Args: core.NewRawJSON(json.RawMessage(args))}
}

// stuckRun plays turns through a Runner with the detector on, over one tool
// "run" that answers from results by its "x" argument.
func stuckRun(t *testing.T, cfg StuckConfig, results map[string]string, turns ...StubTurn) (*StubProvider, *TurnResult, []Event, error) {
	t.Helper()
	src := NewFuncSource()
	AddFunc(src, "run", "runs", func(ctx context.Context, in struct {
		X string `json:"x"`
	}) (string, error) {
		if out, ok := results[in.X]; ok {
			if strings.HasPrefix(out, "ERR ") {
				return "", errors.New(strings.TrimPrefix(out, "ERR "))
			}
			return out, nil
		}
		return "ok " + in.X, nil
	})
	AddFunc(src, "other", "other", func(ctx context.Context, in struct{}) (string, error) { return "", nil })
	stub := NewStubProvider(turns...)
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: src, MaxSteps: 12, StepStages: []StepStage{NewStuckDetector(cfg)}})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "fix the test"}}, func(e Event) { events = append(events, e) })
	return stub, res, events, err
}

func runCall(n int, x string) StubTurn {
	return StubTurn{ToolCalls: []ToolCall{call(fmt.Sprint("c", n), "run", `{"x":"`+x+`"}`)}}
}

func stuckEvents(events []Event) []StuckFinding {
	var out []StuckFinding
	for _, e := range events {
		if e.Kind == EventStuck {
			out = append(out, *e.Stuck)
		}
	}
	return out
}

func lastSystem(req ProviderRequest) string {
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == RoleSystem {
		return req.Messages[n-1].Text
	}
	return ""
}

// Imagine a coding agent that sends the same edit three times and gets the
// same failing test each time. The fourth call carries a note naming the
// tool, and a stuck event reaches the surfaces. The note is for that call
// only, so the record doesn't keep it.
func TestStuckRepeatAddsANote(t *testing.T) {
	stub, res, events, err := stuckRun(t, StuckConfig{}, map[string]string{"a": "FAIL"},
		runCall(1, "a"), runCall(2, "a"), runCall(3, "a"), StubTurn{Text: "giving up"})
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	for i := range 3 {
		if note := lastSystem(reqs[i]); note != "" {
			t.Errorf("call %d already has a note: %q", i+1, note)
		}
	}
	if note := lastSystem(reqs[3]); !strings.Contains(note, "run") || !strings.Contains(note, "3 times") {
		t.Errorf("call 4 note = %q, want it to name run and the count", note)
	}
	got := stuckEvents(events)
	if len(got) != 1 || got[0].Pattern != StuckRepeat || got[0].Count != 3 || !slices.Equal(got[0].Tools, []string{"run"}) || !slices.Equal(got[0].Steps, []int{1, 2, 3}) {
		t.Fatalf("stuck events = %+v", got)
	}
	for _, m := range res.Messages {
		if m.Role == RoleSystem {
			t.Errorf("the note reached the record: %q", m.Text)
		}
	}
}

// The default key reads arguments as JSON, so key order doesn't make two
// calls different. A different result does.
func TestStuckDefaultKeyCanonicalArgs(t *testing.T) {
	_, _, events, err := stuckRun(t, StuckConfig{}, nil,
		StubTurn{ToolCalls: []ToolCall{call("c1", "run", `{"x":"a","y":1}`)}},
		StubTurn{ToolCalls: []ToolCall{call("c2", "run", `{"y":1, "x":"a"}`)}},
		StubTurn{ToolCalls: []ToolCall{call("c3", "run", `{"x":"a","y":1}`)}},
		StubTurn{Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if got := stuckEvents(events); len(got) != 1 || got[0].Pattern != StuckRepeat {
		t.Fatalf("stuck events = %+v, want one repeat", got)
	}

	_, _, events, err = stuckRun(t, StuckConfig{}, nil,
		runCall(1, "a"), runCall(2, "b"), runCall(3, "a"), StubTurn{Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if got := stuckEvents(events); len(got) != 0 {
		t.Fatalf("control: different calls tripped %+v", got)
	}
}

func TestStuckAlternation(t *testing.T) {
	_, _, events, err := stuckRun(t, StuckConfig{}, nil,
		runCall(1, "a"), runCall(2, "b"), runCall(3, "a"), runCall(4, "b"), runCall(5, "a"), runCall(6, "b"), StubTurn{Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	got := stuckEvents(events)
	if len(got) != 1 || got[0].Pattern != StuckAlternate || got[0].Count != 3 || !slices.Equal(got[0].Steps, []int{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("stuck events = %+v, want one alternation over 6 steps", got)
	}
}

// Three different calls that fail the same way trip the error pattern, even
// though no call repeats.
func TestStuckSameError(t *testing.T) {
	results := map[string]string{"a": "ERR no such file", "b": "ERR no such file", "c": "ERR no such file"}
	_, _, events, err := stuckRun(t, StuckConfig{}, results,
		runCall(1, "a"), runCall(2, "b"), runCall(3, "c"), StubTurn{Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if got := stuckEvents(events); len(got) != 1 || got[0].Pattern != StuckSameError {
		t.Fatalf("stuck events = %+v, want one same-error", got)
	}
}

// The detector reads only the most recent steps. Once the model does
// something else, it goes quiet again without any state to reset.
func TestStuckGoesQuietOnceThePatternBreaks(t *testing.T) {
	stub, _, events, err := stuckRun(t, StuckConfig{}, nil,
		runCall(1, "a"), runCall(2, "a"), runCall(3, "a"), runCall(4, "b"), StubTurn{Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if got := stuckEvents(events); len(got) != 1 {
		t.Fatalf("stuck events = %+v, want one, at step 4", got)
	}
	if note := lastSystem(stub.Requests()[4]); note != "" {
		t.Errorf("step 5 still has a note: %q", note)
	}
}

// Repeats from earlier turns are history. They don't count toward this turn.
func TestStuckIgnoresEarlierTurns(t *testing.T) {
	var history []Message
	for i := range 3 {
		id := fmt.Sprint("old", i)
		history = append(history,
			Message{Role: RoleAssistant, ToolCalls: []ToolCall{call(id, "run", `{"x":"a"}`)}},
			Message{Role: RoleTool, ToolCallID: id, Text: "ok a"})
	}
	history = append(history, Message{Role: RoleUser, Text: "again"})
	src := NewFuncSource()
	AddFunc(src, "run", "runs", func(ctx context.Context, in struct {
		X string `json:"x"`
	}) (string, error) {
		return "ok " + in.X, nil
	})
	stub := NewStubProvider(runCall(1, "a"), StubTurn{Text: "done"})
	r, _ := NewRunner(RunnerConfig{Provider: stub, Tools: src, StepStages: []StepStage{NewStuckDetector(StuckConfig{})}})
	var events []Event
	if _, err := r.Run(context.Background(), history, func(e Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	if got := stuckEvents(events); len(got) != 0 {
		t.Fatalf("earlier turns tripped the detector: %+v", got)
	}
}

// jaala's text-action roles make no tool calls (panyam/jaala#509). The model
// writes "try⏎<draft>", and a TurnEnd hook runs it and continues with the
// result as a user message. A host key reads that step from the reply and
// the continuation, so the detector sees the repeat the default key can't.
func TestStuckHostKeyOverTextActions(t *testing.T) {
	key := func(rec StepRecord) (StuckKey, bool) {
		draft, ok := strings.CutPrefix(rec.Assistant.Text, "try\n")
		if !ok || len(rec.Results) != 1 {
			return StuckKey{}, false
		}
		return StuckKey{Call: draft, Result: rec.Results[0].Text}, true
	}
	hook := func(ctx context.Context, info TurnEndInfo) (TurnEndVerdict, error) {
		if strings.HasPrefix(info.Text, "try\n") {
			return TurnEndVerdict{Continue: "matched nothing", AsUser: true}, nil
		}
		return TurnEndVerdict{}, nil
	}
	draft := StubTurn{Text: "try\ncalls(?f, ?g)"}
	for _, c := range []struct {
		name string
		key  StuckKeyFunc
		trip bool
	}{
		{"host key", key, true},
		{"control: the default key skips steps with no tool call", nil, false},
	} {
		stub := NewStubProvider(draft, draft, draft, StubTurn{Text: "final"})
		r, _ := NewRunner(RunnerConfig{
			Provider:   stub,
			TurnEnd:    []TurnEndHook{hook},
			StepStages: []StepStage{NewStuckDetector(StuckConfig{Key: c.key, Policy: StuckStop()})},
		})
		res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "translate"}}, nil)
		if c.trip {
			if !errors.Is(err, ErrTurnStopped) || res == nil || res.Steps != 3 || len(stub.Requests()) != 3 {
				t.Errorf("%s: err %v, result %+v, %d calls; want a stop after 3 steps", c.name, err, res, len(stub.Requests()))
			}
			continue
		}
		if err != nil || res.Text != "final" {
			t.Errorf("%s: err %v, result %+v", c.name, err, res)
		}
	}
}

func TestDefaultStuckKey(t *testing.T) {
	if _, ok := DefaultStuckKey(StepRecord{Assistant: Message{Role: RoleAssistant, Text: "hi"}}); ok {
		t.Errorf("a step with no tool call was keyed")
	}
	rec := func(result string) StepRecord {
		return StepRecord{
			Assistant: Message{Role: RoleAssistant, ToolCalls: []ToolCall{call("c1", "run", `{"x":1}`)}},
			Results:   []Message{{Role: RoleTool, ToolCallID: "c1", Text: result}, {Role: RoleSystem, Text: "a signal note"}},
		}
	}
	ok1, _ := DefaultStuckKey(rec("tool reported an error: boom"))
	ok2, _ := DefaultStuckKey(rec("fine"))
	if !ok1.Error || ok2.Error || ok1.Call != ok2.Call || ok1.Result == ok2.Result {
		t.Errorf("keys %+v / %+v", ok1, ok2)
	}
}

// Each policy acts on the call after the stuck step, and only that call.
func TestStuckPolicies(t *testing.T) {
	repeat := []StubTurn{runCall(1, "a"), runCall(2, "a"), runCall(3, "a"), StubTurn{Text: "done"}}
	t.Run("generation", func(t *testing.T) {
		stub, _, _, err := stuckRun(t, StuckConfig{Policy: StuckGeneration(GenerationParams{
			Temperature: floatPtr(0.7),
			Penalties:   &SamplingPenalties{Repeat: floatPtr(1.3)},
		})}, nil, append(slices.Clone(repeat[:3]), runCall(4, "b"), StubTurn{Text: "done"})...)
		if err != nil {
			t.Fatal(err)
		}
		reqs := stub.Requests()
		if reqs[2].Temperature != nil || reqs[2].Penalties != nil {
			t.Errorf("call 3 already changed: %+v", reqs[2])
		}
		if reqs[3].Temperature == nil || *reqs[3].Temperature != 0.7 || reqs[3].Penalties == nil || *reqs[3].Penalties.Repeat != 1.3 {
			t.Errorf("call 4: temperature %v, penalties %+v", reqs[3].Temperature, reqs[3].Penalties)
		}
		if reqs[4].Temperature != nil || reqs[4].Penalties != nil {
			t.Errorf("call 5 kept the change: %+v", reqs[4])
		}
	})
	t.Run("drop tools", func(t *testing.T) {
		stub, _, _, err := stuckRun(t, StuckConfig{Policy: StuckDropTools()}, nil, repeat...)
		if err != nil {
			t.Fatal(err)
		}
		names := func(r ProviderRequest) (out []string) {
			for _, td := range r.Tools {
				out = append(out, td.Name)
			}
			slices.Sort(out)
			return out
		}
		reqs := stub.Requests()
		if got := names(reqs[2]); !slices.Equal(got, []string{"other", "run"}) {
			t.Errorf("call 3 tools = %v", got)
		}
		if got := names(reqs[3]); !slices.Equal(got, []string{"other"}) {
			t.Errorf("call 4 tools = %v, want run taken away", got)
		}
	})
	t.Run("route", func(t *testing.T) {
		backup := NewStubProvider(StubTurn{Text: "from backup"})
		stub, res, events, err := stuckRun(t, StuckConfig{Policy: StuckRoute(backup, "backup")}, nil, repeat[:3]...)
		if err != nil {
			t.Fatal(err)
		}
		if len(stub.Requests()) != 3 || len(backup.Requests()) != 1 || res.Text != "from backup" {
			t.Errorf("primary %d calls, backup %d, text %q", len(stub.Requests()), len(backup.Requests()), res.Text)
		}
		var models []string
		for _, e := range events {
			if e.Kind == EventStep {
				models = append(models, e.Model)
			}
		}
		if !slices.Equal(models, []string{"", "", "", "backup"}) {
			t.Errorf("step models = %q", models)
		}
	})
	t.Run("stop", func(t *testing.T) {
		stub, res, events, err := stuckRun(t, StuckConfig{Policy: StuckStop()}, nil, repeat...)
		if !errors.Is(err, ErrTurnStopped) || !strings.Contains(err.Error(), "run") {
			t.Fatalf("err = %v, want ErrTurnStopped naming run", err)
		}
		if res == nil || res.Steps != 3 || len(stub.Requests()) != 3 {
			t.Fatalf("result %+v after %d calls, want a partial result after 3", res, len(stub.Requests()))
		}
		if n := len(res.Messages); n == 0 || res.Messages[n-1].Role != RoleTool {
			t.Errorf("record should end with the last tool result: %+v", res.Messages)
		}
		if len(stuckEvents(events)) != 1 {
			t.Errorf("stuck events = %+v", stuckEvents(events))
		}
	})
	t.Run("all", func(t *testing.T) {
		stub, _, _, err := stuckRun(t, StuckConfig{Policy: StuckAll(StuckNote(), StuckDropTools())}, nil, repeat...)
		if err != nil {
			t.Fatal(err)
		}
		req := stub.Requests()[3]
		if lastSystem(req) == "" || len(req.Tools) != 1 {
			t.Errorf("call 4: note %q, %d tools", lastSystem(req), len(req.Tools))
		}
	})
}

// A step stage outside request scope may not change a call's generation
// params, as with Guided and Provider.
func TestStepViewGenerationIsRequestScoped(t *testing.T) {
	for _, scope := range []StepScope{ScopeSession, ScopeTurn} {
		stage := StepStage{Name: "test.gen", Scope: scope, Run: func(_ context.Context, _ StepInfo, v StepView) (StepView, error) {
			v.Generation.Temperature = floatPtr(1)
			return v, nil
		}}
		r, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "x"}), StepStages: []StepStage{stage}})
		if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "q"}}, nil); err == nil || !strings.Contains(err.Error(), "generation") {
			t.Errorf("scope %d: err = %v, want a refusal", scope, err)
		}
	}
	var seen GenerationParams
	stage := StepStage{Name: "test.gen", Scope: ScopeRequest, Run: func(_ context.Context, _ StepInfo, v StepView) (StepView, error) {
		seen = v.Generation
		return v, nil
	}}
	r, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "x"}), StepStages: []StepStage{stage}, Generation: GenerationParams{MaxTokens: 77}})
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "q"}}, nil); err != nil || seen.MaxTokens != 77 {
		t.Errorf("err %v, the stage saw %+v, want the turn's params", err, seen)
	}
}
