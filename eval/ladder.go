package eval

import (
	"context"
	"fmt"
	"math"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/panyam/chakra"
)

// Ladder is an agent built a step at a time, with the eval every step runs
// (#350). The first step is usually the model and its tools alone. Each step
// after it adds one thing, such as a stuck policy, a budget, a verifier or
// memory, and the report says what that step changed on the eval.
type Ladder struct {
	// Name names the agent, as examples/agents/<name> does.
	Name string
	// Steps run in order. Step i runs on the config steps 0 to i built.
	Steps []Step
	// Cases are the eval every step runs.
	Cases []SuiteCase
}

// Step is one rung of a Ladder.
type Step struct {
	// Name says what the step adds, such as "+ calc tool".
	Name string
	// Configure adds the step's change to the config the steps before it
	// built, and returns the result. Nil leaves the config as it is, which
	// suits a first step that runs the base config. The step runs before
	// the next one's Configure is called, so Configure may append to the
	// config's slices.
	Configure func(agent.RunnerConfig) (agent.RunnerConfig, error)
}

// StepReport is one step's outcome.
type StepReport struct {
	// Step is the step's Name.
	Step string `json:"step"`
	// Report is the eval's outcome on this step's config.
	Report SuiteReport `json:"report"`
	// Err is set when this step's Configure failed, or an earlier one did.
	// The step didn't run, and Report is empty.
	Err string `json:"err,omitempty"`
	// Before is the previous step's Passed, for Delta. It is zero on the
	// first step.
	Before int `json:"before"`
}

// Delta is how many more cases this step passed than the step before. It is
// zero on the first step.
func (s StepReport) Delta() int { return s.Report.Passed - s.Before }

// LadderReport is a Ladder's outcome, a StepReport per step in order.
type LadderReport struct {
	Ladder string       `json:"ladder"`
	Steps  []StepReport `json:"steps"`
}

// Run runs the ladder's cases once per step. s supplies the base config,
// ModelName and Prices, and s.Cases is replaced by the ladder's. A failed
// Configure ends the climb: that step and every step after it report the
// error and don't run.
func (l Ladder) Run(ctx context.Context, s Suite) LadderReport {
	report := LadderReport{Ladder: l.Name}
	cfg := s.Config
	s.Cases = l.Cases
	var failed string
	before := 0
	for i, step := range l.Steps {
		sr := StepReport{Step: step.Name, Before: before}
		if failed == "" && step.Configure != nil {
			next, err := step.Configure(cfg)
			if err != nil {
				failed = fmt.Sprintf("step %d (%s): configure: %v", i+1, step.Name, err)
			}
			cfg = next
		}
		if failed != "" {
			sr.Err = failed
			report.Steps = append(report.Steps, sr)
			continue
		}
		s.Config = cfg
		sr.Report = s.Run(ctx)
		before = sr.Report.Passed
		report.Steps = append(report.Steps, sr)
	}
	return report
}

// Table renders the report as a plain-text table, a row per step: cases
// passed, the change over the step before, dollars per correct answer and
// seconds per correct answer. A cost or time it can't know prints as "-".
// It returns the string and does not print (constraint A4).
func (r LadderReport) Table() string {
	var b strings.Builder
	cases := 0
	for _, s := range r.Steps {
		cases = max(cases, s.Report.Total)
	}
	fmt.Fprintf(&b, "%s: %d cases\n", r.Ladder, cases)
	// The reasoning column shows only when some step thought, so a ladder
	// on models that don't keeps its table as it was.
	thought := false
	for _, s := range r.Steps {
		thought = thought || s.Report.ReasoningTokens() > 0
	}
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	head := "step\tpass\tchange\tusd/correct\tsec/correct\t"
	if thought {
		head += "reasoning/correct\t"
	}
	fmt.Fprintln(w, head)
	for i, s := range r.Steps {
		if s.Err != "" {
			dashes := "-\t-\t-\t-\t"
			if thought {
				dashes += "-\t"
			}
			fmt.Fprintf(w, "%s\t%s%s\n", s.Step, dashes, s.Err)
			continue
		}
		change := ""
		if i > 0 {
			change = fmt.Sprintf("%+d", s.Delta())
		}
		usd, sec := "-", "-"
		if d, ok := s.Report.CostPerCorrect(); ok {
			usd = dollars(d)
		}
		if t, ok := s.Report.TimePerCorrect(); ok {
			sec = fmt.Sprintf("%.2f", t.Seconds())
		}
		row := fmt.Sprintf("%s\t%d/%d\t%s\t%s\t%s\t", s.Step, s.Report.Passed, s.Report.Total, change, usd, sec)
		if thought {
			think := "-"
			if n, ok := s.Report.ReasoningPerCorrect(); ok {
				think = fmt.Sprint(n)
			}
			row += think + "\t"
		}
		fmt.Fprintln(w, row)
	}
	w.Flush()
	return b.String()
}

// TimePerCorrect is the wall time of every case's turns, setup included,
// over the number of cases that passed. Failed cases count in the total,
// since the run spent that time too. It reports false when nothing passed.
func (r SuiteReport) TimePerCorrect() (time.Duration, bool) {
	if r.Passed == 0 {
		return 0, false
	}
	var total time.Duration
	for _, c := range r.Cases {
		total += c.Elapsed + c.SetupElapsed
	}
	return total / time.Duration(r.Passed), true
}

// ReasoningTokens sums the reasoning tokens of every case's turns, setup
// included, over every model. Zero means no model thought, or none of the
// providers reported it (agent.Usage.ReasoningTokens).
func (r SuiteReport) ReasoningTokens() int {
	n := 0
	for _, c := range r.Cases {
		for _, u := range c.Usage {
			n += u.ReasoningTokens
		}
		for _, u := range c.SetupUsage {
			n += u.ReasoningTokens
		}
	}
	return n
}

// ReasoningPerCorrect is ReasoningTokens over the number of cases that
// passed, rounded down. Failed cases count in the total, as in
// TimePerCorrect. It reports false when nothing passed.
func (r SuiteReport) ReasoningPerCorrect() (int, bool) {
	if r.Passed == 0 {
		return 0, false
	}
	return r.ReasoningTokens() / r.Passed, true
}

// dollars prints d with at least four decimals, and more when d is small, so
// two significant figures show. A cheap model's cost per correct answer can
// be a few millionths of a dollar.
func dollars(d float64) string {
	places := 4
	if d > 0 {
		places = min(max(places, 1-int(math.Floor(math.Log10(d)))), 10)
	}
	return fmt.Sprintf("$%.*f", places, d)
}
