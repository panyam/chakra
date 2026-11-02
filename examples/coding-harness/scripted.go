package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/files"
	"github.com/panyam/mcpkit/core"
)

// scriptedProvider plays what a competent agent with today's tools does:
// read the code, fix the loop, run the tests, answer. A last turn answers the
// steer, which today runs as a second turn. As tickets land the script moves
// to their tools (apply_patch, shell, update_plan), so the scripted run keeps
// exercising the wiring each check names.
func scriptedProvider(fixture string) (agent.Provider, error) {
	src, err := os.ReadFile(filepath.Join(fixture, "stats.go"))
	if err != nil {
		return nil, err
	}
	call := func(id, name string, args any) agent.ToolCall {
		raw, _ := json.Marshal(args)
		return agent.ToolCall{ID: id, Name: name, Args: core.NewRawJSON(raw)}
	}
	return agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			call("c1", "read_file", map[string]any{"path": "stats.go"}),
		}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			call("c2", "edit_file", map[string]any{
				"path":        "stats.go",
				"expect_hash": files.Hash(string(src)),
				"edits":       []map[string]string{{"old": "for i := 1; i < len(xs); i++ {", "new": "for i := 0; i < len(xs); i++ {"}},
			}),
		}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			call("c3", "run_test", map[string]any{}),
		}},
		agent.StubTurn{Text: "Fixed Sum's loop, which skipped the first element. go test ./... passes."},
		agent.StubTurn{Text: fmt.Sprintf("I changed %s.", "Sum in stats.go")},
	), nil
}
