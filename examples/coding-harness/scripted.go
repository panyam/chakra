package main

import (
	"encoding/json"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// scriptedProvider plays what a competent agent with today's tools does:
// read the code, fix the loop, run the tests, answer. The steer the driver
// sends after the first tool result joins the turn (#149), so the answer
// covers it. As tickets land the script moves
// to their tools (apply_patch, update_plan), so the scripted run keeps
// exercising the wiring each check names.
func scriptedProvider(fixture string) (agent.Provider, error) {
	call := func(id, name string, args any) agent.ToolCall {
		raw, _ := json.Marshal(args)
		return agent.ToolCall{ID: id, Name: name, Args: core.NewRawJSON(raw)}
	}
	return agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			call("p0", "update_plan", map[string]any{"plan": []map[string]string{
				{"step": "Find why the tests fail", "status": "in_progress"},
				{"step": "Fix it", "status": "pending"},
				{"step": "Run go test ./...", "status": "pending"},
			}}),
		}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			call("c0", "load_skill", map[string]any{"name": "go-testing"}),
		}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			call("c1", "read_file", map[string]any{"path": "stats/stats.go"}),
		}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			call("c2", "apply_patch", map[string]any{"input": "*** Begin Patch\n*** Update File: stats/stats.go\n@@ func Sum(xs []int) int {\n-\tfor i := 1; i < len(xs); i++ {\n+\tfor i := 0; i < len(xs); i++ {\n*** End Patch"}),
		}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			call("c3", "shell", map[string]any{"command": []string{"go", "test", "./..."}}),
		}},
		// Asks to leave the sandbox, which always goes to a human; the
		// headless driver declines, as codex exec does without a TTY.
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			call("c4", "shell", map[string]any{
				"command":       []string{"go", "mod", "download"},
				"escalate":      true,
				"justification": "Fetch module dependencies; the sandbox has no network.",
			}),
		}},
		agent.StubTurn{Text: "Fixed Sum's loop, which skipped the first element. go test ./... passes. I changed Sum in stats/stats.go."},
		// The finalizing call for result.schema.json.
		agent.StubTurn{Text: `{"status":"fixed","summary":"Sum skipped the first element; the loop now starts at 0.","files_changed":["stats/stats.go"],"tests_passed":true}`},
		// The driver's /review afterwards (#152): the reviewer reads the
		// diff it was handed and answers, then its findings are coerced.
		agent.StubTurn{Text: "The loop now starts at 0, which fixes Sum. Nothing else changed."},
		agent.StubTurn{Text: `{"findings":[],"verdict":"correct","summary":"Sum's loop now covers every element."}`},
	), nil
}
