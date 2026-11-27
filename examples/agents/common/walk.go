package common

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/demokit"
)

// Walker builds a demokit walk through one agent's ladder on one model. It
// holds the config the ladder has built so far and the steps run so far, so
// each demokit step picks up where the last one stopped. Its methods are
// the default pieces of a walk: Intro, Steps (or Step for one), and Table.
// An Agent.Walk calls them in any order and adds its own sections and
// steps between them.
type Walker struct {
	a       Agent
	m       Model
	ladder  eval.Ladder
	cfg     agent.RunnerConfig
	driver  eval.Driver
	applied []bool
	failed  string
	reports []eval.StepReport
	passed  int
	spent   float64
	priced  bool
}

// Walk builds the walk through a's ladder on m. It starts the ladder, so a
// tool server is running by the time it returns, and stop releases it.
// Run the demo with demokit's Execute, or harness.Run for its modes.
func Walk(ctx context.Context, a Agent, m Model) (d *demokit.Demo, stop func(), err error) {
	l, stop, err := a.Ladder(ctx)
	if err != nil {
		return nil, nil, err
	}
	steps := a.MaxSteps
	if steps == 0 {
		steps = 8
	}
	w := &Walker{
		a:       a,
		m:       m,
		ladder:  l,
		cfg:     agent.RunnerConfig{Provider: m.Provider, Instructions: a.Instructions, MaxSteps: steps},
		applied: make([]bool, len(l.Steps)),
		priced:  m.Prices != nil,
	}
	d = demokit.New(fmt.Sprintf("%s on %s", a.Name, m.Name)).
		Description(fmt.Sprintf("A walk through %s's ladder, one step at a time: what each step adds to the agent, what it passes on %d cases, and what it costs.", a.Name, len(l.Cases)))
	layout := a.Walk
	if layout == nil {
		layout = DefaultWalk
	}
	layout(d, w)
	return d, stop, nil
}

// DefaultWalk is the walk an agent gets when Agent.Walk is nil: Intro, a
// demokit step per ladder step, then the table.
func DefaultWalk(d *demokit.Demo, w *Walker) {
	w.Intro(d)
	w.Steps(d)
	w.Table(d)
}

// Intro adds the opening section: the agent's About, or a generic one, and
// the steps to come.
func (w *Walker) Intro(d *demokit.Demo) {
	lines := slices.Clone(w.a.About)
	if len(lines) == 0 {
		lines = []string{fmt.Sprintf("%s answers %d cases. Each step below adds one thing to the agent, and runs the same cases again.", w.a.Name, len(w.ladder.Cases))}
	}
	lines = append(lines, "", "The steps:")
	for i, s := range w.ladder.Steps {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, s.Name))
	}
	if !w.priced {
		lines = append(lines, "", fmt.Sprintf("%s has no price, so the walk can't say what a step costs.", w.m.Name))
	}
	d.Section("The job", lines...)
}

// Steps adds a demokit step for every ladder step, in order.
func (w *Walker) Steps(d *demokit.Demo) {
	for i := range w.ladder.Steps {
		w.Step(d, i)
	}
}

// Step adds the demokit step for ladder step i. It asks one question:
// Enter runs every case, and a case number watches that one. Either way it
// first applies the step, and every step before it that hasn't been
// applied, and prints what changed. Running every case prints a line per
// case, the step's row and what was spent. Watching one prints that case's
// transcript and its scores.
func (w *Walker) Step(d *demokit.Demo, i int) {
	s := w.ladder.Steps[i]
	n := len(w.ladder.Cases)
	step := d.Step(s.Name).ID(fmt.Sprintf("step-%d", i+1)).
		Input(demokit.String().
			Named("watch", fmt.Sprintf("Enter runs every case, or a case number from 1 to %d watches it", n)).
			WithDefault("all").
			WithParse(func(in string) (any, error) {
				in = strings.TrimSpace(in)
				if in == "" || in == "all" || in == "run" {
					return "all", nil
				}
				c, err := strconv.Atoi(in)
				if err != nil || c < 1 || c > n {
					return nil, fmt.Errorf("press Enter, or type a case number from 1 to %d", n)
				}
				return c, nil
			}))
	if note := w.a.StepNotes[s.Name]; note != "" {
		step.Note(note)
	}
	step.Run(func(ctx demokit.StepContext) *demokit.StepResult {
		if !w.applyThrough(i) {
			return demokit.Errf("%s", w.failed)
		}
		if c := caseNumber(ctx.Inputs["watch"]); c > 0 {
			return w.watch(ctx.Ctx, c)
		}
		w.runAll(ctx.Ctx, i)
		return nil
	})
}

// caseNumber reads the watch input: a case number, or 0 to run every case.
// A live prompt parses a number as an int, a recorded trace decodes it
// from JSON as a float64, and "all", or no input at all, means run.
func caseNumber(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// Table adds the final step, which prints the table of every step that ran
// all its cases.
func (w *Walker) Table(d *demokit.Demo) {
	d.Step("The table").ID("table").Run(func(demokit.StepContext) *demokit.StepResult {
		if len(w.reports) == 0 {
			fmt.Println("No step ran every case, so there's no table.")
			return nil
		}
		fmt.Print(eval.LadderReport{Ladder: w.ladder.Name, Steps: w.reports}.Table())
		return nil
	})
}

// applyThrough applies every step up to and including i that hasn't been
// applied, printing what each one changed. It reports false once a step's
// Configure has failed, since every later step builds on it.
func (w *Walker) applyThrough(i int) bool {
	for j := 0; j <= i; j++ {
		if w.failed != "" {
			return false
		}
		if w.applied[j] {
			continue
		}
		s := w.ladder.Steps[j]
		next := w.cfg
		if s.Configure != nil {
			var err error
			if next, err = s.Configure(w.cfg); err != nil {
				w.failed = fmt.Sprintf("step %q could not be applied: %v", s.Name, err)
				return false
			}
		}
		if j < i {
			fmt.Printf("Applying %q first, without running its cases.\n", s.Name)
		}
		for _, line := range changes(w.cfg, next) {
			fmt.Println("  " + line)
		}
		if s.Driver != nil {
			fmt.Println("  runs each case its own way from here (Driver)")
			w.driver = s.Driver
		}
		w.cfg = next
		w.applied[j] = true
	}
	return true
}

// runAll runs every case on the config built through step i and records
// the step's row.
func (w *Walker) runAll(ctx context.Context, i int) {
	fmt.Printf("\nRunning %d cases:\n", len(w.ladder.Cases))
	suite := eval.Suite{Config: w.cfg, Cases: w.ladder.Cases, ModelName: w.m.Name, Prices: w.m.Prices, Driver: w.driver, Live: w.m.Live,
		OnCase: func(_ int, r eval.CaseReport) { fmt.Println(caseLine(r)) }}
	rep := suite.Run(ctx)
	sr := eval.StepReport{Step: w.ladder.Steps[i].Name, Report: rep, Before: w.passed}
	if len(w.reports) == 0 {
		sr.Before = rep.Passed
	} else {
		sr.BeforeRuns = w.reports[len(w.reports)-1].Report.PassesPerRun()
	}
	w.reports = append(w.reports, sr)
	w.passed = rep.Passed

	summary := fmt.Sprintf("\n%s passed %d of %d", sr.Step, rep.Passed, rep.Total)
	if len(w.reports) > 1 {
		summary += fmt.Sprintf(", %+d on the step before", sr.Delta())
		if !sr.Resolved() {
			summary += ". That's one run of a live model, so the change may be noise"
		}
	}
	fmt.Println(summary + ".")
	cost, ok := rep.Cost()
	if !w.priced || !ok {
		return
	}
	w.spent += cost
	fmt.Printf("This step cost $%.5f, and the walk has spent $%.5f.\n", cost, w.spent)
	if i+1 < len(w.ladder.Steps) {
		fmt.Printf("The next step runs the same %d cases, so expect about $%.5f more.\n", rep.Total, cost)
	}
}

// watch runs case n, counting from one, on the config built so far, and
// prints its transcript and scores. The step's row isn't recorded, since
// only one case ran.
func (w *Walker) watch(ctx context.Context, n int) *demokit.StepResult {
	if n < 1 || n > len(w.ladder.Cases) {
		return demokit.Errf("no case %d; pick 1 to %d", n, len(w.ladder.Cases))
	}
	sc := w.ladder.Cases[n-1]
	fmt.Printf("\nWatching case %d: %s\n\n", n, sc.Scenario.Name)
	cfg, scenario, _, err := sc.Prepare(ctx, w.cfg)
	if err != nil {
		return demokit.Err(err)
	}
	drive := w.driver
	if drive == nil {
		drive = eval.RunScenario
	}
	results, err := drive(ctx, cfg, scenario)
	if err != nil {
		return demokit.Err(err)
	}
	if len(results) == 0 {
		return demokit.Errf("case %d ran no turns", n)
	}
	final := eval.Final(results)
	fmt.Println(eval.Transcript(final))
	if sc.Scorers == nil {
		return nil
	}
	for _, s := range sc.Scorers(cfg.Provider) {
		v := s.Score(final)
		mark := "PASS"
		if !v.Pass {
			mark = "FAIL"
		}
		fmt.Printf("%s %s: %s\n", mark, v.Name, v.Detail)
	}
	fmt.Println("\nThis step's row in the table needs every case, so watching one leaves it out.")
	return nil
}

// changes describes what a step did to the config, a line per change.
func changes(prev, next agent.RunnerConfig) []string {
	var out []string
	if next.Instructions != prev.Instructions {
		if added, ok := strings.CutPrefix(next.Instructions, prev.Instructions); ok {
			out = append(out, "+ instructions:")
			for _, line := range strings.Split(strings.TrimSpace(added), "\n") {
				out = append(out, "    "+line)
			}
		} else {
			out = append(out, "~ instructions rewritten")
		}
	}
	if next.Tools != prev.Tools && next.Tools != nil {
		names := "tools"
		if defs, err := next.Tools.Tools(context.Background()); err == nil {
			var ns []string
			for _, t := range defs {
				ns = append(ns, t.Name)
			}
			names = strings.Join(ns, ", ")
		}
		out = append(out, "+ tools: "+names)
	}
	if n := len(next.StepStages) - len(prev.StepStages); n > 0 {
		out = append(out, fmt.Sprintf("+ %d step stage(s), run before each model call", n))
	}
	if n := len(next.TurnEnd) - len(prev.TurnEnd); n > 0 {
		out = append(out, fmt.Sprintf("+ %d turn-end hook(s), which can send an answer back", n))
	}
	if n := len(next.ToolMiddleware) - len(prev.ToolMiddleware); n > 0 {
		out = append(out, fmt.Sprintf("+ %d tool middleware", n))
	}
	if next.MaxSteps != prev.MaxSteps {
		out = append(out, fmt.Sprintf("~ step limit %d -> %d", prev.MaxSteps, next.MaxSteps))
	}
	if next.Provider != prev.Provider {
		out = append(out, "~ a different provider")
	}
	if len(out) == 0 {
		out = append(out, "(the config is unchanged: this is the starting point)")
	}
	return out
}
