package eval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/panyam/chakra"
)

// SuiteCase is one graded unit: a scenario to run and the scorers that grade
// it.
//
// Scorers are per-case, not per-suite, because an external benchmark grades
// each case on its own. Every question in a memory benchmark has its own
// expected answer. A single scorer list shared across the suite can only
// express properties that hold for every case ("did not error", "called a
// tool"). It can never express the ground truth that makes a benchmark a
// benchmark. The previous shape had exactly that limitation, and longmemeval
// worked around it by hand-rolling its own runner loop.
type SuiteCase struct {
	// Scenario is the conversation to run. Its Name identifies the case in
	// the report.
	Scenario Scenario

	// Scorers builds this case's graders. It takes the provider because a
	// rubric scorer (Judge) needs a model to grade with, and the suite owns
	// the provider; a case that only uses deterministic scorers ignores the
	// argument.
	//
	// A case with no scorers fails, as it did under the old Suite. If
	// nothing grades a case, it would silently pass forever.
	Scorers func(agent.Provider) []Scorer

	// Configure adapts the suite's base RunnerConfig for this case. Nil uses
	// the base unchanged.
	//
	// It exists because some cases test the harness, not the model. A
	// compaction case has to run under a Compactor, and building the
	// Compactor needs the provider. Configure returns a config rather than
	// mutating one, so each case's changes can't leak into the next.
	Configure func(agent.RunnerConfig) (agent.RunnerConfig, error)

	// Setup prepares this case's run, once, after Configure and before the
	// first turn. Nil runs the Scenario as written.
	//
	// Picture a memory layer under test. For each benchmark question it
	// ingests the haystack into its own store, then offers the agent tools
	// that query that store. Configure can't do that, because it sees only
	// the config and returns no usage. Setup returns what the run should use
	// and what the ingestion cost, and the report keeps that cost apart from
	// the question's. An adapter usually closes over the case's own input.
	//
	// An error fails this case with RunErr "setup: ...", and the suite moves
	// on. The Usage returned with an error is still reported.
	Setup func(context.Context) (Setup, error)
}

// Setup is what SuiteCase.Setup hands back. Each field that is set overrides
// the Scenario's own field for this run.
type Setup struct {
	// Tools replaces Scenario.Tools. When the Scenario also has Memory set,
	// the memory tools are merged on top, as they are for Scenario.Tools.
	Tools agent.ToolSource

	// Instructions replaces Scenario.Instructions when non-empty.
	Instructions string

	// History replaces Scenario.History when non-nil.
	History []agent.Message

	// Usage is what the setup itself spent, keyed by model name the way
	// TurnResult.UsageByModel is. A key of "" means the suite's ModelName.
	// Suite.Prices prices it like the turns' usage, and it is reported as
	// CaseReport.SetupUsage.
	Usage map[string]agent.Usage
}

// Price is what one model charges, in dollars per million tokens.
type Price struct {
	InputPerM  float64 `json:"inputPerM"`
	OutputPerM float64 `json:"outputPerM"`
}

// Prices maps a model name to its Price. The names are the ones CaseReport
// uses, so the keys of TurnResult.UsageByModel and Setup.Usage, with ""
// relabelled as Suite.ModelName.
type Prices map[string]Price

// Cost prices every usage map together. It reports false when any model
// with a non-zero token count has no price, because a total that leaves out
// one model's spend would read as cheaper than the run was. It also reports
// false for a nil Prices.
func (p Prices) Cost(usage ...map[string]agent.Usage) (float64, bool) {
	if p == nil {
		return 0, false
	}
	total := 0.0
	for _, u := range usage {
		for name, n := range u {
			if n.InputTokens == 0 && n.OutputTokens == 0 {
				continue
			}
			price, ok := p[name]
			if !ok {
				return 0, false
			}
			total += float64(n.InputTokens)*price.InputPerM/1e6 + float64(n.OutputTokens)*price.OutputPerM/1e6
		}
	}
	return total, true
}

// Single wraps a single-turn Case with a fixed scorer list, for the
// table-driven style that does not need per-case ground truth.
//
// It exists so the common shape is still one line after Suite moved to
// scenarios. An adapter yields a SuiteCase, and Single builds the same type,
// so there is one kind of graded unit, not two.
func Single(c Case, scorers ...Scorer) SuiteCase {
	return SuiteCase{
		Scenario: Scenario{
			Name:         c.Name,
			History:      c.History,
			Turns:        []string{c.Input},
			Tools:        c.Tools,
			Instructions: c.Instructions,
			MaxSteps:     c.MaxSteps,
		},
		Scorers: func(agent.Provider) []Scorer { return scorers },
	}
}

// Suite is a set of graded cases sharing a base config.
type Suite struct {
	// Config is the base RunnerConfig shared by every case (Provider is
	// required). Each case's Configure is layered on a copy.
	Config agent.RunnerConfig

	// Cases is the set of graded units to evaluate.
	Cases []SuiteCase

	// ModelName labels the tokens of steps no StepStage named. The Runner
	// counts those under "" in UsageByModel, because a Provider carries no
	// name of its own. Set it to the model behind Config.Provider so its
	// tokens can be priced.
	ModelName string

	// Prices turns each case's token counts into a dollar cost. Nil reports
	// tokens and time only.
	Prices Prices
}

// CaseReport is one case's outcome. It holds the per-scorer verdicts and
// whether the case passed overall, meaning every scorer passed and the run
// built cleanly. It also holds what the case spent in tokens, time and,
// when the suite has prices, dollars.
type CaseReport struct {
	Case   string  `json:"case"`
	Scores []Score `json:"scores"`
	Pass   bool    `json:"pass"`
	// RunErr is set when the harness could not run this case (an invalid
	// config, a failed Setup, a case with no turns); when set, no scorers
	// ran and Pass is false.
	RunErr string `json:"runErr,omitempty"`

	// Usage sums the tokens of every turn in the scenario, by model name.
	// For a one-question benchmark that is the question's turn. It leaves
	// out sub-agents, as TurnResult.Usage does, and it leaves out the
	// judge, whose tokens are the cost of grading and not of the agent.
	// Models that spent nothing are left out, so the map is empty for a
	// provider that reports no usage.
	Usage map[string]agent.Usage `json:"usage,omitempty"`

	// SetupUsage is what SuiteCase.Setup reported spending, by model name.
	SetupUsage map[string]agent.Usage `json:"setupUsage,omitempty"`

	// Elapsed is the wall time of the turns, and SetupElapsed the wall time
	// of SuiteCase.Setup. Neither includes scoring.
	Elapsed      time.Duration `json:"elapsed,omitempty"`
	SetupElapsed time.Duration `json:"setupElapsed,omitempty"`

	// Cost is Usage plus SetupUsage priced by Suite.Prices, in dollars. It
	// is nil when the suite has no prices or a model that spent tokens has
	// none.
	Cost *float64 `json:"cost,omitempty"`
}

// SuiteReport is the aggregate: one CaseReport per case plus pass/fail counts.
//
// Passed and Failed count whole cases, and a case passes only if every one
// of its scorers passed. That suits a benchmark that asks one question per
// case. It doesn't suit a benchmark that reports several independent rates.
// A security suite wants utility and attack-success counted separately, and
// one of them inverted. Each CaseReport keeps its per-scorer verdicts with
// their names, so a per-dimension rollup only adds code over data already
// here and needs no re-plumbing. See issue 1060.
type SuiteReport struct {
	Cases  []CaseReport `json:"cases"`
	Passed int          `json:"passed"`
	Failed int          `json:"failed"`
	Total  int          `json:"total"`
}

// Pass reports whether every case passed.
func (r SuiteReport) Pass() bool { return r.Failed == 0 }

// Run evaluates every case against its own scorers and returns the report. It
// does not stop on a failing case. A per-case harness failure goes in that
// case's RunErr, and the suite continues. Ctx cancellation does not abort the
// whole suite either. It reaches each case through the Runner. NoError
// records it in the scores, or RunErr records it for a build failure.
func (s Suite) Run(ctx context.Context) SuiteReport {
	report := SuiteReport{Total: len(s.Cases)}
	for _, sc := range s.Cases {
		cr := CaseReport{Case: sc.Scenario.Name, Pass: true}

		fail := func(format string, args ...any) {
			cr.RunErr = fmt.Sprintf(format, args...)
			cr.Pass = false
			s.price(&cr)
			report.Cases = append(report.Cases, cr)
			report.Failed++
		}

		cfg := s.Config
		if sc.Configure != nil {
			adapted, err := sc.Configure(cfg)
			if err != nil {
				fail("configure: %v", err)
				continue
			}
			cfg = adapted
		}

		scenario := sc.Scenario
		if sc.Setup != nil {
			start := time.Now()
			st, err := sc.Setup(ctx)
			cr.SetupElapsed = time.Since(start)
			cr.SetupUsage = s.addUsage(nil, st.Usage)
			if err != nil {
				fail("setup: %v", err)
				continue
			}
			if st.Tools != nil {
				scenario.Tools = st.Tools
			}
			if st.Instructions != "" {
				scenario.Instructions = st.Instructions
			}
			if st.History != nil {
				scenario.History = st.History
			}
		}

		start := time.Now()
		results, err := RunScenario(ctx, cfg, scenario)
		cr.Elapsed = time.Since(start)
		for _, r := range results {
			if r.Turn == nil {
				continue
			}
			byModel := r.Turn.UsageByModel
			if byModel == nil {
				byModel = map[string]agent.Usage{"": r.Turn.Usage}
			}
			cr.Usage = s.addUsage(cr.Usage, byModel)
		}
		if err != nil {
			fail("%v", err)
			continue
		}
		// Final panics on an empty slice. If a case's Scenario has no Turns,
		// RunScenario returns one. That is a plausible adapter bug (History
		// populated, the question forgotten), so we report it as this case's
		// failure and the suite keeps going.
		if len(results) == 0 {
			fail("scenario %q ran no turns", sc.Scenario.Name)
			continue
		}
		final := Final(results)

		var scorers []Scorer
		if sc.Scorers != nil {
			scorers = sc.Scorers(s.Config.Provider)
		}
		for _, scorer := range scorers {
			verdict := scorer.Score(final)
			cr.Scores = append(cr.Scores, verdict)
			if !verdict.Pass {
				cr.Pass = false
			}
		}
		if len(scorers) == 0 {
			cr.Pass = false
		}
		s.price(&cr)

		report.Cases = append(report.Cases, cr)
		if cr.Pass {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	return report
}

// addUsage adds from into into, relabelling "" as ModelName and dropping
// models that spent nothing. It returns into, allocating it when needed.
func (s Suite) addUsage(into, from map[string]agent.Usage) map[string]agent.Usage {
	for name, u := range from {
		if u.InputTokens == 0 && u.OutputTokens == 0 {
			continue
		}
		if name == "" {
			name = s.ModelName
		}
		if into == nil {
			into = map[string]agent.Usage{}
		}
		sum := into[name]
		sum.InputTokens += u.InputTokens
		sum.OutputTokens += u.OutputTokens
		into[name] = sum
	}
	return into
}

// price sets cr.Cost from the suite's prices.
func (s Suite) price(cr *CaseReport) {
	if cost, ok := s.Prices.Cost(cr.Usage, cr.SetupUsage); ok {
		cr.Cost = &cost
	}
}

// Cost sums every case's cost. It reports false when any case has no cost,
// for the reason Prices.Cost gives.
func (r SuiteReport) Cost() (float64, bool) {
	return sumCost(r.Cases)
}

// CostPerCorrect is the total cost over the number of cases that passed.
// Failed cases count in the total, since the run paid for them too. It
// reports false when the cost is unknown or nothing passed.
func (r SuiteReport) CostPerCorrect() (float64, bool) {
	cost, ok := r.Cost()
	if !ok || r.Passed == 0 {
		return 0, false
	}
	return cost / float64(r.Passed), true
}

func sumCost(cases []CaseReport) (float64, bool) {
	if len(cases) == 0 {
		return 0, false
	}
	total := 0.0
	for _, c := range cases {
		if c.Cost == nil {
			return 0, false
		}
		total += *c.Cost
	}
	return total, true
}

// String renders the report as a plain-text table. It returns the string and
// does not print (constraint A4). A test or CLI writes it. The format is
// stable enough to eyeball, but it is not a machine contract. Marshal the
// report as JSON for that.
func (r SuiteReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "eval suite: %d/%d cases passed\n", r.Passed, r.Total)
	for _, c := range r.Cases {
		status := "PASS"
		if !c.Pass {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %s\n", status, c.Case)
		if line := spendLine(c); line != "" {
			fmt.Fprintf(&b, "      %s\n", line)
		}
		if c.RunErr != "" {
			fmt.Fprintf(&b, "      run error: %s\n", c.RunErr)
			continue
		}
		for _, sc := range c.Scores {
			mark := "ok"
			if !sc.Pass {
				mark = "x "
			}
			fmt.Fprintf(&b, "      %s %-12s %.2f  %s\n", mark, sc.Name, sc.Value, sc.Detail)
		}
	}
	if cost, ok := r.Cost(); ok {
		fmt.Fprintf(&b, "cost: $%.4f in all", cost)
		if per, ok := r.CostPerCorrect(); ok {
			fmt.Fprintf(&b, ", $%.4f per correct answer", per)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// spendLine renders a case's tokens, time and cost on one line, or "" when
// the case reported none.
func spendLine(c CaseReport) string {
	if len(c.Usage) == 0 && len(c.SetupUsage) == 0 {
		return ""
	}
	total := func(u map[string]agent.Usage) (in, out int) {
		for _, n := range u {
			in += n.InputTokens
			out += n.OutputTokens
		}
		return in, out
	}
	var parts []string
	if len(c.SetupUsage) > 0 {
		in, out := total(c.SetupUsage)
		parts = append(parts, fmt.Sprintf("setup %d in / %d out (%s)", in, out, c.SetupElapsed.Round(time.Millisecond)))
	}
	in, out := total(c.Usage)
	parts = append(parts, fmt.Sprintf("turns %d in / %d out (%s)", in, out, c.Elapsed.Round(time.Millisecond)))
	if c.Cost != nil {
		parts = append(parts, fmt.Sprintf("$%.4f", *c.Cost))
	}
	return strings.Join(parts, ", ")
}

// DimensionReport is one scorer name's outcome across a whole suite.
type DimensionReport struct {
	// Name is the scorer name these counts aggregate.
	Name string `json:"name"`

	// Passed and Failed count the cases carrying a verdict under this name.
	// Total is their sum, and is not necessarily the suite's case count. A
	// dimension has a smaller Total when only some cases are graded on it.
	Passed int `json:"passed"`
	Failed int `json:"failed"`
	Total  int `json:"total"`

	// Cost sums the cost of the cases graded on this dimension. It is nil
	// when any of them has no cost.
	Cost *float64 `json:"cost,omitempty"`
}

// CostPerPass is Cost over Passed, the cost of one correct answer on this
// dimension. It reports false when the cost is unknown or nothing passed.
func (d DimensionReport) CostPerPass() (float64, bool) {
	if d.Cost == nil || d.Passed == 0 {
		return 0, false
	}
	return *d.Cost / float64(d.Passed), true
}

// Rate is the fraction of graded cases that passed this dimension, 0 when
// nothing was graded on it.
func (d DimensionReport) Rate() float64 {
	if d.Total == 0 {
		return 0
	}
	return float64(d.Passed) / float64(d.Total)
}

// Dimensions aggregates verdicts by scorer name, in first-seen order.
//
// Passed/Failed on SuiteReport count whole cases, and a case passes only if
// every one of its scorers passed. That suits a benchmark that asks one
// question per case. It doesn't suit a suite that reports several independent
// rates. A security suite measures whether the agent did the user's job AND
// whether it resisted an injected instruction, and those move independently.
// Imagine a run that succeeded at the task while also obeying the attacker.
// That quadrant matters most, and collapsing the two rates loses it.
//
// Dimensions is deliberately a method, not new fields. CaseReport already
// carries the data, so a suite that does not need Dimensions is unaffected,
// and adding it needed no re-plumbing. The caller decides whether to present
// a dimension inverted, and nothing here encodes that. For example, an
// attack-success rate is 1-Rate of a "resisted" dimension.
func (r SuiteReport) Dimensions() []DimensionReport {
	var order []string
	byName := map[string]*DimensionReport{}
	graded := map[string][]CaseReport{}
	lastCase := map[string]int{}
	for i, c := range r.Cases {
		for _, s := range c.Scores {
			d, ok := byName[s.Name]
			if !ok {
				d = &DimensionReport{Name: s.Name}
				byName[s.Name] = d
				order = append(order, s.Name)
			}
			d.Total++
			// A case with two verdicts under one name pays once.
			if j, seen := lastCase[s.Name]; !seen || j != i {
				graded[s.Name] = append(graded[s.Name], c)
				lastCase[s.Name] = i
			}
			if s.Pass {
				d.Passed++
			} else {
				d.Failed++
			}
		}
	}
	out := make([]DimensionReport, 0, len(order))
	for _, name := range order {
		d := *byName[name]
		if cost, ok := sumCost(graded[name]); ok {
			d.Cost = &cost
		}
		out = append(out, d)
	}
	return out
}
