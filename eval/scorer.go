package eval

import (
	"fmt"
	"slices"
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
	// Want is the expected answer in a short form, such as the rows Rows
	// wants or the text Contains looks for, for a report to print beside
	// the answer. It is set whether or not the case passed, and empty for a
	// scorer with nothing to compare against.
	Want string `json:"want,omitempty"`
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

// wants sets a score's Want.
func wants(s Score, want string) Score {
	s.Want = want
	return s
}

// ExactMatch passes when the final turn text equals want exactly.
func ExactMatch(want string) Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Turn == nil {
			return wants(boolScore("ExactMatch", false, noTurn(r)), want)
		}
		got := r.Turn.Text
		return wants(boolScore("ExactMatch", got == want, fmt.Sprintf("want %q, got %q", want, got)), want)
	})
}

// Contains passes when the final turn text contains substr.
func Contains(substr string) Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Turn == nil {
			return wants(boolScore("Contains", false, noTurn(r)), fmt.Sprintf("contains %q", substr))
		}
		got := r.Turn.Text
		return wants(boolScore("Contains", strings.Contains(got, substr),
			fmt.Sprintf("want substring %q in %q", substr, got)), fmt.Sprintf("contains %q", substr))
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
			return boolScore("NotContains", false, noTurn(r))
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
			return boolScore("MaxSteps", false, noTurn(r))
		}
		steps := r.Turn.Steps
		return boolScore("MaxSteps", steps <= n, fmt.Sprintf("steps=%d, cap=%d", steps, n))
	})
}

// StepCount passes when the turn used exactly n steps.
func StepCount(n int) Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Turn == nil {
			return boolScore("StepCount", false, noTurn(r))
		}
		steps := r.Turn.Steps
		return boolScore("StepCount", steps == n, fmt.Sprintf("steps=%d, want=%d", steps, n))
	})
}

// NoError passes when the run succeeded and every tool error was recovered.
// It fails on a run error, an error event, or a tool dispatch error, such as
// a call to a tool that doesn't exist. A tool result marked IsError fails it
// too, unless a later call to the same tool returned a result without one.
// That later success means the model fixed its call, as a stuck policy or a
// helpful error message is meant to make it do (#365). The Runner folds
// these failures back into the conversation, and none of them aborts the
// turn, so NoError reads them from the events.
func NoError() Scorer {
	return scorerFunc(func(r Result) Score {
		if r.Err != nil {
			return boolScore("NoError", false, "run error: "+r.Err.Error())
		}
		// failed holds each tool whose latest result was an error.
		failed := map[string]bool{}
		var order []string
		for _, e := range r.Events {
			switch e.Kind {
			case agent.EventError:
				return boolScore("NoError", false, "error event: "+e.Error)
			case agent.EventToolError:
				return boolScore("NoError", false, "tool dispatch error: "+e.Error)
			case agent.EventToolEnd:
				name := ""
				if e.ToolCall != nil {
					name = e.ToolCall.Name
				}
				if e.ToolResult != nil && e.ToolResult.IsError {
					if !failed[name] {
						order = append(order, name)
					}
					failed[name] = true
				} else {
					failed[name] = false
				}
			}
		}
		for _, name := range order {
			if failed[name] {
				return boolScore("NoError", false, fmt.Sprintf("tool %q reported an error, and no later call to it succeeded", name))
			}
		}
		return boolScore("NoError", true, "no run or dispatch errors, and every tool error was recovered")
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

// Rows grades an answer that lists rows, such as import paths or qualified
// names. It passes when every row in want is in the answer, and no row of
// among that is missing from want is. among is every row the answer could
// name, so a row outside it doesn't fail the case.
//
// The answer is the lines that hold one row and nothing else, once list
// markers and backticks are trimmed. So a list followed by "audit imports it
// too, but it's in another module" grades the list. An answer with no such
// line, such as one sentence naming two rows, is graded on its whole text.
//
// A row matches only as a whole token: a match followed by a letter, digit,
// '.', '/', '_' or '-' is part of a longer row. So example.com/shop/orders
// doesn't match inside example.com/shop/orders.Place.
func Rows(want, among []string) Scorer {
	sorted := slices.Sorted(slices.Values(want))
	short := strings.Join(sorted, ", ")
	wanted := map[string]bool{}
	for _, w := range want {
		wanted[w] = true
	}
	return scorerFunc(func(r Result) Score {
		if r.Turn == nil {
			return wants(boolScore("Rows", false, noTurn(r)), short)
		}
		text := listedRows(r.Turn.Text, among, want)
		var missing, extra []string
		for _, w := range want {
			if !hasRow(text, w) {
				missing = append(missing, w)
			}
		}
		for _, a := range among {
			if !wanted[a] && hasRow(text, a) {
				extra = append(extra, a)
			}
		}
		detail := fmt.Sprintf("%d of %d rows", len(want)-len(missing), len(want))
		if len(missing) > 0 {
			detail += fmt.Sprintf(", missing %s", strings.Join(missing, ", "))
		}
		if len(extra) > 0 {
			detail += fmt.Sprintf(", extra %s", strings.Join(extra, ", "))
		}
		return wants(boolScore("Rows", len(missing) == 0 && len(extra) == 0, detail), short)
	})
}

// listedRows returns the lines of text that hold a single row of among or
// want, joined, or text itself when no line does.
func listedRows(text string, among, want []string) string {
	rows := map[string]bool{}
	for _, r := range among {
		rows[r] = true
	}
	for _, r := range want {
		rows[r] = true
	}
	var listed []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.Trim(line, " \t-*`")
		if rows[line] {
			listed = append(listed, line)
		}
	}
	if len(listed) == 0 {
		return text
	}
	return strings.Join(listed, "\n")
}

// hasRow reports whether row appears in text as a whole token.
func hasRow(text, row string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], row)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(row)
		if !rowByte(text, start-1) && !rowByte(text, end) {
			return true
		}
		i = start + 1
	}
}

// rowByte reports whether text[i] would continue a row. A '.' or '/' that
// ends a sentence, before a space or the end of text, doesn't.
func rowByte(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return false
	}
	c := text[i]
	if c == '.' || c == '/' {
		return i+1 < len(text) && rowByte(text, i+1)
	}
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// noTurn is a scorer's detail for a run with no turn result, with the run's
// error when there is one.
func noTurn(r Result) string {
	if r.Err != nil {
		return "no turn result (run failed: " + r.Err.Error() + ")"
	}
	return "no turn result (run failed)"
}
