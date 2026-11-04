package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

var otherGrammar = &GuidedDecoding{Grammar: `root ::= "look " [a-z]+`}

// guideFrom sets g as the grammar for every step from `from` on.
func guideFrom(from int, g *GuidedDecoding) StepStage {
	return StepStage{Name: "test.guide", Scope: ScopeRequest, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
		if info.Step >= from {
			v.Guided = g
		}
		return v, nil
	}}
}

// continueOnce sends the model back once, so a turn without tools reaches a
// second step.
func continueOnce() TurnEndHook {
	n := 0
	return func(context.Context, TurnEndInfo) (TurnEndVerdict, error) {
		n++
		if n == 1 {
			return TurnEndVerdict{Continue: "again"}, nil
		}
		return TurnEndVerdict{}, nil
	}
}

func TestStepStageSwitchesTheGrammar(t *testing.T) {
	stub := grammarStub{NewStubProvider(StubTurn{Text: "ans(a)"}, StubTurn{Text: "look b"})}
	_, err := guidedRun(t, RunnerConfig{
		Provider:       stub,
		ResponseGuided: answerGrammar,
		StepStages:     []StepStage{guideFrom(2, otherGrammar)},
		TurnEnd:        []TurnEndHook{continueOnce()},
	}, TurnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 2 || reqs[0].Guided != answerGrammar || reqs[1].Guided != otherGrammar {
		t.Fatalf("sent %d calls with grammars %v, want the turn's then the stage's", len(reqs), guidedOf(reqs))
	}
}

// jaala#509's shape. Tools go unguided until the last step, where a request
// stage drops the tools and sets the answer grammar.
func TestStepStageDropsToolsAndSetsTheAnswerGrammar(t *testing.T) {
	last := StepStage{Name: "test.last", Scope: ScopeRequest, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
		if info.Step == info.MaxSteps {
			v.Tools, v.Guided = nil, answerGrammar
		}
		return v, nil
	}}
	stub := grammarStub{NewStubProvider(callLook("c1"), StubTurn{Text: "ans(x)"})}
	res, err := guidedRun(t, RunnerConfig{Provider: stub, Tools: lookSource(t), MaxSteps: 2, StepStages: []StepStage{last}}, TurnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 2 || reqs[0].Guided != nil || len(reqs[0].Tools) == 0 || reqs[1].Guided != answerGrammar || len(reqs[1].Tools) != 0 {
		t.Fatalf("calls %d, grammars %v", len(reqs), guidedOf(reqs))
	}
	if res.Text != "ans(x)" {
		t.Errorf("text %q", res.Text)
	}
}

func TestStepStageClearsTheGrammarForOneCall(t *testing.T) {
	only1 := StepStage{Name: "test.clear", Scope: ScopeRequest, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
		if info.Step == 1 {
			v.Guided = nil
		}
		return v, nil
	}}
	stub := grammarStub{NewStubProvider(StubTurn{Text: "free"}, StubTurn{Text: "ans(a)"})}
	_, err := guidedRun(t, RunnerConfig{
		Provider:       stub,
		ResponseGuided: answerGrammar,
		StepStages:     []StepStage{only1},
		TurnEnd:        []TurnEndHook{continueOnce()},
	}, TurnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 2 || reqs[0].Guided != nil || reqs[1].Guided != answerGrammar {
		t.Fatalf("grammars %v, want none on step 1 and the turn's on step 2", guidedOf(reqs))
	}
}

func TestOnlyRequestStagesMaySetTheGrammar(t *testing.T) {
	for _, scope := range []StepScope{ScopeSession, ScopeTurn} {
		st := guideFrom(1, otherGrammar)
		st.Scope = scope
		stub := grammarStub{NewStubProvider(StubTurn{Text: "x"})}
		_, err := guidedRun(t, RunnerConfig{Provider: stub, ResponseGuided: answerGrammar, StepStages: []StepStage{st}}, TurnRequest{})
		if err == nil || !strings.Contains(err.Error(), "only a request stage") || len(stub.Requests()) != 0 {
			t.Errorf("scope %d: err %v after %d calls, want the request-only refusal", scope, err, len(stub.Requests()))
		}
	}
	// A turn stage that leaves the turn's grammar alone is fine.
	pass := StepStage{Name: "test.pass", Scope: ScopeTurn, Run: func(_ context.Context, _ StepInfo, v StepView) (StepView, error) { return v, nil }}
	stub := grammarStub{NewStubProvider(StubTurn{Text: "ans(a)"})}
	if _, err := guidedRun(t, RunnerConfig{Provider: stub, ResponseGuided: answerGrammar, StepStages: []StepStage{pass}}, TurnRequest{}); err != nil {
		t.Fatal(err)
	}
	if g := stub.Requests()[0].Guided; g != answerGrammar {
		t.Errorf("sent %v, want the turn's grammar through a pass-through turn stage", g)
	}
}

func TestStageGrammarChecksTheProviderItGoesTo(t *testing.T) {
	plain := NewStubProvider(StubTurn{Text: "ans(x)"})
	_, err := guidedRun(t, RunnerConfig{
		Provider:   grammarStub{NewStubProvider(StubTurn{Text: "ans(y)"})},
		StepStages: []StepStage{guideFrom(1, answerGrammar), routeFrom(1, "plain", plain)},
	}, TurnRequest{})
	if !errors.Is(err, ErrUnsupportedParam) || len(plain.Requests()) != 0 {
		t.Errorf("routed: err %v after %d calls, want ErrUnsupportedParam before any call", err, len(plain.Requests()))
	}

	unrouted := NewStubProvider(StubTurn{Text: "ans(x)"})
	_, err = guidedRun(t, RunnerConfig{Provider: unrouted, StepStages: []StepStage{guideFrom(1, answerGrammar)}}, TurnRequest{})
	if !errors.Is(err, ErrUnsupportedParam) || len(unrouted.Requests()) != 0 {
		t.Errorf("configured provider: err %v after %d calls", err, len(unrouted.Requests()))
	}

	bad := grammarStub{NewStubProvider(StubTurn{Text: "x"})}
	_, err = guidedRun(t, RunnerConfig{Provider: bad, StepStages: []StepStage{guideFrom(1, &GuidedDecoding{})}}, TurnRequest{})
	if !errors.Is(err, ErrUnsupportedParam) || len(bad.Requests()) != 0 {
		t.Errorf("no kind set: err %v after %d calls", err, len(bad.Requests()))
	}
}

func TestToolStepFinalizesUnderTheStageGrammar(t *testing.T) {
	stub := grammarStub{NewStubProvider(callLook("c1"), StubTurn{Text: "it is x"}, StubTurn{Text: "look x"})}
	res, err := guidedRun(t, RunnerConfig{
		Provider:       stub,
		Tools:          lookSource(t),
		ResponseGuided: answerGrammar,
		StepStages:     []StepStage{guideFrom(2, otherGrammar)},
	}, TurnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 3 || reqs[0].Guided != nil || reqs[1].Guided != nil || reqs[2].Guided != otherGrammar {
		t.Fatalf("grammars %v, want two unguided tool steps and a finalize under the stage's", guidedOf(reqs))
	}
	if res.Text != "look x" {
		t.Errorf("text %q", res.Text)
	}
}

func TestToolStepWithClearedGrammarSkipsTheFinalize(t *testing.T) {
	clear := StepStage{Name: "test.clear", Scope: ScopeRequest, Run: func(_ context.Context, _ StepInfo, v StepView) (StepView, error) {
		v.Guided = nil
		return v, nil
	}}
	stub := grammarStub{NewStubProvider(callLook("c1"), StubTurn{Text: "it is x"})}
	res, err := guidedRun(t, RunnerConfig{Provider: stub, Tools: lookSource(t), ResponseGuided: answerGrammar, StepStages: []StepStage{clear}}, TurnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(stub.Requests()); n != 2 || res.Text != "it is x" {
		t.Fatalf("%d calls, text %q, want two steps and the free answer", n, res.Text)
	}
}

func TestStageGrammarRefusedBesideAResponseSchema(t *testing.T) {
	stub := grammarStub{NewStubProvider(StubTurn{Text: "{}"})}
	_, err := guidedRun(t, RunnerConfig{
		Provider:       stub,
		ResponseSchema: core.NewRawJSON(json.RawMessage(`{"type":"object"}`)),
		StepStages:     []StepStage{guideFrom(1, answerGrammar)},
	}, TurnRequest{})
	if err == nil || !strings.Contains(err.Error(), "one constraint") || len(stub.Requests()) != 0 {
		t.Errorf("err %v after %d calls, want the one-constraint refusal", err, len(stub.Requests()))
	}
}

func guidedOf(reqs []ProviderRequest) []*GuidedDecoding {
	out := make([]*GuidedDecoding, len(reqs))
	for i, r := range reqs {
		out[i] = r.Guided
	}
	return out
}
