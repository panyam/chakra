package codeq

import (
	"context"
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Scripted is a stand-in model for CI. It picks a tool from the question's
// wording and answers from what the tool returned. It has four habits, one
// for each step to correct:
//
//   - It keeps every row a tool returns unless its instructions say to
//     scope by module or leave out test files.
//   - Asked for a bare function name, it calls callers with that name. The
//     server refuses, and it repeats the call until a note says it's stuck.
//     Then it qualifies the name.
//   - It answers "which packages does X import" in prose with short names,
//     until a note asks for rows alone.
//
// So each step passes more in CI, with no model. Only a live run says what a
// step does for a real one.
func Scripted() agent.Provider { return scripted{} }

type scripted struct{}

func (s scripted) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return agent.NewStubProvider(s.reply(req)).Stream(ctx, req)
}

func (s scripted) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return agent.NewStubProvider(s.reply(req)).Generate(ctx, req)
}

var (
	askDefined   = regexp.MustCompile(`^What is the qualified name of the (?:function|type) (\w+)\?`)
	askImports   = regexp.MustCompile(`^Which packages does (\S+) import\?`)
	askImporters = regexp.MustCompile(`^Which packages (?:in (\S+) )?import ([^\s,?]+)`)
	askCallers   = regexp.MustCompile(`^Which functions (?:in (\S+) )?call (\S+?)(?: from (\S+))?\?`)
	askModule    = regexp.MustCompile(`^Which packages make up the module (\S+)\?`)
)

func (scripted) reply(req agent.ProviderRequest) agent.StubTurn {
	var question, lastTool string
	stuck, rowsAsked := false, false
	for _, m := range req.Messages {
		switch m.Role {
		case agent.RoleUser:
			if question == "" {
				question = m.Text
			}
		case agent.RoleTool:
			lastTool = m.Text
		case agent.RoleSystem:
			stuck = stuck || strings.Contains(m.Text, "Try a different approach")
			rowsAsked = rowsAsked || strings.Contains(m.Text, "one row per line")
		}
	}
	scope := strings.Contains(req.Instructions, `"in <module>"`)
	noTests := strings.Contains(req.Instructions, `"non-test"`) && strings.Contains(question, "non-test")

	var tool string
	var args map[string]string
	module := ""
	switch {
	case askDefined.MatchString(question):
		tool, args = "find", map[string]string{"name": askDefined.FindStringSubmatch(question)[1]}
	case askImports.MatchString(question):
		tool, args = "imports", map[string]string{"package": askImports.FindStringSubmatch(question)[1]}
	case askImporters.MatchString(question):
		m := askImporters.FindStringSubmatch(question)
		module, tool, args = m[1], "importers", map[string]string{"package": m[2]}
	case askCallers.MatchString(question):
		m := askCallers.FindStringSubmatch(question)
		fn := m[2]
		if m[3] != "" && stuck {
			fn = m[3] + "." + fn
		}
		module, tool, args = m[1], "callers", map[string]string{"function": fn}
	case askModule.MatchString(question):
		tool, args = "packages", map[string]string{"module": askModule.FindStringSubmatch(question)[1]}
	default:
		return agent.StubTurn{Text: "I can only answer questions about packages, imports and callers."}
	}

	if lastTool == "" || strings.Contains(lastTool, "needs a qualified name") {
		raw, _ := json.Marshal(args)
		return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: tool, Args: core.NewRawJSON(raw)}}}
	}

	var rows []string
	for _, line := range strings.Split(lastTool, "\n") {
		row, testOnly := strings.CutSuffix(strings.TrimSpace(line), " (test files only)")
		row, _, _ = strings.Cut(row, "  declared at ")
		if row == "" || row == "none" || testOnly && noTests {
			continue
		}
		if scope && module != "" && !strings.HasPrefix(row, module+"/") {
			continue
		}
		rows = append(rows, row)
	}
	if tool == "imports" && !rowsAsked {
		var short []string
		for _, r := range rows {
			short = append(short, path.Base(r))
		}
		return agent.StubTurn{Text: "It imports " + strings.Join(short, ", ") + "."}
	}
	return agent.StubTurn{Text: strings.Join(rows, "\n")}
}
