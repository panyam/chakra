package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func planCall(id string, args any) ToolCall {
	raw, _ := json.Marshal(args)
	return ToolCall{ID: id, Name: PlanToolName, Args: core.NewRawJSON(raw)}
}

func steps(pairs ...string) []map[string]string {
	var out []map[string]string
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]string{"step": pairs[i], "status": pairs[i+1]})
	}
	return out
}

func TestPlanValidate(t *testing.T) {
	for _, c := range []struct {
		name string
		p    Plan
		ok   bool
	}{
		{"one in progress", Plan{Steps: []PlanStep{{"read", PlanCompleted}, {"fix", PlanInProgress}, {"test", PlanPending}}}, true},
		{"none in progress", Plan{Steps: []PlanStep{{"read", PlanPending}}}, true},
		{"no steps", Plan{}, false},
		{"empty step", Plan{Steps: []PlanStep{{" ", PlanPending}}}, false},
		{"unknown status", Plan{Steps: []PlanStep{{"read", "done"}}}, false},
		{"two in progress", Plan{Steps: []PlanStep{{"a", PlanInProgress}, {"b", PlanInProgress}}}, false},
	} {
		if err := c.p.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// A turn in which the model calls update_plan emits the plan as an EventPlan
// for that step, keeps the call in the record, answers with an ack, and the
// tool remembers it. A second call replaces it whole.
func TestUpdatePlanEmitsThePlanAndKeepsItInTheRecord(t *testing.T) {
	tool := NewPlanTool()
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{planCall("p1", map[string]any{"plan": steps("read the code", "in_progress", "fix the loop", "pending")})}},
		StubTurn{ToolCalls: []ToolCall{planCall("p2", map[string]any{"plan": steps("read the code", "completed", "fix the loop", "in_progress"), "explanation": "found it"})}},
		StubTurn{Text: "done"},
	)
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: tool})
	if err != nil {
		t.Fatal(err)
	}
	var plans []Event
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "fix it"}}, func(e Event) {
		if e.Kind == EventPlan {
			plans = append(plans, e)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 || plans[0].Step != 1 || plans[1].Step != 2 || plans[1].Plan.Explanation != "found it" || plans[1].Plan.Steps[1].Status != PlanInProgress {
		t.Fatalf("plan events %+v", plans)
	}
	if got := res.Messages[1].Text; got != "plan updated: 0 of 2 steps done; now: read the code" {
		t.Errorf("ack %q", got)
	}
	if res.Messages[0].ToolCalls[0].Name != PlanToolName {
		t.Errorf("the call isn't in the record: %+v", res.Messages[0])
	}
	if l := tool.Latest(); l == nil || l.Steps[0].Status != PlanCompleted {
		t.Errorf("latest %+v", l)
	}
	raw, _ := json.Marshal(plans[0])
	if !strings.Contains(string(raw), `"kind":"plan"`) || !strings.Contains(string(raw), `"step":"read the code"`) {
		t.Errorf("the event doesn't serialize: %s", raw)
	}
}

// An invalid plan is an error result the model can act on, and nothing is
// emitted or kept.
func TestAnInvalidPlanIsAToolError(t *testing.T) {
	tool := NewPlanTool()
	var emitted int
	ctx := withToolEmit(context.Background(), func(Event) { emitted++ })
	res, err := tool.Call(ctx, PlanToolName, map[string]any{"plan": []any{
		map[string]any{"step": "a", "status": "in_progress"}, map[string]any{"step": "b", "status": "in_progress"},
	}})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].Text, "at most one") {
		t.Fatalf("%v %+v", err, res)
	}
	if res, _ := tool.Call(ctx, PlanToolName, map[string]any{"plan": "not a list"}); !res.IsError {
		t.Errorf("a plan that isn't a list was accepted")
	}
	if emitted != 0 || tool.Latest() != nil {
		t.Errorf("an invalid plan emitted %d events, latest %+v", emitted, tool.Latest())
	}
	if res, _ := tool.Call(ctx, PlanToolName, map[string]any{"plan": steps("a", "pending")}); res.IsError || emitted != 1 {
		t.Errorf("control: a valid plan: %+v, %d emitted", res, emitted)
	}
}

// The step stage adds the latest plan to a request only when no update_plan
// call is left in view, as after a compaction, and only for that request.
func TestThePlanStaysInViewAfterItsCallIsGone(t *testing.T) {
	tool := NewPlanTool()
	stage := tool.StepStage()
	view := StepView{Messages: []Message{{Role: RoleUser, Text: "summary of earlier work"}}}
	if got, _ := stage.Run(context.Background(), StepInfo{}, view); len(got.Messages) != 1 {
		t.Errorf("before any plan the stage added %d messages", len(got.Messages)-1)
	}
	tool.Call(context.Background(), PlanToolName, map[string]any{"plan": steps("read", "completed", "fix", "in_progress")})
	got, _ := stage.Run(context.Background(), StepInfo{}, view)
	if len(got.Messages) != 2 || got.Messages[1].Role != RoleSystem || !strings.Contains(got.Messages[1].Text, "[x] read\n[~] fix") {
		t.Errorf("after compaction: %+v", got.Messages)
	}
	if len(view.Messages) != 1 {
		t.Errorf("the stage changed the view it was given")
	}
	inView := StepView{Messages: append(view.Messages, Message{Role: RoleAssistant, ToolCalls: []ToolCall{planCall("p", nil)}})}
	if got, _ := stage.Run(context.Background(), StepInfo{}, inView); len(got.Messages) != 2 {
		t.Errorf("control: with the call in view the stage still added the plan")
	}
	if stage.Scope != ScopeRequest {
		t.Errorf("scope %v: the note must not join the record", stage.Scope)
	}
}
