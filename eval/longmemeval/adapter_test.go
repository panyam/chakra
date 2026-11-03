package longmemeval

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// TestSmokeAdapterYieldsEveryCase pins that the adapter maps the whole smoke
// set onto the harness, and that each case carries its own scorers. Under the
// previous suite shape neither was expressible, so this package ran its own
// loop.
func TestSmokeAdapterYieldsEveryCase(t *testing.T) {
	cases, err := SmokeAdapter{}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cases) != len(SmokeScenarios()) {
		t.Fatalf("adapter dropped cases: %d of %d", len(cases), len(SmokeScenarios()))
	}
	for _, c := range cases {
		if c.Scorers == nil {
			t.Fatalf("case %q has no scorers; a case nothing grades passes forever", c.Scenario.Name)
		}
	}
}

// TestSmokeAdapterNamesCarryCategory pins that the report groups by category
// without the harness knowing what a category is.
func TestSmokeAdapterNamesCarryCategory(t *testing.T) {
	cases, err := SmokeAdapter{}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, c := range cases {
		if !strings.Contains(c.Scenario.Name, "/") {
			t.Errorf("case name %q should be <category>/<name>", c.Scenario.Name)
		}
	}
}

// TestSmokeAdapterRunsThroughSuite feeds an adapter's cases straight into
// eval.Suite with no per-suite glue. It runs against a stub provider, so it
// asserts the plumbing rather than memory quality. The live, tagged benchmark
// measures memory quality.
func TestSmokeAdapterRunsThroughSuite(t *testing.T) {
	cases, err := SmokeAdapter{}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Over-provision the script. A compaction case runs under a
	// SummarizingCompactor, which calls the provider itself to summarize the
	// head, so the number of provider calls is not the number of turns.
	total := 0
	for _, c := range cases {
		total += len(c.Scenario.Turns)
	}
	turns := make([]agent.StubTurn, 0, total*4+20)
	for i := 0; i < cap(turns); i++ {
		turns = append(turns, agent.StubTurn{Text: "stub answer"})
	}

	suite := eval.Suite{
		Config: agent.RunnerConfig{Provider: agent.NewStubProvider(turns...)},
		Cases:  cases,
	}
	report := suite.Run(context.Background())

	if report.Total != len(cases) {
		t.Fatalf("suite ran %d of %d cases", report.Total, len(cases))
	}
	// No case may pass on a stub answer. This assertion caught the abstention
	// case passing vacuously. Its only deterministic scorer is a MustNot that
	// any answer satisfies, so without the rubric nothing graded it.
	// appendRubric now marks such a case ungradeable instead.
	if report.Passed != 0 {
		t.Errorf("a stub answer should satisfy no case's ground truth, got %d passes:\n%s", report.Passed, report)
	}
	for _, c := range report.Cases {
		if c.RunErr != "" {
			t.Errorf("case %q failed to run: %s", c.Case, c.RunErr)
		}
	}
}
