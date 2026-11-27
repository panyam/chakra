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

// A step's Driver runs that step's cases and every later step's, until a
// step brings another.
func TestStepDriverRunsItsStepAndLaterOnes(t *testing.T) {
	var ran []string
	driver := func(label string) Driver {
		return func(_ context.Context, _ agent.RunnerConfig, s Scenario) ([]Result, error) {
			ran = append(ran, label)
			return []Result{{Case: Case{Name: s.Name}, Turn: &agent.TurnResult{Text: label}}}, nil
		}
	}
	l := Ladder{
		Name:  "drive",
		Steps: []Step{{Name: "base"}, {Name: "+ a", Driver: driver("a")}, {Name: "+ more"}, {Name: "+ b", Driver: driver("b")}},
		Cases: []SuiteCase{Single(Case{Name: "c", Input: "go"}, Contains("b"))},
	}
	p := agent.NewStubProvider(agent.StubTurn{Text: "base"})
	r := l.Run(context.Background(), Suite{Config: agent.RunnerConfig{Provider: p}})
	if got := strings.Join(ran, ","); got != "a,a,b" {
		t.Errorf("drivers ran %q, want a,a,b (base on RunScenario, + more inheriting a)", got)
	}
	if r.Steps[3].Report.Passed != 1 || r.Steps[1].Report.Passed != 0 {
		t.Errorf("passes = %d, %d; want only + b to pass", r.Steps[1].Report.Passed, r.Steps[3].Report.Passed)
	}
}

// With repeats, each case runs that many times. The report counts passes
// per repeat and the cases that passed at least once, and the table shows
// the mean pass with the lowest and highest run.
func TestRepeatsRunEachCaseAndTheTableShowsTheSpread(t *testing.T) {
	// Run by run: a and b pass, then only b, then both again.
	answers := []string{"right", "right", "wrong", "right", "right", "right"}
	i := 0
	scripted := &replyProvider{reply: func(agent.ProviderRequest) agent.StubTurn {
		a := answers[i]
		i++
		return agent.StubTurn{Text: a}
	}}
	l := Ladder{
		Name:  "r",
		Steps: []Step{{Name: "only"}},
		Cases: []SuiteCase{
			Single(Case{Name: "a", Input: "q"}, ExactMatch("right")),
			Single(Case{Name: "b", Input: "q"}, ExactMatch("right")),
		},
	}
	r := l.Run(context.Background(), Suite{Config: agent.RunnerConfig{Provider: scripted}, Repeats: 3})
	rep := r.Steps[0].Report
	if rep.Total != 6 || rep.Passed != 5 || rep.Distinct() != 2 || rep.PassAtK() != 2 {
		t.Fatalf("total %d passed %d distinct %d pass@k %d, want 6, 5, 2, 2", rep.Total, rep.Passed, rep.Distinct(), rep.PassAtK())
	}
	if got := rep.PassesPerRun(); len(got) != 3 || got[0] != 2 || got[1] != 1 || got[2] != 2 {
		t.Errorf("passes per run = %v, want [2 1 2]", got)
	}
	if tbl := r.Table(); !strings.Contains(tbl, "r: 2 cases") || !strings.Contains(tbl, "1.7/2 (1-2)") {
		t.Errorf("table:\n%s\nwant 2 cases and a pass cell of 1.7/2 (1-2)", tbl)
	}
}

// sequence answers each call with the next of answers, in call order.
func sequence(answers ...string) *replyProvider {
	i := 0
	return &replyProvider{reply: func(agent.ProviderRequest) agent.StubTurn {
		a := answers[i%len(answers)]
		i++
		return agent.StubTurn{Text: a}
	}}
}

// threeCases is a ladder of three steps over three cases that pass on
// "right".
func threeCases() Ladder {
	return Ladder{
		Name:  "r",
		Steps: []Step{{Name: "one"}, {Name: "two"}, {Name: "three"}},
		Cases: []SuiteCase{
			Single(Case{Name: "a", Input: "q"}, ExactMatch("right")),
			Single(Case{Name: "b", Input: "q"}, ExactMatch("right")),
			Single(Case{Name: "c", Input: "q"}, ExactMatch("right")),
		},
	}
}

func TestAChangeWithinTheSpreadIsMarked(t *testing.T) {
	const r, w = "right", "wrong"
	// Passes per run: step one 0, 1, 0. Step two 1, 1, 0, which overlaps.
	// Step three 3, 3, 3, which doesn't.
	p := sequence(
		w, w, w, r, w, w, w, w, w,
		r, w, w, r, w, w, w, w, w,
		r, r, r, r, r, r, r, r, r)
	rep := threeCases().Run(context.Background(), Suite{Config: agent.RunnerConfig{Provider: p}, Repeats: 3})
	if got := []bool{rep.Steps[1].Resolved(), rep.Steps[2].Resolved()}; got[0] || !got[1] {
		t.Errorf("resolved = %v, want [false true]", got)
	}
	tbl := rep.Table()
	for _, want := range []string{"~+0.3", "+2.3", "~ this step's runs overlap the step before's"} {
		if !strings.Contains(tbl, want) {
			t.Errorf("table:\n%s\nwant %q", tbl, want)
		}
	}
	if strings.Contains(tbl, "~+2.3") {
		t.Errorf("a change outside the spread is marked:\n%s", tbl)
	}
}

func TestOneRunOfALiveModelMarksEveryChange(t *testing.T) {
	const r, w = "right", "wrong"
	answers := []string{w, w, w, r, w, w, r, r, r}
	live := threeCases().Run(context.Background(), Suite{Config: agent.RunnerConfig{Provider: sequence(answers...)}, Live: true})
	tbl := live.Table()
	for _, want := range []string{"~+1", "~+2", "~ one run of a live model shows no spread"} {
		if !strings.Contains(tbl, want) {
			t.Errorf("live table:\n%s\nwant %q", tbl, want)
		}
	}
	scripted := threeCases().Run(context.Background(), Suite{Config: agent.RunnerConfig{Provider: sequence(answers...)}})
	if tbl := scripted.Table(); strings.Contains(tbl, "~") {
		t.Errorf("a scripted model's single run is marked:\n%s", tbl)
	}
	if !scripted.Steps[1].Resolved() {
		t.Error("a scripted model's single run is unresolved")
	}
}
