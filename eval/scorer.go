package eval

import (
	"fmt"
	"strings"

	"github.com/panyam/chakra"
)

// Score is one scorer's verdict on a Result. Value is a normalized score in
// [0,1] where 1.0 is a full pass; the deterministic scorers emit 1.0 or 0.0,
// while a graded scorer (the LLM judge) may report an intermediate value. Pass
// is a boolean, and the Suite aggregates it. Detail holds the human-readable
// specifics.
type Score struct {
	Name   string  `json:"name"`
	Pass   bool    `json:"pass"`
	Value  float64 `json:"value"`
	Detail string  `json:"detail,omitempty"`
}

// Scorer grades a Result. Implementations must be pure functions of the
// Result and safe for concurrent use; the deterministic ones read no state.
type Scorer interface {
	Score(Result) Score
}

// scorerFunc adapts a plain function to the Scorer interface.
type scorerFunc func(Result) Score

func (f scorerFunc) Score(r Result) Score { return f(r) }

// boolScore builds a pass/fail Score with Value 1.0 or 0.0.
func boolScore(name string, ok bool, detail string) Score {
	v := 0.0
	if ok {
		v = 1.0
	}
	return Score{Name: name, Pass: ok, Value: v, Detail: detail}
}

// ExactMatch passes when the final turn text equals want exactly.
func ExactMatch(want string) Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Turn == nil {
			return boolScore("ExactMatch", false, "no turn result (run failed)")
		}
		got := r.Turn.Text
		return boolScore("ExactMatch", got == want, fmt.Sprintf("want %q, got %q", want, got))
	})
}

// Contains passes when the final turn text contains substr.
func Contains(substr string) Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Turn == nil {
			return boolScore("Contains", false, "no turn result (run failed)")
		}
		got := r.Turn.Text
		return boolScore("Contains", strings.Contains(got, substr),
			fmt.Sprintf("want substring %q in %q", substr, got))
	})
}

// NotContains passes when the final turn text does NOT contain substr. It is
// the abstention and knowledge-supersede check. A memory eval uses it to
// assert the model did not surface a stale fact (the old value after an
// update), or did not hallucinate a specific it was never told. A failed run
// has no turn and so no answer to trust, and it fails the scorer.
func NotContains(substr string) Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Turn == nil {
			return boolScore("NotContains", false, "no turn result (run failed)")
		}
		got := r.Turn.Text
		return boolScore("NotContains", !strings.Contains(got, substr),
			fmt.Sprintf("want substring %q absent from %q", substr, got))
	})
}

// ToolCalled passes when the turn requested the named tool at least once. It
// scans the event stream for tool-begin. The Runner emits tool-begin for every
// dispatched call, including ones that later error or are denied. So the
// scorer reports intent to call regardless of the call's outcome.
func ToolCalled(name string) Scorer {
	return scorerFunc(func(r Result) Score {
		count := 0
		for _, e := range r.Events {
			if e.Kind == agent.EventToolBegin && e.ToolCall != nil && e.ToolCall.Name == name {
				count++
			}
		}
		return boolScore("ToolCalled", count > 0,
			fmt.Sprintf("tool %q called %d time(s)", name, count))
	})
}

// MaxSteps passes when the turn used at most n steps.
func MaxSteps(n int) Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Turn == nil {
			return boolScore("MaxSteps", false, "no turn result (run failed)")
		}
		steps := r.Turn.Steps
		return boolScore("MaxSteps", steps <= n, fmt.Sprintf("steps=%d, cap=%d", steps, n))
	})
}

// StepCount passes when the turn used exactly n steps.
func StepCount(n int) Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Turn == nil {
			return boolScore("StepCount", false, "no turn result (run failed)")
		}
		steps := r.Turn.Steps
		return boolScore("StepCount", steps == n, fmt.Sprintf("steps=%d, want=%d", steps, n))
	})
}

// NoError passes when the run itself succeeded and no tool failed: no run
// error, no error event, no tool dispatch error, and no tool result marked
// IsError. The Runner folds some failures back into the conversation, and
// those never abort the turn. NoError catches them as well as a hard run
// error.
func NoError() Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Err != nil {
			return boolScore("NoError", false, "run error: "+r.Err.Error())
		}
		for _, e := range r.Events {
			switch e.Kind {
			case agent.EventError:
				return boolScore("NoError", false, "error event: "+e.Error)
			case agent.EventToolError:
				return boolScore("NoError", false, "tool dispatch error: "+e.Error)
			case agent.EventToolEnd:
				if e.ToolResult != nil && e.ToolResult.IsError {
					name := ""
					if e.ToolCall != nil {
						name = e.ToolCall.Name
					}
					return boolScore("NoError", false, fmt.Sprintf("tool %q reported an error", name))
				}
			}
		}
		return boolScore("NoError", true, "no run, dispatch, or tool errors")
	})
}

// NotDenied passes when no tool call was blocked by an approval policy. A
// denial is deliberately not an error. The Runner emits a distinct
// tool-denied event, not tool-error, so NoError ignores it. NotDenied is the
// separate check for evals that assert the model never attempted a gated
// tool. Compose it with NoError when a case must be both clean and unblocked.
func NotDenied() Scorer {
	return scorerFunc(func(r Result) Score {
		for _, e := range r.Events {
			if e.Kind == agent.EventToolDenied {
				name := ""
				if e.ToolCall != nil {
					name = e.ToolCall.Name
				}
				return boolScore("NotDenied", false,
					fmt.Sprintf("tool %q denied: %s", name, e.Reason))
			}
		}
		return boolScore("NotDenied", true, "no tool calls were denied")
	})
}

// Ungradeable returns a Scorer that always fails, naming why the grade could
// not be computed here.
//
// It exists so "we could not grade this" is reported as a failure, not as a
// pass. The two alternatives are both worse. Dropping the scorer leaves the
// case graded by whatever remains, and for a rubric-graded case nothing that
// remains may discriminate. Skipping the case hides it from the totals.
func Ungradeable(name, reason string) Scorer {
	return scorerFunc(func(Result) Score {
		return Score{Name: name, Pass: false, Value: 0, Detail: reason}
	})
}
