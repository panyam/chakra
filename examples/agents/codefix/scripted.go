package codefix

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Scripted is a stand-in model for CI. It knows each task's fix, and it has
// one habit for each step after the first to correct:
//
//   - Without a test tool it can't find the line that panics.
//   - Without a repo map it looks for the priciest bug in report.go, where
//     the symptom shows, and gives up.
//   - Without a plan slot it fixes the first of two bugs and stops.
//   - On the threshold task it changes the constant, which breaks another
//     test, and calls it done. Only the test gate sends it back.
//
// So each step passes one more task in CI, with no model. Only a live run
// says what a step does for a real one.
func Scripted() agent.Provider { return scripted{} }

type scripted struct{}

func (s scripted) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return agent.NewStubProvider(s.reply(req)).Stream(ctx, req)
}

func (s scripted) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return agent.NewStubProvider(s.reply(req)).Generate(ctx, req)
}

// action is one tool call, or the final answer when tool is empty.
type action struct {
	tool string
	args map[string]any
	text string
}

// fix reads path, as edit_file needs the hash a read returns, then edits it.
func fix(path, old, new string) []action {
	return []action{
		{tool: "read_file", args: map[string]any{"path": path}},
		{tool: "edit_file", args: map[string]any{"path": path, "edits": []map[string]string{{"old": old, "new": new}}}},
	}
}

func (scripted) reply(req agent.ProviderRequest) agent.StubTurn {
	var report, testOutput string
	calls, repoMap, refused := 0, false, false
	for _, m := range req.Messages {
		switch {
		case m.Role == agent.RoleUser && report == "":
			report = m.Text
		case m.Role == agent.RoleAssistant:
			calls += len(m.ToolCalls)
		case m.Role == agent.RoleTool && strings.Contains(m.Text, "FAIL"):
			testOutput = m.Text
		}
		repoMap = repoMap || strings.Contains(m.Text, "Files in the module, with what each declares")
		refused = refused || strings.Contains(m.Text, "The tests still fail")
	}
	has := func(name string) bool {
		for _, t := range req.Tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	tests, plan := has("run_tests"), has(agent.SlotToolName)

	var acts []action
	if tests {
		acts = append(acts, action{tool: "run_tests", args: map[string]any{}})
	}
	say := func(text string) { acts = append(acts, action{text: text}) }
	switch {
	case strings.HasPrefix(report, tasks[0].report):
		acts = append(acts, fix("money/money.go", "for i := 1; i < len(xs); i++", "for i := 0; i < len(xs); i++")...)
		say("money.Sum started at the second item. It starts at the first now.")
	case strings.HasPrefix(report, tasks[1].report):
		acts = append(acts, fix("discount/discount.go", "const Threshold = 1000", "const Threshold = 999")...)
		say("The threshold was one cent too high. It's 999 now.")
		if refused {
			acts = append(acts, fix("discount/discount.go", "const Threshold = 999", "const Threshold = 1000")...)
			acts = append(acts, fix("discount/discount.go", "if total > Threshold {", "if total >= Threshold {")...)
			say("The comparison left out the threshold itself. It's >= now, and the constant is back to 1000.")
		}
	case strings.HasPrefix(report, tasks[2].report):
		if !strings.Contains(testOutput, "(*Cart).Total") {
			say("I read cart.go and couldn't tell which call panics.")
			break
		}
		acts = append(acts, fix("cart/cart.go", "func (c *Cart) Total() int {\n\treturn money.Sum(c.Prices)", "func (c *Cart) Total() int {\n\tif c == nil {\n\t\treturn 0\n\t}\n\treturn money.Sum(c.Prices)")...)
		say("Total panicked on a nil cart. It returns 0 now, as Priciest does.")
	case strings.HasPrefix(report, tasks[3].report):
		if !repoMap {
			say("report.Summary formats the priciest item correctly, so I couldn't find the bug.")
			break
		}
		acts = append(acts, fix("money/money.go", "if x < best {", "if x > best {")...)
		say("The repo map shows cart.Priciest calls money.Max, and Max kept the smallest amount. It keeps the largest now.")
	case strings.HasPrefix(report, tasks[4].report):
		if plan {
			acts = append(acts, action{tool: agent.SlotToolName, args: map[string]any{"slot": "plan", "text": "1. discount rounds down: add 50 before dividing. 2. report drops the leading zero: use %02d."}})
		}
		acts = append(acts, fix("discount/discount.go", "return total - total*pct/100", "return total - (total*pct+50)/100")...)
		if !plan {
			say("The discount rounds to the nearest cent now.")
			break
		}
		acts = append(acts, fix("report/report.go", `"$%d.%d"`, `"$%d.%02d"`)...)
		say("Both fixed: the discount rounds to the nearest cent, and cents print with two digits.")
	default:
		return agent.StubTurn{Text: "I don't know this task."}
	}

	// Answers count as actions too, so a refusal after one picks up after it.
	done := calls + answers(req.Messages)
	if done >= len(acts) {
		return agent.StubTurn{Text: acts[len(acts)-1].text}
	}
	a := acts[done]
	if a.tool == "" {
		return agent.StubTurn{Text: a.text}
	}
	if a.tool == "edit_file" {
		a.args["expect_hash"] = lastHash(req.Messages)
	}
	raw, _ := json.Marshal(a.args)
	return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: fmt.Sprintf("c%d", done), Name: a.tool, Args: core.NewRawJSON(raw)}}}
}

// answers counts the assistant messages that answered without a tool call.
func answers(ms []agent.Message) int {
	n := 0
	for _, m := range ms {
		if m.Role == agent.RoleAssistant && len(m.ToolCalls) == 0 && m.Text != "" {
			n++
		}
	}
	return n
}

// lastHash is the hash in the latest tool result, which is the read_file
// that comes before each edit.
func lastHash(ms []agent.Message) string {
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Role != agent.RoleTool {
			continue
		}
		for _, line := range strings.Split(ms[i].Text, "\n") {
			if h, ok := strings.CutPrefix(line, "hash: "); ok {
				return strings.Fields(h)[0]
			}
		}
		return ""
	}
	return ""
}
