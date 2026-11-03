package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// grammarStub is a StubProvider that says it takes a grammar, as llama.cpp's
// preset does.
type grammarStub struct{ *StubProvider }

func (grammarStub) ProviderCapabilities() Capabilities {
	return Capabilities{Guided: []string{GuidedGrammar, GuidedJSONSchema}}
}

var answerGrammar = &GuidedDecoding{Grammar: `root ::= "ans(" [a-z]+ ")"`}

func lookSource(t *testing.T) ToolSource {
	t.Helper()
	src := NewFuncSource()
	if err := AddFunc(src, "look", "looks", func(context.Context, struct{}) (string, error) { return "seen", nil }); err != nil {
		t.Fatal(err)
	}
	return src
}

func callLook(id string) StubTurn {
	return StubTurn{ToolCalls: []ToolCall{{ID: id, Name: "look", Args: core.NewRawJSON(json.RawMessage(`{}`))}}}
}

func guidedRun(t *testing.T, cfg RunnerConfig, req TurnRequest) (*TurnResult, error) {
	t.Helper()
	r, err := NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if req.History == nil {
		req.History = []Message{{Role: RoleUser, Text: "q"}}
	}
	return r.RunTurn(context.Background(), req)
}

// With no tools offered, every model call sends the grammar, and the turn
// makes no extra call.
func TestResponseGuidedWithoutToolsGuidesEveryStep(t *testing.T) {
	stub := grammarStub{NewStubProvider(StubTurn{Text: "ans(x)"})}
	res, err := guidedRun(t, RunnerConfig{Provider: stub, ResponseGuided: answerGrammar}, TurnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 1 || reqs[0].Guided != answerGrammar || res.Text != "ans(x)" {
		t.Fatalf("requests %+v, text %q", reqs, res.Text)
	}
}

// With tools offered, tool steps go unguided. When the model answers without a
// tool, one guided call with no tools writes the answer. The guided
// text replaces the free draft as the turn's answer and in its record.
func TestResponseGuidedWithToolsFinalizesTheAnswer(t *testing.T) {
	stub := grammarStub{NewStubProvider(callLook("c1"), StubTurn{Text: "the answer is x"}, StubTurn{Text: "ans(x)"})}
	gen := GenerationParams{ToolChoice: ToolChoiceRequired}
	res, err := guidedRun(t, RunnerConfig{Provider: stub, Tools: lookSource(t), ResponseGuided: answerGrammar, Generation: gen}, TurnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d calls, want two steps and one guided call", len(reqs))
	}
	for i := range 2 {
		if reqs[i].Guided != nil || len(reqs[i].Tools) == 0 {
			t.Errorf("step %d: guided %v, %d tools", i+1, reqs[i].Guided, len(reqs[i].Tools))
		}
	}
	fin := reqs[2]
	if fin.Guided != answerGrammar || len(fin.Tools) != 0 || !fin.ToolChoice.IsZero() {
		t.Fatalf("guided call: guided %v, tools %d, tool choice %+v", fin.Guided, len(fin.Tools), fin.ToolChoice)
	}
	if last := fin.Messages[len(fin.Messages)-1]; last.Role != RoleTool {
		t.Errorf("the guided call should answer from the tool result, ended with %+v", last)
	}
	if res.Text != "ans(x)" || res.Steps != 2 {
		t.Fatalf("text %q steps %d", res.Text, res.Steps)
	}
	final := res.Messages[len(res.Messages)-1]
	if final.Role != RoleAssistant || final.Text != "ans(x)" {
		t.Errorf("recorded answer %+v", final)
	}
	for _, m := range res.Messages {
		if m.Text == "the answer is x" {
			t.Errorf("the free draft stayed in the record: %+v", res.Messages)
		}
	}
}

// A TurnEnd hook judges the guided answer and sees the draft beside it. A
// refusal sends the model back, and its next answer is guided again.
func TestResponseGuidedTurnEndJudgesTheGuidedAnswer(t *testing.T) {
	stub := grammarStub{NewStubProvider(
		StubTurn{Text: "draft one"}, StubTurn{Text: "ans(bad)"},
		StubTurn{Text: "draft two"}, StubTurn{Text: "ans(good)"},
	)}
	var seen []TurnEndInfo
	hook := func(_ context.Context, info TurnEndInfo) (TurnEndVerdict, error) {
		seen = append(seen, info)
		if info.Text == "ans(bad)" {
			return TurnEndVerdict{Continue: "bad is not a relation"}, nil
		}
		return TurnEndVerdict{}, nil
	}
	res, err := guidedRun(t, RunnerConfig{Provider: stub, Tools: lookSource(t), ResponseGuided: answerGrammar, TurnEnd: []TurnEndHook{hook}}, TurnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].Text != "ans(bad)" || seen[0].Draft != "draft one" || seen[1].Text != "ans(good)" || seen[1].Draft != "draft two" {
		t.Fatalf("the hook saw %+v", seen)
	}
	if res.Text != "ans(good)" {
		t.Fatalf("text %q", res.Text)
	}
	if g := stub.Requests()[3].Guided; g != answerGrammar {
		t.Errorf("the answer after the refusal was not guided: %v", g)
	}
	var roles []string
	for _, m := range res.Messages {
		roles = append(roles, string(m.Role)+":"+m.Text)
	}
	if got := strings.Join(roles, "|"); got != "assistant:ans(bad)|system:bad is not a relation|assistant:ans(good)" {
		t.Errorf("record %s", got)
	}
}

func TestResponseGuidedPerTurnOverride(t *testing.T) {
	other := &GuidedDecoding{Grammar: `root ::= "y"`}
	stub := grammarStub{NewStubProvider(StubTurn{Text: "y"})}
	if _, err := guidedRun(t, RunnerConfig{Provider: stub, ResponseGuided: answerGrammar}, TurnRequest{ResponseGuided: other}); err != nil {
		t.Fatal(err)
	}
	if g := stub.Requests()[0].Guided; g != other {
		t.Fatalf("sent %v, want the turn's grammar", g)
	}
}

func TestResponseGuidedRefusedBeforeAnyCall(t *testing.T) {
	for name, c := range map[string]struct {
		p   *StubProvider
		cfg func(Provider) RunnerConfig
	}{
		"provider can't take a regex": {NewStubProvider(StubTurn{Text: "x"}), func(p Provider) RunnerConfig {
			return RunnerConfig{Provider: grammarStub{p.(*StubProvider)}, ResponseGuided: &GuidedDecoding{Regex: "x"}}
		}},
		"provider reports nothing": {NewStubProvider(StubTurn{Text: "x"}), func(p Provider) RunnerConfig {
			return RunnerConfig{Provider: p, ResponseGuided: answerGrammar}
		}},
		"no kind set": {NewStubProvider(StubTurn{Text: "x"}), func(p Provider) RunnerConfig {
			return RunnerConfig{Provider: grammarStub{p.(*StubProvider)}, ResponseGuided: &GuidedDecoding{}}
		}},
	} {
		_, err := guidedRun(t, c.cfg(c.p), TurnRequest{})
		if !errors.Is(err, ErrUnsupportedParam) || len(c.p.Requests()) != 0 {
			t.Errorf("%s: err %v after %d calls", name, err, len(c.p.Requests()))
		}
	}
	stub := grammarStub{NewStubProvider(StubTurn{Text: "x"})}
	_, err := guidedRun(t, RunnerConfig{Provider: stub, ResponseGuided: answerGrammar, ResponseSchema: core.NewRawJSON(json.RawMessage(`{"type":"object"}`))}, TurnRequest{})
	if err == nil || !strings.Contains(err.Error(), "ResponseGuided") || len(stub.Requests()) != 0 {
		t.Errorf("schema and grammar together: %v after %d calls", err, len(stub.Requests()))
	}
}
