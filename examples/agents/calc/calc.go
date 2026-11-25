// Package calc is the smallest agent ladder (#350): an assistant that
// answers arithmetic questions, first on its own and then with a calculator
// tool. It exists so `make exercise-agents` has a ladder to run before any
// agent for a real job does, and its README shows how to read the table.
package calc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/mcpkit/core"
)

// Instructions is the agent's system prompt on every step.
const Instructions = "Answer the arithmetic question with the number alone. Use the calc tool when you have one."

// cases are the eval: two sums a model gets right in its head, and four
// products and quotients it usually doesn't.
var cases = []struct{ question, answer string }{
	{"What is 2 + 3?", "5"},
	{"What is 40 - 18?", "22"},
	{"What is 17 * 23?", "391"},
	{"What is 144 / 12?", "12"},
	{"What is 1234 * 5678?", "7006652"},
	{"What is 99 * 101?", "9999"},
}

// Ladder is calc's two steps over its six cases.
func Ladder() eval.Ladder {
	var cs []eval.SuiteCase
	for _, c := range cases {
		cs = append(cs, eval.Single(eval.Case{Name: c.question, Input: c.question}, eval.Contains(c.answer), eval.NoError()))
	}
	return eval.Ladder{
		Name: "calc",
		Steps: []eval.Step{
			{Name: "model only"},
			{Name: "+ calc tool", Configure: withCalc},
		},
		Cases: cs,
	}
}

// withCalc offers the calc tool.
func withCalc(c agent.RunnerConfig) (agent.RunnerConfig, error) {
	src := agent.NewFuncSource()
	err := agent.AddFunc(src, "calc", "Evaluate one arithmetic operation, such as \"17 * 23\". It takes two integers and one of + - * /.",
		func(_ context.Context, in struct {
			Expr string `json:"expr" jsonschema:"the operation, such as 17 * 23"`
		}) (string, error) {
			return evaluate(in.Expr)
		}, agent.ReadOnly())
	if err != nil {
		return c, err
	}
	c.Tools = src
	return c, nil
}

var operation = regexp.MustCompile(`(-?\d+)\s*([-+*/])\s*(-?\d+)`)

// evaluate works out "a op b".
func evaluate(expr string) (string, error) {
	m := operation.FindStringSubmatch(expr)
	if m == nil {
		return "", fmt.Errorf("calc: %q isn't one operation on two integers", expr)
	}
	a, _ := strconv.ParseInt(m[1], 10, 64)
	b, _ := strconv.ParseInt(m[3], 10, 64)
	switch m[2] {
	case "+":
		return strconv.FormatInt(a+b, 10), nil
	case "-":
		return strconv.FormatInt(a-b, 10), nil
	case "*":
		return strconv.FormatInt(a*b, 10), nil
	}
	if b == 0 {
		return "", fmt.Errorf("calc: division by zero")
	}
	if a%b == 0 {
		return strconv.FormatInt(a/b, 10), nil
	}
	return strconv.FormatFloat(float64(a)/float64(b), 'f', -1, 64), nil
}

// Scripted is a stand-in model for CI. It adds and subtracts in its head.
// It calls calc for a product or a quotient when the tool is offered, and
// otherwise guesses to one significant figure, as a small model often
// does. So the ladder's second step passes more in CI, with no model.
func Scripted() agent.Provider { return scripted{} }

type scripted struct{}

func (s scripted) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return agent.NewStubProvider(s.reply(req)).Stream(ctx, req)
}

func (s scripted) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return agent.NewStubProvider(s.reply(req)).Generate(ctx, req)
}

func (scripted) reply(req agent.ProviderRequest) agent.StubTurn {
	if len(req.Messages) == 0 {
		return agent.StubTurn{Text: "I have no question."}
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == agent.RoleTool {
		return agent.StubTurn{Text: strings.TrimSpace(last.Text)}
	}
	m := operation.FindStringSubmatch(last.Text)
	if m == nil {
		return agent.StubTurn{Text: "I can only do arithmetic."}
	}
	expr := m[0]
	if m[2] == "+" || m[2] == "-" {
		v, _ := evaluate(expr)
		return agent.StubTurn{Text: v}
	}
	for _, t := range req.Tools {
		if t.Name == "calc" {
			args, _ := json.Marshal(map[string]string{"expr": expr})
			return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "calc", Args: core.NewRawJSON(args)}}}
		}
	}
	v, _ := evaluate(expr)
	f, _ := strconv.ParseFloat(v, 64)
	return agent.StubTurn{Text: "about " + strconv.FormatFloat(roughly(f), 'f', -1, 64)}
}

// roughly rounds to one significant figure.
func roughly(f float64) float64 {
	if f == 0 {
		return 0
	}
	p := math.Pow(10, math.Floor(math.Log10(math.Abs(f))))
	return math.Round(f/p) * p
}
