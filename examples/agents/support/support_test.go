package support

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/mcpkit/core"
)

// Each step passes more customers on the scripted model, with the scripted
// customer saying yes when asked.
func TestEachStepPassesMore(t *testing.T) {
	l, stop, err := Ladder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	r := l.Run(context.Background(), eval.Suite{Config: agent.RunnerConfig{Provider: Scripted(), Instructions: Instructions, MaxSteps: 8}})
	var got []int
	for _, s := range r.Steps {
		if s.Err != "" {
			t.Fatalf("%s: %s", s.Step, s.Err)
		}
		got = append(got, s.Report.Passed)
	}
	if want := []int{2, 3, 4, 7, 8}; !slices.Equal(got, want) {
		t.Errorf("passed per step = %v, want %v\n%s", got, want, r.Table())
	}
}

// The policy guard stops a refund the policy doesn't allow, with the
// reason, and lets an allowed one through.
func TestPolicyGuard(t *testing.T) {
	s := &shop{}
	s.reset()
	guard := policyGuard(s)
	ran := 0
	next := func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
		ran++
		res := core.TextResult("done")
		return &res, nil
	}
	call := func(name, args string) *core.ToolResult {
		res, err := guard(context.Background(), agent.ToolCallInfo{Call: agent.ToolCall{Name: name, Args: core.NewRawJSON([]byte(args))}}, next)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for _, c := range []struct{ name, args, why string }{
		{"refund_order", `{"order_id":"A2"}`, "past the 30-day window"},
		{"refund_order", `{"order_id":"A3"}`, "final sale"},
		{"change_address", `{"order_id":"A5","address":"x"}`, "already shipped"},
	} {
		res := call(c.name, c.args)
		if !res.IsError || !strings.Contains(res.Content[0].Text, c.why) {
			t.Errorf("%s %s = %+v, want refused because %s", c.name, c.args, res, c.why)
		}
	}
	if ran != 0 {
		t.Errorf("a refused call reached the tool %d times", ran)
	}
	if res := call("refund_order", `{"order_id":"A1"}`); res.IsError || ran != 1 {
		t.Errorf("an allowed refund was stopped: %+v", res)
	}
}

// Keyword search finds the page a question is about.
func TestSearchFindsThePage(t *testing.T) {
	for q, want := range map[string]string{
		"How many days do I have to return something?": "returns",
		"How long is the warranty on headphones?":      "warranty",
		"Can you change the address on my order?":      "address",
	} {
		hits := search(q, 2)
		if !slices.ContainsFunc(hits, func(d doc) bool { return d.name == want }) {
			var names []string
			for _, d := range hits {
				names = append(names, d.name)
			}
			t.Errorf("search(%q) = %v, want %s among them", q, names, want)
		}
	}
}

// The citation check sends back an answer that cites nothing, and lets a
// cited answer or a question through.
func TestCitationCheck(t *testing.T) {
	for text, refuse := range map[string]bool{
		"You have 30 days to return it.":           true,
		"You have 30 days to return it [returns].": false,
		"Shall I refund order A1?":                 false,
	} {
		v, _ := citationCheck(context.Background(), agent.TurnEndInfo{Text: text, StepsLeft: 5})
		if (v.Continue != "") != refuse {
			t.Errorf("%q: refused %v, want %v", text, v.Continue != "", refuse)
		}
	}
}

// An answer given before the customer's thanks still counts, though the
// agent's last reply is "You're welcome".
func TestOutcomeReadsEveryReply(t *testing.T) {
	s := &shop{}
	s.reset()
	s.heard([]agent.Message{
		{Role: agent.RoleUser, Text: "How many days do I have to return something?"},
		{Role: agent.RoleAssistant, Text: "You have 30 days from delivery [returns]."},
		{Role: agent.RoleUser, Text: "Thanks!"},
		{Role: agent.RoleAssistant, Text: "You're welcome!"},
	})
	got := outcome(s, customers[5]).Score(eval.Result{Turn: &agent.TurnResult{Text: "You're welcome!"}})
	if !got.Pass {
		t.Errorf("score = %+v, want a pass from the earlier reply", got)
	}
}
