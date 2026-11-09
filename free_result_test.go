package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// answerAgainFree answers an echo of "again" from middleware with a free
// result, and lets anything else run.
var answerAgainFree = ToolMiddleware(func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
	if strings.Contains(string(info.Call.Args.Raw()), `"again"`) {
		return nil, &FreeResultError{Text: "echo: again (asked before)", Reason: "repeat"}
	}
	return next(ctx, info)
})

// lastToolText is the last tool result the model saw on request n.
func lastToolText(t *testing.T, stub *StubProvider, n int) string {
	t.Helper()
	msgs := stub.Requests()[n].Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleTool {
			return msgs[i].Text
		}
	}
	t.Fatalf("request %d has no tool result", n)
	return ""
}

// A free result reads as the call's result, with no denial prefix, and the
// step costs nothing.
func TestAFreeResultReadsAsAResultAndCostsNoStep(t *testing.T) {
	res, events, stub, err := runFree(t, RunnerConfig{MaxSteps: 1}, []ToolMiddleware{answerAgainFree},
		echoCall("again"), echoCall("again"), StubTurn{Text: "done"})
	if err != nil || res.Text != "done" {
		t.Fatalf("%+v %v", res, err)
	}
	if got := lastToolText(t, stub, 1); got != "echo: again (asked before)" {
		t.Errorf("model read %q", got)
	}
	if res.Steps != 1 || res.FreeRefusals != 2 {
		t.Errorf("steps %d, free %d; want 1, 2", res.Steps, res.FreeRefusals)
	}
	free := kindsOf(events, EventRefusalFree)
	if len(free) != 2 || free[0].Reason != "repeat" {
		t.Errorf("free events %+v", free)
	}
	if n := len(kindsOf(events, EventToolDenied)); n != 0 {
		t.Errorf("%d denials for a free result", n)
	}
	ends := kindsOf(events, EventToolEnd)
	if len(ends) != 2 || toolResultText(ends[0].ToolResult) != "echo: again (asked before)" {
		t.Errorf("tool-end events %+v", ends)
	}
}

// Past the cap the model still reads the result, and the step counts.
func TestFreeResultsPastTheCapCount(t *testing.T) {
	res, _, stub, err := runFree(t, RunnerConfig{MaxFreeRefusals: 1}, []ToolMiddleware{answerAgainFree},
		echoCall("again"), echoCall("again"), StubTurn{Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps != 2 || res.FreeRefusals != 1 {
		t.Errorf("steps %d, free %d; want 2, 1", res.Steps, res.FreeRefusals)
	}
	if got := lastToolText(t, stub, 2); got != "echo: again (asked before)" {
		t.Errorf("model read %q past the cap", got)
	}
}

// A step mixing a free result and a free denial is free, under both reasons.
func TestAStepOfFreeResultsAndFreeDenialsIsFree(t *testing.T) {
	both := StubTurn{ToolCalls: []ToolCall{toolCall("c1", "echo", `{"msg":"again"}`), toolCall("c2", "echo", `{"msg":"bad"}`)}}
	res, events, _, err := runFree(t, RunnerConfig{}, []ToolMiddleware{answerAgainFree, refuseBadFree}, both, StubTurn{Text: "done"})
	if err != nil || res.Steps != 1 || res.FreeRefusals != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if free := kindsOf(events, EventRefusalFree); len(free) != 1 || free[0].Reason != "repeat; bad does not parse" {
		t.Errorf("free events %+v", free)
	}
}

// An AddFunc handler can return a free result or a free denial itself, with
// no middleware, and the Runner treats it as one from middleware.
func TestAFuncSourceToolCanAnswerOrRefuseFree(t *testing.T) {
	for _, c := range []struct {
		name, want string
		err        error
	}{
		{"result", "seen it", FreeResult("seen it")},
		{"wrapped result", "seen it", fmt.Errorf("try: %w", FreeResult("seen it"))},
		{"denial", toolDeniedPrefix + "no", DenyToolFree("no")},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := NewFuncSource()
			if err := AddFunc(src, "try", "tries", func(context.Context, struct{}) (string, error) { return "", c.err }); err != nil {
				t.Fatal(err)
			}
			stub := NewStubProvider(StubTurn{ToolCalls: []ToolCall{toolCall("c1", "try", `{}`)}}, StubTurn{Text: "done"})
			r, err := NewRunner(RunnerConfig{Provider: stub, Tools: src, MaxSteps: 1})
			if err != nil {
				t.Fatal(err)
			}
			res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "q"}}, nil)
			if err != nil || res.Steps != 1 || res.FreeRefusals != 1 {
				t.Fatalf("%+v %v", res, err)
			}
			if got := lastToolText(t, stub, 1); got != c.want {
				t.Errorf("model read %q, want %q", got, c.want)
			}
		})
	}
}

func toolCall(id, name, args string) ToolCall {
	return ToolCall{ID: id, Name: name, Args: core.NewRawJSON(json.RawMessage(args))}
}
