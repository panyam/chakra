package eval

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// usageTurn scripts a text reply that reports token usage.
func usageTurn(text string, in, out int) agent.StubTurn {
	return agent.StubTurn{Deltas: []agent.Delta{
		{Kind: agent.DeltaText, Text: text},
		{Kind: agent.DeltaUsage, Usage: &agent.Usage{InputTokens: in, OutputTokens: out}},
		{Kind: agent.DeltaFinish, FinishReason: "stop"},
	}}
}

// usageToolTurn scripts a tool call that reports token usage.
func usageToolTurn(id, name, args string, in, out int) agent.StubTurn {
	return agent.StubTurn{Deltas: []agent.Delta{
		{Kind: agent.DeltaToolCallStart, ToolCallID: id, ToolName: name, Text: string(core.NewRawJSON(json.RawMessage(args)).Raw())},
		{Kind: agent.DeltaUsage, Usage: &agent.Usage{InputTokens: in, OutputTokens: out}},
		{Kind: agent.DeltaFinish, FinishReason: "tool_calls"},
	}}
}

func TestSuiteSetupSeedsToolsAndInstructions(t *testing.T) {
	stub := agent.NewStubProvider(
		usageToolTurn("c1", "lookup", `{"key":"x"}`, 200, 0),
		usageTurn("value-for-x", 300, 100),
	)
	calls := 0
	suite := Suite{
		Config:    agent.RunnerConfig{Provider: stub, Instructions: "base"},
		ModelName: "answerer",
		Cases: []SuiteCase{{
			Scenario: Scenario{Name: "armed", Turns: []string{"get x"}},
			Setup: func(ctx context.Context) (Setup, error) {
				calls++
				time.Sleep(time.Millisecond)
				return Setup{
					Tools:        lookupSource(t),
					Instructions: "arm instructions",
					Usage:        map[string]agent.Usage{"extractor": {InputTokens: 1000, OutputTokens: 50}},
				}, nil
			},
			Scorers: func(agent.Provider) []Scorer { return []Scorer{ExactMatch("value-for-x"), ToolCalled("lookup")} },
		}},
	}

	report := suite.Run(context.Background())
	if calls != 1 {
		t.Fatalf("setup ran %d times, want 1", calls)
	}
	c := report.Cases[0]
	if !c.Pass {
		t.Fatalf("case should pass with the setup's tool: %+v", c)
	}
	if got := stub.Requests()[0].Instructions; got != "arm instructions" {
		t.Errorf("instructions = %q, want the setup's", got)
	}
	if got := c.SetupUsage["extractor"]; got.InputTokens != 1000 || got.OutputTokens != 50 {
		t.Errorf("setup usage = %+v", c.SetupUsage)
	}
	if got := c.Usage["answerer"]; got.InputTokens != 500 || got.OutputTokens != 100 {
		t.Errorf("question usage = %+v, want both steps summed under the suite's model name", c.Usage)
	}
	if c.SetupElapsed < time.Millisecond || c.Elapsed <= 0 {
		t.Errorf("elapsed = %v, setup elapsed = %v", c.Elapsed, c.SetupElapsed)
	}
	if c.Cost != nil {
		t.Errorf("cost = %v without prices, want nil", *c.Cost)
	}
}

func TestSuiteSetupErrorIsPerCase(t *testing.T) {
	suite := Suite{
		Config: agent.RunnerConfig{Provider: agent.NewStubProvider(agent.StubTurn{Text: "ok"})},
		Cases: []SuiteCase{
			{
				Scenario: Scenario{Name: "broken", Turns: []string{"q"}},
				Setup: func(context.Context) (Setup, error) {
					return Setup{Usage: map[string]agent.Usage{"extractor": {InputTokens: 7}}}, errors.New("ingest failed")
				},
				Scorers: func(agent.Provider) []Scorer { return []Scorer{NoError()} },
			},
			Single(Case{Name: "fine", Input: "q"}, NoError()),
		},
	}
	report := suite.Run(context.Background())
	if report.Passed != 1 || report.Failed != 1 {
		t.Fatalf("aggregate = %+v", report)
	}
	broken := report.Cases[0]
	if !strings.Contains(broken.RunErr, "setup: ingest failed") {
		t.Errorf("run error = %q", broken.RunErr)
	}
	if broken.SetupUsage["extractor"].InputTokens != 7 {
		t.Errorf("a failed setup should still report what it spent: %+v", broken.SetupUsage)
	}
}

// costSuite runs two cases. Each one ingests on "extractor" and answers on
// "answerer". Case A passes and case B answers wrong.
func costSuite(prices Prices) SuiteReport {
	setup := func(in, out int) func(context.Context) (Setup, error) {
		return func(context.Context) (Setup, error) {
			return Setup{Usage: map[string]agent.Usage{"extractor": {InputTokens: in, OutputTokens: out}}}, nil
		}
	}
	scorers := func(agent.Provider) []Scorer { return []Scorer{ExactMatch("right"), NoError()} }
	return Suite{
		Config: agent.RunnerConfig{Provider: agent.NewStubProvider(
			usageTurn("right", 500_000, 100_000),
			usageTurn("wrong", 1_000_000, 0),
		)},
		ModelName: "answerer",
		Prices:    prices,
		Cases: []SuiteCase{
			{Scenario: Scenario{Name: "A", Turns: []string{"q"}}, Setup: setup(1_000_000, 500_000), Scorers: scorers},
			{Scenario: Scenario{Name: "B", Turns: []string{"q"}}, Setup: setup(1_000_000, 0), Scorers: scorers},
		},
	}.Run(context.Background())
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSuiteCostPerCorrectAnswer(t *testing.T) {
	report := costSuite(Prices{
		"extractor": {InputPerM: 1, OutputPerM: 2},
		"answerer":  {InputPerM: 3, OutputPerM: 15},
	})
	// A: setup 1M in + 0.5M out on extractor = 1 + 1, and the question
	// 0.5M in + 0.1M out on answerer = 1.5 + 1.5, so 5 in all.
	// B: setup 1M in = 1, and the question 1M in = 3, so 4 in all.
	want := map[string]float64{"A": 5, "B": 4}
	for _, c := range report.Cases {
		if c.Cost == nil || !near(*c.Cost, want[c.Case]) {
			t.Errorf("case %s cost = %v, want %v", c.Case, c.Cost, want[c.Case])
		}
	}
	if got, ok := report.CostPerCorrect(); !ok || !near(got, 9) {
		t.Errorf("cost per correct = %v, %v; want 9 (both cases' cost over one pass)", got, ok)
	}
	dims := map[string]DimensionReport{}
	for _, d := range report.Dimensions() {
		dims[d.Name] = d
	}
	if got, ok := dims["ExactMatch"].CostPerPass(); !ok || !near(got, 9) {
		t.Errorf("ExactMatch cost per pass = %v, %v; want 9", got, ok)
	}
	if got, ok := dims["NoError"].CostPerPass(); !ok || !near(got, 4.5) {
		t.Errorf("NoError cost per pass = %v, %v; want 4.5", got, ok)
	}
	if !strings.Contains(report.String(), "$9.0000 per correct answer") {
		t.Errorf("report doesn't give the cost per correct answer:\n%s", report)
	}
}

func TestSuiteWithoutPricesReportsTokensOnly(t *testing.T) {
	for name, prices := range map[string]Prices{
		"no prices":        nil,
		"one model priced": {"answerer": {InputPerM: 3, OutputPerM: 15}},
	} {
		report := costSuite(prices)
		for _, c := range report.Cases {
			if c.Cost != nil {
				t.Errorf("%s: case %s cost = %v, want nil", name, c.Case, *c.Cost)
			}
			if c.Usage["answerer"].InputTokens == 0 || c.SetupUsage["extractor"].InputTokens == 0 {
				t.Errorf("%s: case %s lost its tokens: %+v %+v", name, c.Case, c.Usage, c.SetupUsage)
			}
		}
		if _, ok := report.CostPerCorrect(); ok {
			t.Errorf("%s: cost per correct should be unknown", name)
		}
		for _, d := range report.Dimensions() {
			if _, ok := d.CostPerPass(); ok {
				t.Errorf("%s: %s cost per pass should be unknown", name, d.Name)
			}
		}
		if strings.Contains(report.String(), "$") {
			t.Errorf("%s: report prints a cost:\n%s", name, report)
		}
	}
}

func TestDimensionCostCountsEachCaseOnce(t *testing.T) {
	two := 2.0
	report := SuiteReport{Cases: []CaseReport{{
		Case: "a", Pass: true, Cost: &two,
		Scores: []Score{{Name: "Contains", Pass: true}, {Name: "Contains", Pass: true}},
	}}}
	d := report.Dimensions()[0]
	if d.Cost == nil || *d.Cost != 2 {
		t.Fatalf("dimension cost = %+v, want 2", d)
	}
}
