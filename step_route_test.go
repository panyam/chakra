package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// routeFrom sends every step from `from` on to p under name.
func routeFrom(from int, name string, p Provider) StepStage {
	return StepStage{Name: "test.route", Scope: ScopeRequest, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
		if info.Step >= from {
			v.Provider, v.ProviderName = p, name
		}
		return v, nil
	}}
}

func withUsage(t StubTurn, in, out int) StubTurn {
	d := t.deltas()
	t.Deltas = append(d[:len(d)-1:len(d)-1], Delta{Kind: DeltaUsage, Usage: &Usage{InputTokens: in, OutputTokens: out}}, d[len(d)-1])
	return t
}

func TestStepStageRoutesOneCall(t *testing.T) {
	big := NewStubProvider(dumpCalls(1)[0])
	small := NewStubProvider(StubTurn{Text: "done"})
	r, _ := NewRunner(RunnerConfig{Provider: big, Tools: bigTool(1), StepStages: []StepStage{routeFrom(2, "small", small)}})
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" {
		t.Errorf("text = %q", res.Text)
	}
	if n := len(big.Requests()); n != 1 {
		t.Errorf("default provider got %d calls, want step 1 only", n)
	}
	if n := len(small.Requests()); n != 1 {
		t.Fatalf("routed provider got %d calls, want step 2 only", n)
	}
	// The routed call sees the whole turn so far, tool result included.
	if msgs := small.Requests()[0].Messages; len(msgs) != 3 || msgs[2].Role != RoleTool {
		t.Errorf("routed call sent %d messages, want user, assistant, tool", len(msgs))
	}
}

func TestRoutedProviderIsNotSticky(t *testing.T) {
	big := NewStubProvider(dumpCalls(1)[0], StubTurn{Text: "done"})
	small := NewStubProvider(dumpCalls(2)[1])
	only2 := StepStage{Name: "test.only2", Scope: ScopeRequest, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
		if info.Step == 2 {
			v.Provider, v.ProviderName = small, "small"
		}
		return v, nil
	}}
	r, _ := NewRunner(RunnerConfig{Provider: big, Tools: bigTool(1), StepStages: []StepStage{only2}})
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil); err != nil {
		t.Fatal(err)
	}
	if n, m := len(big.Requests()), len(small.Requests()); n != 2 || m != 1 {
		t.Errorf("default got %d calls and routed %d, want 2 and 1 (steps 1 and 3 back on the default)", n, m)
	}
}

func TestOnlyRequestStagesMayRoute(t *testing.T) {
	for _, scope := range []StepScope{ScopeSession, ScopeTurn} {
		st := routeFrom(1, "small", NewStubProvider(StubTurn{Text: "x"}))
		st.Scope = scope
		r, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "x"}), StepStages: []StepStage{st}})
		_, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "only a request stage") {
			t.Errorf("scope %d: err = %v, want the request-only refusal", scope, err)
		}
	}
}

func TestRoutedGuidedStepChecksTheRoutedProvider(t *testing.T) {
	plain := NewStubProvider(StubTurn{Text: "ans(x)"})
	_, err := guidedRun(t, RunnerConfig{
		Provider:       grammarStub{NewStubProvider(StubTurn{Text: "ans(y)"})},
		ResponseGuided: answerGrammar,
		StepStages:     []StepStage{routeFrom(1, "plain", plain)},
	}, TurnRequest{})
	if !errors.Is(err, ErrUnsupportedParam) {
		t.Fatalf("err = %v, want ErrUnsupportedParam for a routed provider that can't guide", err)
	}
	if n := len(plain.Requests()); n != 0 {
		t.Errorf("routed provider was called %d times; the constraint must not be sent unenforced", n)
	}
}

func TestStepEventCarriesModelAndUsage(t *testing.T) {
	big := NewStubProvider(withUsage(dumpCalls(1)[0], 100, 10))
	small := NewStubProvider(withUsage(StubTurn{Text: "done"}, 50, 5))
	r, _ := NewRunner(RunnerConfig{Provider: big, Tools: bigTool(1), StepStages: []StepStage{routeFrom(2, "small", small)}})
	var steps []Event
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventStep {
			steps = append(steps, e)
		}
	}); err != nil {
		t.Fatal(err)
	}
	// Step 1 was not named, so it emits nothing. An unrouted turn's stream
	// is unchanged.
	if len(steps) != 1 {
		t.Fatalf("got %d step events, want 1 (the named step only)", len(steps))
	}
	if s := steps[0]; s.Step != 2 || s.Model != "small" || s.Usage == nil || s.Usage.InputTokens != 50 || s.Usage.OutputTokens != 5 {
		t.Errorf("step event = %+v, want step 2 on small with 50/5 tokens", s)
	}
}

func TestUsageByModelOnlyWhenNamed(t *testing.T) {
	plain, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(withUsage(StubTurn{Text: "x"}, 1, 1))})
	res, err := plain.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.UsageByModel != nil {
		t.Errorf("UsageByModel = %v on a turn no stage named, want nil", res.UsageByModel)
	}

	big := NewStubProvider(withUsage(dumpCalls(1)[0], 100, 10))
	small := NewStubProvider(withUsage(StubTurn{Text: "done"}, 50, 5))
	label := StepStage{Name: "test.label", Scope: ScopeRequest, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
		if info.Step == 1 {
			v.ProviderName = "big" // names the default without replacing it
		}
		return v, nil
	}}
	r, _ := NewRunner(RunnerConfig{Provider: big, Tools: bigTool(1), StepStages: []StepStage{label, routeFrom(2, "small", small)}})
	res, err = r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(big.Requests()) != 1 {
		t.Errorf("a name without a provider must keep the default provider")
	}
	want := map[string]Usage{"big": {InputTokens: 100, OutputTokens: 10}, "small": {InputTokens: 50, OutputTokens: 5}}
	if len(res.UsageByModel) != 2 || res.UsageByModel["big"] != want["big"] || res.UsageByModel["small"] != want["small"] {
		t.Errorf("UsageByModel = %v, want %v", res.UsageByModel, want)
	}
	if res.Usage.InputTokens != 150 || res.Usage.OutputTokens != 15 {
		t.Errorf("Usage = %+v, want the total across both", res.Usage)
	}
}

func TestUsageByModelOnPartialTurn(t *testing.T) {
	big := NewStubProvider(withUsage(dumpCalls(1)[0], 100, 10))
	small := NewStubProvider(withUsage(dumpCalls(2)[1], 50, 5))
	r, _ := NewRunner(RunnerConfig{Provider: big, Tools: bigTool(1), MaxSteps: 2, StepStages: []StepStage{routeFrom(2, "small", small)}})
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
	if res == nil || res.UsageByModel["small"].InputTokens != 50 {
		t.Errorf("partial result UsageByModel = %v, want the routed step's usage kept", res)
	}
}

// A Responses provider chains from its response to the last history that it
// answered. Imagine another provider answers a step. That step extends the
// history with a message that the server never saw. So the next Responses
// call misses the cursor. It resends the turn in full instead of chaining past
// the gap.
func TestRoutedStepMakesTheNextResponsesCallResendInFull(t *testing.T) {
	f := &rsFake{reply: func(n int, _ map[string]any) (int, string) {
		if n == 1 {
			return 0, rsReplyCall(n, "dump")
		}
		return 0, rsReplyText(n, "done")
	}}
	ts := f.serve(t)
	var notes []string
	rs := newTestResponsesProvider(t, ts.URL, ResponsesConfig{OnFullHistory: func(r string) { notes = append(notes, r) }})
	other := NewStubProvider(dumpCalls(2)[1])
	only2 := StepStage{Name: "test.only2", Scope: ScopeRequest, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
		if info.Step == 2 {
			v.Provider, v.ProviderName = other, "other"
		}
		return v, nil
	}}
	r, _ := NewRunner(RunnerConfig{Provider: rs, Tools: bigTool(1), StepStages: []StepStage{only2}})
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil); err != nil {
		t.Fatal(err)
	}
	req := f.request(1)
	if _, ok := req["previous_response_id"]; ok {
		t.Fatalf("step 3 chained past a step the server never saw: %v", req)
	}
	if got := len(inputItems(req)); got != 5 {
		t.Errorf("step 3 sent %d input items, want the full 5 (user, call, result, call, result)", got)
	}
	if len(notes) != 1 || notes[0] != FullHistoryNoCursor {
		t.Errorf("notes = %v, want one FullHistoryNoCursor", notes)
	}
}
