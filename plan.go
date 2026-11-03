package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// The statuses a PlanStep can have.
const (
	PlanPending    = "pending"
	PlanInProgress = "in_progress"
	PlanCompleted  = "completed"
)

// Plan is the checklist a model publishes with update_plan (#148): its
// steps in order, each with a status, and an optional explanation of what
// changed. It travels on EventPlan, so it is JSON-tagged like every Event
// payload (A2).
type Plan struct {
	Steps       []PlanStep `json:"steps"`
	Explanation string     `json:"explanation,omitempty"`
}

// PlanStep is one step of a Plan.
type PlanStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

// Validate refuses a plan a surface couldn't show sensibly: no steps, an
// empty step, a status other than the three, or more than one step in
// progress, since a plan with two current steps says nothing about which
// one the model is on.
func (p Plan) Validate() error {
	if len(p.Steps) == 0 {
		return fmt.Errorf("a plan needs at least one step")
	}
	inProgress := 0
	for i, s := range p.Steps {
		if strings.TrimSpace(s.Step) == "" {
			return fmt.Errorf("step %d is empty", i+1)
		}
		switch s.Status {
		case PlanPending, PlanCompleted:
		case PlanInProgress:
			inProgress++
		default:
			return fmt.Errorf("step %d's status is %q; use %s, %s or %s", i+1, s.Status, PlanPending, PlanInProgress, PlanCompleted)
		}
	}
	if inProgress > 1 {
		return fmt.Errorf("%d steps are in_progress; at most one can be", inProgress)
	}
	return nil
}

// String is the plan as a checklist, one step per line: [x] completed, [~]
// in progress, [ ] pending, with the explanation first when there is one.
func (p Plan) String() string {
	var b strings.Builder
	if p.Explanation != "" {
		b.WriteString(p.Explanation + "\n")
	}
	for _, s := range p.Steps {
		mark := "[ ]"
		switch s.Status {
		case PlanCompleted:
			mark = "[x]"
		case PlanInProgress:
			mark = "[~]"
		}
		fmt.Fprintf(&b, "%s %s\n", mark, s.Step)
	}
	return b.String()
}

// PlanToolName is the tool's name, the one codex uses.
const PlanToolName = "update_plan"

// PlanTool is the update_plan tool: a ToolSource whose one tool takes the
// whole plan each call, replaces the previous one, and emits it as an
// EventPlan for surfaces. The call and its arguments are part of the record
// like any tool call, so a resumed session still has the plan in its
// history. One PlanTool serves one session.
type PlanTool struct {
	mu     sync.Mutex
	latest *Plan
}

// NewPlanTool returns the tool with no plan yet.
func NewPlanTool() *PlanTool { return &PlanTool{} }

// Latest is the last plan update_plan accepted, nil before the first.
func (t *PlanTool) Latest() *Plan {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.latest == nil {
		return nil
	}
	p := *t.latest
	p.Steps = append([]PlanStep(nil), t.latest.Steps...)
	return &p
}

// Tools lists update_plan.
func (t *PlanTool) Tools(context.Context) ([]core.ToolDef, error) {
	return []core.ToolDef{{
		Name:        PlanToolName,
		Title:       "Update the plan",
		Annotations: map[string]any{"readOnlyHint": true},
		Description: "Publish your plan for a task with several steps, as a short checklist the user can follow, and update it as you go: mark a step in_progress when you start it and completed when it's done, with at most one in_progress. Send the whole plan each time. Skip it for a task you can finish in one or two steps.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plan": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"step":   map[string]any{"type": "string", "description": "What the step does, in a few words."},
							"status": map[string]any{"type": "string", "enum": []string{PlanPending, PlanInProgress, PlanCompleted}},
						},
						"required":             []string{"step", "status"},
						"additionalProperties": false,
					},
				},
				"explanation": map[string]any{"type": "string", "description": "What changed since the last plan, if anything did."},
			},
			"required":             []string{"plan"},
			"additionalProperties": false,
		},
	}}, nil
}

// Call validates the plan, keeps it as the latest, emits it as an EventPlan
// for the step that called it, and answers with a short acknowledgement. An
// invalid plan is an IsError result the model can fix; nothing is emitted.
func (t *PlanTool) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if name != PlanToolName {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	p, err := planFromArgs(args)
	if err == nil {
		err = p.Validate()
	}
	if err != nil {
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "plan not updated: " + err.Error()}}, IsError: true}, nil
	}
	t.mu.Lock()
	t.latest = &p
	t.mu.Unlock()
	if emit := toolEmitFrom(ctx); emit != nil {
		cp := p
		emit(Event{Kind: EventPlan, Plan: &cp})
	}
	done, current := 0, ""
	for _, s := range p.Steps {
		switch s.Status {
		case PlanCompleted:
			done++
		case PlanInProgress:
			current = s.Step
		}
	}
	ack := fmt.Sprintf("plan updated: %d of %d steps done", done, len(p.Steps))
	if current != "" {
		ack += "; now: " + current
	}
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: ack}}}, nil
}

func planFromArgs(args map[string]any) (Plan, error) {
	raw, err := json.Marshal(map[string]any{"steps": args["plan"], "explanation": args["explanation"]})
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return Plan{}, fmt.Errorf("the plan isn't a list of {step, status}: %w", err)
	}
	return p, nil
}

// StepStage keeps the latest plan in view: when no update_plan call is left
// in the messages a model call will see, because a compaction summarized it
// away, it adds the plan as a note for that one request. It adds nothing
// while the call itself is still in view, or before the first plan.
func (t *PlanTool) StepStage() StepStage {
	return StepStage{
		Name:  "agent.plan",
		Scope: ScopeRequest,
		Run: func(_ context.Context, _ StepInfo, view StepView) (StepView, error) {
			p := t.Latest()
			if p == nil {
				return view, nil
			}
			for _, m := range view.Messages {
				for _, c := range m.ToolCalls {
					if c.Name == PlanToolName {
						return view, nil
					}
				}
			}
			note := Message{Role: RoleSystem, Text: "Your current plan (from update_plan):\n" + p.String()}
			view.Messages = append(append([]Message(nil), view.Messages...), note)
			return view, nil
		},
	}
}

// toolEmitKey carries, in a tool call's context, the function that emits an
// Event for the step that made the call. Unexported: the plan tool is the
// one tool that publishes an event of its own, and a general way for any
// tool to put events on the turn's stream is a bigger decision than #148.
type toolEmitKey struct{}

func withToolEmit(ctx context.Context, emit func(Event)) context.Context {
	return context.WithValue(ctx, toolEmitKey{}, emit)
}

func toolEmitFrom(ctx context.Context) func(Event) {
	emit, _ := ctx.Value(toolEmitKey{}).(func(Event))
	return emit
}
