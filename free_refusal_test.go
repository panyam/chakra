package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// refuseBadFree refuses an echo of "bad" as free, and lets anything else run.
var refuseBadFree = BeforeToolCall(func(_ context.Context, info *ToolCallInfo) error {
	if strings.Contains(string(info.Call.Args.Raw()), `"bad"`) {
		return DenyToolFree("bad does not parse")
	}
	return nil
})

func runFree(t *testing.T, cfg RunnerConfig, mw []ToolMiddleware, turns ...StubTurn) (*TurnResult, []Event, *StubProvider, error) {
	t.Helper()
	src := NewFuncSource()
	type in struct {
		Msg string `json:"msg"`
	}
	if err := AddFunc(src, "echo", "echoes msg", func(_ context.Context, i in) (string, error) {
		return "echo: " + i.Msg, nil
	}); err != nil {
		t.Fatal(err)
	}
	stub := NewStubProvider(turns...)
	cfg.Provider, cfg.Tools, cfg.ToolMiddleware = stub, src, mw
	r, err := NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	ctx := context.Background()
	if cfg.TreeBudget != (TreeBudget{}) {
		ctx = WithTreeBudget(ctx, cfg.TreeBudget)
	}
	res, err := r.Run(ctx, []Message{{Role: RoleUser, Text: "q"}}, func(e Event) { events = append(events, e) })
	return res, events, stub, err
}

// Three free refusals and then an answer end with the step count of the bare
// answer. Under MaxSteps 2 the turn still finishes, though it took four calls.
func TestFreeToolRefusalsDoNotUseUpSteps(t *testing.T) {
	res, events, stub, err := runFree(t, RunnerConfig{MaxSteps: 2}, []ToolMiddleware{refuseBadFree},
		echoCall("bad"), echoCall("bad"), echoCall("bad"), StubTurn{Text: "done"})
	if err != nil || res.Text != "done" {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Steps != 1 || res.FreeRefusals != 3 || len(stub.Requests()) != 4 {
		t.Errorf("steps %d, free %d, calls %d; want 1, 3, 4", res.Steps, res.FreeRefusals, len(stub.Requests()))
	}
	free := kindsOf(events, EventRefusalFree)
	if len(free) != 3 || free[0].Step != 1 || free[2].Step != 3 || free[0].Reason != "bad does not parse" {
		t.Errorf("free-refusal events %+v", free)
	}
	if len(kindsOf(events, EventToolDenied)) != 3 {
		t.Errorf("each free refusal is still a denial on the wire")
	}
}

// Past the cap a refusal counts. Four refusals under the default cap of
// three leave one counted, plus the answer.
func TestFreeRefusalsPastTheCapCount(t *testing.T) {
	res, events, _, err := runFree(t, RunnerConfig{}, []ToolMiddleware{refuseBadFree},
		echoCall("bad"), echoCall("bad"), echoCall("bad"), echoCall("bad"), StubTurn{Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps != 2 || res.FreeRefusals != defaultMaxFreeRefusals || len(kindsOf(events, EventRefusalFree)) != 3 {
		t.Errorf("steps %d, free %d, events %d; want 2, 3, 3", res.Steps, res.FreeRefusals, len(kindsOf(events, EventRefusalFree)))
	}
}

// Past the cap, a turn at its step limit ends with ErrMaxSteps.
func TestFreeRefusalsPastTheCapHitTheStepLimit(t *testing.T) {
	turns := []StubTurn{}
	for range 6 {
		turns = append(turns, echoCall("bad"))
	}
	res, _, stub, err := runFree(t, RunnerConfig{MaxSteps: 2, MaxFreeRefusals: 2}, []ToolMiddleware{refuseBadFree}, turns...)
	if !errors.Is(err, ErrMaxSteps) || res == nil || res.Steps != 2 || res.FreeRefusals != 2 || len(stub.Requests()) != 4 {
		t.Fatalf("%+v %v after %d calls", res, err, len(stub.Requests()))
	}
}

// A negative cap turns free refusals off.
func TestNegativeMaxFreeRefusalsCountsEveryRefusal(t *testing.T) {
	res, events, _, err := runFree(t, RunnerConfig{MaxFreeRefusals: -1}, []ToolMiddleware{refuseBadFree},
		echoCall("bad"), StubTurn{Text: "done"})
	if err != nil || res.Steps != 2 || res.FreeRefusals != 0 || len(kindsOf(events, EventRefusalFree)) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A step where one call ran made progress, so it counts even though its other
// call was refused free.
func TestAStepWithACallThatRanIsNotFree(t *testing.T) {
	both := StubTurn{ToolCalls: []ToolCall{
		{ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"msg":"bad"}`))},
		{ID: "c2", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"msg":"good"}`))},
	}}
	res, events, _, err := runFree(t, RunnerConfig{}, []ToolMiddleware{refuseBadFree}, both, StubTurn{Text: "done"})
	if err != nil || res.Steps != 2 || res.FreeRefusals != 0 || len(kindsOf(events, EventRefusalFree)) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A refusal that isn't marked free counts, as it always has.
func TestAPlainDenialStillCounts(t *testing.T) {
	deny := BeforeToolCall(func(context.Context, *ToolCallInfo) error { return DenyTool("no") })
	res, _, _, err := runFree(t, RunnerConfig{}, []ToolMiddleware{deny}, echoCall("x"), StubTurn{Text: "done"})
	if err != nil || res.Steps != 2 || res.FreeRefusals != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A free turn-end Continue doesn't use up a step. With MaxSteps 1 the turn
// takes three calls and still ends with one step. The hook sees the call
// number in Step and the counted steps left in StepsLeft.
func TestFreeTurnEndContinueDoesNotUseUpAStep(t *testing.T) {
	var seen []TurnEndInfo
	hook := func(_ context.Context, info TurnEndInfo) (TurnEndVerdict, error) {
		seen = append(seen, info)
		if len(seen) <= 2 {
			return TurnEndVerdict{Continue: "draft repeats the last one", Free: true}, nil
		}
		return TurnEndVerdict{}, nil
	}
	stub := NewStubProvider(StubTurn{Text: "a"}, StubTurn{Text: "a"}, StubTurn{Text: "b"})
	res, events, err := runTurnEnd(t, RunnerConfig{Provider: stub, MaxSteps: 1, TurnEnd: []TurnEndHook{hook}})
	if err != nil || res.Text != "b" || res.Steps != 1 || res.FreeRefusals != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(seen) != 3 || seen[2].Step != 3 || seen[2].StepsLeft != 1 || seen[0].StepsLeft != 1 {
		t.Errorf("the hook saw %+v", seen)
	}
	if len(kindsOf(events, EventRefusalFree)) != 2 || len(kindsOf(events, EventTurnContinued)) != 2 {
		t.Errorf("events %+v", events)
	}
}

// Free without Continue means nothing. The answer is accepted.
func TestTurnEndFreeWithoutContinueAccepts(t *testing.T) {
	hook := func(context.Context, TurnEndInfo) (TurnEndVerdict, error) { return TurnEndVerdict{Free: true}, nil }
	res, _, err := runTurnEnd(t, RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "a"}), TurnEnd: []TurnEndHook{hook}})
	if err != nil || res.Text != "a" || res.Steps != 1 || res.FreeRefusals != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A free step gives its tree-budget step back. Two free refusals and an
// answer fit in a budget of two steps.
func TestAFreeStepRefundsTheTreeBudget(t *testing.T) {
	res, _, stub, err := runFree(t, RunnerConfig{TreeBudget: TreeBudget{MaxSteps: 2}}, []ToolMiddleware{refuseBadFree},
		echoCall("bad"), echoCall("bad"), StubTurn{Text: "done"})
	if err != nil || res.Steps != 1 || len(stub.Requests()) != 3 {
		t.Fatalf("%+v %v after %d calls", res, err, len(stub.Requests()))
	}
}

// StepInfo.StepsLeft counts the steps the turn may still count, this one
// included. A free step leaves it where it was.
func TestStepInfoStepsLeftSkipsFreeSteps(t *testing.T) {
	var left []int
	probe := StepStage{Name: "probe", Scope: ScopeRequest, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
		left = append(left, info.StepsLeft)
		return v, nil
	}}
	_, _, _, err := runFree(t, RunnerConfig{MaxSteps: 3, StepStages: []StepStage{probe}}, []ToolMiddleware{refuseBadFree},
		echoCall("bad"), echoCall("good"), StubTurn{Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(left) != "[3 3 2]" {
		t.Errorf("StepsLeft per call %v, want [3 3 2]", left)
	}
}

// A free refusal is still a denied call in the record, so the stuck detector
// sees three identical ones. It fires on the fourth call, as the cap runs out.
func TestStuckDetectorSeesRepeatedFreeRefusals(t *testing.T) {
	turns := []StubTurn{echoCall("bad"), echoCall("bad"), echoCall("bad"), StubTurn{Text: "done"}}
	res, events, _, err := runFree(t, RunnerConfig{StepStages: []StepStage{NewStuckDetector(StuckConfig{})}}, []ToolMiddleware{refuseBadFree}, turns...)
	if err != nil || res.FreeRefusals != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	stuck := kindsOf(events, EventStuck)
	if len(stuck) != 1 || stuck[0].Step != 4 {
		t.Errorf("stuck events %+v, want one before call 4", stuck)
	}
}
