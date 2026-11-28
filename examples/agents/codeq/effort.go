package codeq

import (
	"context"
	"fmt"
	"slices"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// effortStageName names the stage each effort step installs, so the next
// step can take it out.
const effortStageName = "codeq.effort"

// effortPolicy picks one model call's effort from the turn so far. Empty
// leaves the connection's own setting.
type effortPolicy func(agent.StepInfo) string

// fixed asks for the same effort on every call.
func fixed(e string) effortPolicy { return func(agent.StepInfo) string { return e } }

// highOnRepair asks for low effort until the answer check has refused an
// answer in this turn, and high on every call after that. A first draft
// needs little thought, and a repair needs more (#375).
func highOnRepair(info agent.StepInfo) string {
	for _, m := range info.Turn {
		if m.Text == rowsOnly {
			return agent.EffortHigh
		}
	}
	return agent.EffortLow
}

// effortStage sets each call's effort from p. It is a request stage, since
// an effort is a choice about one call and joins no record.
func effortStage(p effortPolicy) agent.StepStage {
	return agent.StepStage{
		Name:  effortStageName,
		Scope: agent.ScopeRequest,
		Run: func(_ context.Context, info agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			v.Generation.Effort = p(info)
			return v, nil
		},
	}
}

// withEffort returns cfg with p's stage in place of any earlier effort
// stage.
func withEffort(cfg agent.RunnerConfig, p effortPolicy) agent.RunnerConfig {
	stages := slices.DeleteFunc(slices.Clone(cfg.StepStages), func(s agent.StepStage) bool { return s.Name == effortStageName })
	cfg.StepStages = append(stages, effortStage(p))
	return cfg
}

// effortLevels are the levels the effort ladder asks for. The lowest is
// "minimal" rather than agent.EffortNone, since Gemini 3.5 Flash-Lite
// refuses none and takes minimal, as OpenAI's reasoning models do. Its first step
// checks the connection sends them all, so a connection that can't fails
// with one error rather than one per case.
var effortLevels = []string{"minimal", agent.EffortLow, agent.EffortHigh}

// EffortLadder runs codeq's full agent, every step of Ladder, under one
// effort policy per step (#375). The first step keeps the connection's own
// effort. Each later step replaces the policy before it, so a row compares
// two policies rather than adding one. Run it with make exercise
// LADDER=effort, on a connection whose provider sends minimal, low and
// high, such as a gemini one.
func EffortLadder(ctx context.Context) (eval.Ladder, func(), error) {
	l, stop, err := Ladder(ctx)
	if err != nil {
		return eval.Ladder{}, nil, err
	}
	agentSteps := l.Steps
	full := func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
		if missing := slices.DeleteFunc(slices.Clone(effortLevels), func(e string) bool {
			return slices.Contains(agent.CapabilitiesOf(cfg.Provider).Effort, e)
		}); len(missing) > 0 {
			return cfg, fmt.Errorf("the effort ladder asks for %s, and this connection's provider doesn't send %s", strings.Join(effortLevels, ", "), strings.Join(missing, ", "))
		}
		for _, s := range agentSteps {
			if s.Configure == nil {
				continue
			}
			next, err := s.Configure(cfg)
			if err != nil {
				return cfg, fmt.Errorf("%s: %w", s.Name, err)
			}
			cfg = next
		}
		return cfg, nil
	}
	policy := func(p effortPolicy) func(agent.RunnerConfig) (agent.RunnerConfig, error) {
		return func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) { return withEffort(cfg, p), nil }
	}
	l.Name = "codeq effort"
	l.Steps = []eval.Step{
		{Name: "connection's effort", Configure: full},
		{Name: "effort minimal", Configure: policy(fixed("minimal"))},
		{Name: "effort low", Configure: policy(fixed(agent.EffortLow))},
		{Name: "effort high", Configure: policy(fixed(agent.EffortHigh))},
		{Name: "high on repair", Configure: policy(highOnRepair)},
	}
	return l, stop, nil
}
