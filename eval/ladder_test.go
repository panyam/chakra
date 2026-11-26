package eval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/chakra"
)

// replyProvider answers each call with what reply returns for the request,
// so a test's model can react to the config a ladder step built.
type replyProvider struct {
	reply func(agent.ProviderRequest) agent.StubTurn
	seen  []string
}

func (p *replyProvider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	p.seen = append(p.seen, req.Instructions)
	return agent.NewStubProvider(p.reply(req)).Stream(ctx, req)
}

func (p *replyProvider) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	p.seen = append(p.seen, req.Instructions)
	return agent.NewStubProvider(p.reply(req)).Generate(ctx, req)
}

func echoInstructions(in, out int) *replyProvider {
	return &replyProvider{reply: func(req agent.ProviderRequest) agent.StubTurn {
		return usageTurn("said "+req.Instructions, in, out)
	}}
}

func appendInstructions(s string) Step {
	return Step{Name: "+ " + s, Configure: func(c agent.RunnerConfig) (agent.RunnerConfig, error) {
		c.Instructions += s
		return c, nil
	}}
}

// Each step runs on the config the steps before it built, and the report
// counts what the step added over the one before.
func TestLadderStepsBuildOnEachOther(t *testing.T) {
	p := echoInstructions(0, 0)
	l := Ladder{
		Name:  "spell",
		Steps: []Step{{Name: "base"}, appendInstructions("a"), appendInstructions("b")},
		Cases: []SuiteCase{
			Single(Case{Name: "ab", Input: "go"}, Contains("said ab")),
			Single(Case{Name: "a", Input: "go"}, Contains("said a")),
		},
	}
	r := l.Run(context.Background(), Suite{Config: agent.RunnerConfig{Provider: p}})

	if got := strings.Join(p.seen, ","); got != ",,a,a,ab,ab" {
		t.Errorf("instructions per call = %q, want each step building on the last", got)
	}
	if len(r.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(r.Steps))
	}
	for i, want := range []struct {
		name   string
		passed int
		delta  int
	}{{"base", 0, 0}, {"+ a", 1, 1}, {"+ b", 2, 1}} {
		s := r.Steps[i]
		if s.Step != want.name || s.Report.Passed != want.passed || s.Delta() != want.delta {
			t.Errorf("step %d = %s passed %d delta %d, want %s passed %d delta %d", i, s.Step, s.Report.Passed, s.Delta(), want.name, want.passed, want.delta)
		}
	}
}

// A step whose Configure fails reports the error, and the steps after it
// don't run, since they would build on a config that doesn't exist.
func TestLadderConfigureErrorStopsTheClimb(t *testing.T) {
	p := echoInstructions(0, 0)
	l := Ladder{
		Name: "broken",
		Steps: []Step{
			{Name: "base"},
			{Name: "boom", Configure: func(c agent.RunnerConfig) (agent.RunnerConfig, error) { return c, errors.New("no tool server") }},
			appendInstructions("x"),
		},
		Cases: []SuiteCase{Single(Case{Name: "c", Input: "go"}, NoError())},
	}
	r := l.Run(context.Background(), Suite{Config: agent.RunnerConfig{Provider: p}})
	if len(r.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(r.Steps))
	}
	if r.Steps[0].Err != "" || r.Steps[0].Report.Passed != 1 {
		t.Errorf("base = %+v, want a clean pass", r.Steps[0])
	}
	if !strings.Contains(r.Steps[1].Err, "no tool server") {
		t.Errorf("boom Err = %q, want the configure error", r.Steps[1].Err)
	}
	if r.Steps[2].Err == "" {
		t.Errorf("the step after a failed configure ran: %+v", r.Steps[2])
	}
	if len(p.seen) != 1 {
		t.Errorf("model calls = %d, want only the base step's", len(p.seen))
	}
}

// Dollars and seconds are per correct answer, so a step that passes more
// for the same spend reads cheaper. A step with no prices reads as unknown,
// not free.
func TestLadderTableCostAndTimePerCorrect(t *testing.T) {
	p := echoInstructions(1_000_000, 0)
	l := Ladder{
		Name:  "spell",
		Steps: []Step{{Name: "base"}, appendInstructions("a")},
		Cases: []SuiteCase{
			Single(Case{Name: "a1", Input: "go"}, Contains("said a")),
			Single(Case{Name: "a2", Input: "go"}, Contains("said a")),
		},
	}
	priced := l.Run(context.Background(), Suite{Config: agent.RunnerConfig{Provider: p}, ModelName: "m", Prices: Prices{"m": {Input: 2}}})
	if d, ok := priced.Steps[1].Report.CostPerCorrect(); !ok || d != 2 {
		t.Errorf("+ a: CostPerCorrect = %v, %v; want 2 (two cases at $2, two correct)", d, ok)
	}
	if _, ok := priced.Steps[1].Report.TimePerCorrect(); !ok {
		t.Errorf("+ a: TimePerCorrect unknown with two correct")
	}
	if _, ok := priced.Steps[0].Report.TimePerCorrect(); ok {
		t.Errorf("base: TimePerCorrect known with nothing correct")
	}
	table := priced.Table()
	for _, want := range []string{"spell", "base", "0/2", "+ a", "2/2", "+2", "$2.0000"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}

	unpriced := l.Run(context.Background(), Suite{Config: agent.RunnerConfig{Provider: p}, ModelName: "m"})
	if strings.Contains(unpriced.Table(), "$") {
		t.Errorf("an unpriced ladder shows dollars:\n%s", unpriced.Table())
	}
}

// A cheap model's dollars per correct keeps two significant figures, so a
// Haiku row doesn't read $0.0000.
func TestLadderDollarsKeepTwoSignificantFigures(t *testing.T) {
	for _, c := range []struct {
		d    float64
		want string
	}{
		{2, "$2.0000"},
		{0.0003, "$0.00030"},
		{0.0000123, "$0.000012"},
		{0, "$0.0000"},
	} {
		if got := dollars(c.d); got != c.want {
			t.Errorf("dollars(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// A step that thought shows its reasoning tokens per correct answer. A
// ladder where no step thought keeps the table it had.
func TestLadderTableShowsReasoningOnlyWhenAStepThought(t *testing.T) {
	step := func(name string, passed, reasoning int) StepReport {
		return StepReport{Step: name, Report: SuiteReport{Passed: passed, Total: 2, Cases: []CaseReport{
			{Usage: map[string]agent.Usage{"m": {InputTokens: 10, OutputTokens: 900, ReasoningTokens: reasoning}}},
		}}}
	}
	thought := LadderReport{Ladder: "think", Steps: []StepReport{step("off", 1, 0), step("+ thinking", 2, 800)}}
	if n, ok := thought.Steps[1].Report.ReasoningPerCorrect(); !ok || n != 400 {
		t.Errorf("+ thinking: ReasoningPerCorrect = %d, %v; want 400 (800 over two correct)", n, ok)
	}
	table := thought.Table()
	for _, want := range []string{"reasoning/correct", "400"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	plain := LadderReport{Ladder: "plain", Steps: []StepReport{step("off", 1, 0), step("on", 2, 0)}}
	if strings.Contains(plain.Table(), "reasoning") {
		t.Errorf("a ladder with no thinking shows a reasoning column:\n%s", plain.Table())
	}
}

// The suite's per-case sum keeps every count, so a ladder built from real
// turns sees reasoning tokens, and cached tokens too.
func TestSuiteUsageSumKeepsEveryCount(t *testing.T) {
	u := agent.Usage{InputTokens: 100, OutputTokens: 50, CachedInputTokens: 80, ReasoningTokens: 30}
	got := Suite{ModelName: "m"}.addUsage(map[string]agent.Usage{"m": u}, map[string]agent.Usage{"": u})
	if want := u.Add(u); got["m"] != want {
		t.Errorf("sum = %+v, want %+v", got["m"], want)
	}
}
