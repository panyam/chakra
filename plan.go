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

// Plan is the checklist a model publishes with update_plan (#148). It has
// the steps in order, each with a status, and an optional explanation of
// what changed. It is sent on EventPlan, so it is JSON-tagged like every
// Event payload (A2).
type Plan struct {
	Steps       []PlanStep `json:"steps"`
	Explanation string     `json:"explanation,omitempty"`
}

// PlanStep is one step of a Plan.
type PlanStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

// Validate refuses a plan that a surface couldn't show sensibly. It refuses
// a plan with no steps, an empty step, or a status other than the three. It
// also refuses more than one step in progress. A plan with two current steps
// says nothing about which one the model is on.
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

// String is the plan as a checklist, one step per line. [x] marks completed,
// [~] in progress, and [ ] pending. The explanation comes first when there is
// one.
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

// PlanTool is the update_plan tool. It is a ToolSource with one tool. Each
// call takes the whole plan, replaces the previous one, and emits it as an
// EventPlan for surfaces. The call and its arguments are part of the record
// like any tool call, so a resumed session still has the plan in its
// history. One PlanTool serves one session.
type PlanTool struct {
	mu     sync.Mutex
	latest *Plan
	slots  *Slots
	slot   string
}

// NewPlanTool returns the tool with no plan yet.
func NewPlanTool() *PlanTool { return &PlanTool{} }

// WriteTo makes each accepted plan the value of the named slot too (#279), so
// the model sees the plan on every call through Slots.StepStage. StepStage
// then adds nothing, since the slot already shows the plan. The slot should
// be a replace slot. A plan too long for it is refused, and the model can
// send a shorter one. It returns t.
func (t *PlanTool) WriteTo(slots *Slots, name string) *PlanTool {
	t.mu.Lock()
	t.slots, t.slot = slots, name
	t.mu.Unlock()
	return t
}

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
		Description: "Publish a short checklist of your plan, for the user to follow, when a task has several distinct steps. Skip it when the task is a quick fix or you can see the whole job from where you are. " +
			"Update it only when a step's status changes, not after each action. Send the whole plan each time, with at most one step in_progress.",
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
// invalid plan gets an IsError result that the model can fix, and nothing is
// emitted.
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
	slots, slot := t.slots, t.slot
	t.mu.Unlock()
	if slots != nil {
		if _, err := slots.Write(ctx, slot, p.String()); err != nil {
			return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "plan not updated: " + err.Error()}}, IsError: true}, nil
		}
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

// StepStage keeps the latest plan in view. Imagine a compaction that
// summarizes away every update_plan call. The next model call would then see
// no plan. In that case the stage adds the plan as a note for that one
// request. It adds nothing while the call itself is still in view, or before
// the first plan.
func (t *PlanTool) StepStage() StepStage {
	return StepStage{
		Name:  "agent.plan",
		Scope: ScopeRequest,
		Run: func(_ context.Context, _ StepInfo, view StepView) (StepView, error) {
			t.mu.Lock()
			toSlot := t.slots != nil
			t.mu.Unlock()
			p := t.Latest()
			if p == nil || toSlot {
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

// toolEmitKey is the context key for a function in a tool call's context.
// The function emits an Event for the step that made the call. It is
// unexported, because only the plan tool and slot writes publish events of
// their own. A general way for any tool to put events on the turn's stream
// is a bigger decision than #148.
type toolEmitKey struct{}

func withToolEmit(ctx context.Context, emit func(Event)) context.Context {
	return context.WithValue(ctx, toolEmitKey{}, emit)
}

func toolEmitFrom(ctx context.Context) func(Event) {
	emit, _ := ctx.Value(toolEmitKey{}).(func(Event))
	return emit
}
