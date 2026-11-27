package codeq

import (
	"context"
	"encoding/json"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Scripted is a stand-in model for CI. It picks a tool from the question's
// wording and answers from what the tool returned. Its habits copy what
// Haiku 5.5 did on the eval (#365), one for each step to correct, and one
// no step corrects:
//
//   - It keeps every row a tool returns unless its instructions say to
//     scope by module or leave out test files. So it names both functions
//     called Format when the question asks for one module's.
//   - Asked for a bare function name, it calls callers with that name. The
//     server refuses, and it repeats the call until a note says it's stuck.
//     Then it qualifies the name.
//   - It answers "which packages does X import" in prose with short names,
//     and a negation in prose that names the packages it left out too,
//     until a note asks for rows alone.
//   - On a multi-hop question it stops after one hop: direct callers, or
//     direct imports. No step of this ladder fixes that. Breaking a problem
//     into hops is mission 395's work.
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
	askDefined   = regexp.MustCompile(`^What is the qualified name of the (?:function|type) (\w+)(?: in (\S+))?\?`)
	askNone      = regexp.MustCompile(`^Which packages in (\S+) import nothing from (\S+?),`)
	askReach     = regexp.MustCompile(`^Which functions (?:in (\S+) )?reach (\S+) only through`)
	askAllCalls  = regexp.MustCompile(`^Which functions call (\S+), directly or through`)
	askDependsOn = regexp.MustCompile(`^Which packages in (\S+) does (\S+) depend on`)
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

	if m := askNone.FindStringSubmatch(question); m != nil {
		return answerNone(req, m[1], m[2], noTests, rowsAsked)
	}

	var tool string
	var args map[string]string
	module := ""
	switch {
	case askDefined.MatchString(question):
		m := askDefined.FindStringSubmatch(question)
		module, tool, args = m[2], "find", map[string]string{"name": m[1]}
	// The multi-hop questions get one hop's lookup, and its rows are the
	// answer.
	case askReach.MatchString(question):
		m := askReach.FindStringSubmatch(question)
		module, tool, args = m[1], "callers", map[string]string{"function": m[2]}
	case askAllCalls.MatchString(question):
		tool, args = "callers", map[string]string{"function": askAllCalls.FindStringSubmatch(question)[1]}
	case askDependsOn.MatchString(question):
		m := askDependsOn.FindStringSubmatch(question)
		module, tool, args = m[1], "imports", map[string]string{"package": m[2]}
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
	if tool == "imports" && askImports.MatchString(question) && !rowsAsked {
		var short []string
		for _, r := range rows {
			short = append(short, path.Base(r))
		}
		return agent.StubTurn{Text: "It imports " + strings.Join(short, ", ") + "."}
	}
	return agent.StubTurn{Text: strings.Join(rows, "\n")}
}

// answerNone answers "which packages in module import nothing from other". It
// lists module's packages, reads each one's imports in one step, and keeps
// the packages with no import from other. Until a note asks for rows alone,
// it answers in prose that names the other packages too.
func answerNone(req agent.ProviderRequest, module, other string, noTests, rowsAsked bool) agent.StubTurn {
	// results maps each tool call's argument to what the tool returned.
	args := map[string]string{}
	results := map[string]string{}
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			var in map[string]string
			_ = json.Unmarshal(tc.Args.Raw(), &in)
			args[tc.ID] = in["module"] + in["package"]
		}
		if m.Role == agent.RoleTool {
			results[args[m.ToolCallID]] = m.Text
		}
	}
	listed, ok := results[module]
	if !ok {
		raw, _ := json.Marshal(map[string]string{"module": module})
		return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "p1", Name: "packages", Args: core.NewRawJSON(raw)}}}
	}
	pkgs := strings.Fields(listed)
	var calls []agent.ToolCall
	for i, p := range pkgs {
		if _, seen := results[p]; !seen {
			raw, _ := json.Marshal(map[string]string{"package": p})
			calls = append(calls, agent.ToolCall{ID: "i" + strconv.Itoa(i), Name: "imports", Args: core.NewRawJSON(raw)})
		}
	}
	if len(calls) > 0 {
		return agent.StubTurn{ToolCalls: calls}
	}
	var none, some []string
	for _, p := range pkgs {
		uses := false
		for _, line := range strings.Split(results[p], "\n") {
			row, testOnly := strings.CutSuffix(strings.TrimSpace(line), " (test files only)")
			if strings.HasPrefix(row, other+"/") && !(testOnly && noTests) {
				uses = true
			}
		}
		if uses {
			some = append(some, p)
		} else {
			none = append(none, p)
		}
	}
	if rowsAsked {
		return agent.StubTurn{Text: strings.Join(none, "\n")}
	}
	return agent.StubTurn{Text: "These import nothing from " + other + ":\n- " + strings.Join(none, "\n- ") +
		"\n\nThese do:\n- " + strings.Join(some, "\n- ")}
}
