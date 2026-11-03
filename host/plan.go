package host

import "github.com/panyam/chakra"

// PlanExtension adds update_plan (#148): the model publishes a checklist of
// its steps, surfaces show it as an EventPlan, and a step stage keeps the
// latest plan in view after a compaction summarized its call away. Build one
// per session, since it holds that session's latest plan.
type PlanExtension struct {
	BaseExtension
	tool *agent.PlanTool
}

// NewPlanExtension returns the extension with no plan yet.
func NewPlanExtension() *PlanExtension { return &PlanExtension{tool: agent.NewPlanTool()} }

// Name is "plan".
func (e *PlanExtension) Name() string { return "plan" }

// Tools is update_plan.
func (e *PlanExtension) Tools() (agent.ToolSource, error) { return e.tool, nil }

// StepStages keeps the latest plan in view; see agent.PlanTool.StepStage.
func (e *PlanExtension) StepStages() []agent.StepStage {
	return []agent.StepStage{e.tool.StepStage()}
}

// Latest is the session's latest plan, nil before the first.
func (e *PlanExtension) Latest() *agent.Plan { return e.tool.Latest() }
