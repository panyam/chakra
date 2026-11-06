package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

// check is one behaviour the mission asks for, with the wiring that serves it.
// The exercise prints the wiring column, so the table doubles as the map from
// behaviour to hook, extension and tool.
type check struct {
	id        string
	behaviour string
	wiring    string
	run       func(r *runResult, fixture string) (bool, string)
}

var checks = []check{
	{
		id:        "tests_pass",
		behaviour: "the fixture's failing test passes after the run",
		wiring:    "ext/files edit + ext/exec shell",
		run: func(r *runResult, _ string) (bool, string) {
			if r.testsPassed {
				return true, ""
			}
			return false, lastLine(r.testOutput)
		},
	},
	{
		id:        "agents_md_loaded",
		behaviour: "AGENTS.md is in the instructions of every model call",
		wiring:    "agentsmd prompt section (#146)",
		run: func(r *runResult, fixture string) (bool, string) {
			rule := firstRule(filepath.Join(fixture, "AGENTS.md"))
			if len(r.requests) == 0 {
				return false, "no model requests recorded"
			}
			for i, req := range r.requests {
				if !strings.Contains(req.Instructions, rule) {
					return false, fmt.Sprintf("request %d's instructions lack %q", i+1, rule)
				}
			}
			return true, ""
		},
	},
	{
		id:        "subdir_agents_md_loaded",
		behaviour: "stats/AGENTS.md reaches the model once the agent works in stats/, not before",
		wiring:    "agentsmd middleware and step stage (#146)",
		run: func(r *runResult, fixture string) (bool, string) {
			rule := firstRule(filepath.Join(fixture, "stats", "AGENTS.md"))
			for i, req := range r.requests {
				if !strings.Contains(strings.Join(req.SystemTexts, "\n"), rule) {
					continue
				}
				if i == 0 {
					return false, "loaded before any tool call touched stats/"
				}
				return true, ""
			}
			return false, fmt.Sprintf("no request carried %q", rule)
		},
	},
	{
		id:        "test_files_untouched",
		behaviour: "AGENTS.md's rule holds: no *_test.go edited",
		wiring:    "model behaviour, given #146",
		run: func(r *runResult, _ string) (bool, string) {
			if len(r.testFilesChanged) == 0 {
				return true, ""
			}
			return false, "changed " + strings.Join(r.testFilesChanged, ", ")
		},
	},
	{
		id:        "shell_command",
		behaviour: "a command ran through the general shell tool",
		wiring:    "ext/exec shell tool, prefix rules (#154)",
		run:       toolUsed("shell"),
	},
	{
		id:        "escalation_asked",
		behaviour: "a command outside the sandbox asked for escalation",
		wiring:    "ApprovalClassifier on the shell tool (#154, on #153)",
		run: func(r *runResult, _ string) (bool, string) {
			for _, ev := range r.events {
				if ev.Kind == host.HostElicitRequest {
					return true, ""
				}
			}
			return false, "no ask raised"
		},
	},
	{
		id:        "patch_edit",
		behaviour: "the edit went through the multi-file patch envelope",
		wiring:    "ext/files apply_patch (#156)",
		run:       toolUsed("apply_patch"),
	},
	{
		id:        "skill_loaded",
		behaviour: "a skill was loaded from a local directory",
		wiring:    "host skill catalog fed from disk (#147)",
		run: func(r *runResult, _ string) (bool, string) {
			for _, ev := range r.events {
				if ev.Kind == host.HostSkillsLoaded && ev.Loaded > 0 {
					return true, ""
				}
			}
			return false, "no skills-loaded event with skills in it"
		},
	},
	{
		id:        "plan_published",
		behaviour: "update_plan was offered on every call, and every plan the model sent reached the surfaces",
		wiring:    "update_plan tool + plan event (#148)",
		// The model decides whether to plan. A one-line fix needs no plan,
		// and the tool's description says to skip it. So this checks the
		// wiring, and says when the model chose not to plan.
		run: func(r *runResult, _ string) (bool, string) {
			if len(r.requests) == 0 {
				return false, "no model requests recorded"
			}
			for i, req := range r.requests {
				// the structured-result call offers no tools at all
				if len(req.Tools) > 0 && !slices.Contains(req.Tools, "update_plan") {
					return false, fmt.Sprintf("request %d didn't offer update_plan", i+1)
				}
			}
			accepted, published := 0, 0
			for _, ev := range r.events {
				if ev.Kind != host.HostRunnerEvent {
					continue
				}
				switch e := ev.RunnerEvent; {
				case e.Kind == agent.EventToolEnd && e.ToolCall != nil && e.ToolCall.Name == "update_plan" && (e.ToolResult == nil || !e.ToolResult.IsError):
					accepted++
				case e.Kind == agent.EventPlan && e.Plan != nil:
					published++
				}
			}
			if accepted != published {
				return false, fmt.Sprintf("%d plans accepted, %d plan events", accepted, published)
			}
			if accepted == 0 {
				return true, "offered; the model didn't plan"
			}
			return true, ""
		},
	},
	{
		id:        "stuck_noticed",
		behaviour: "a call repeated with the same result was noticed, and the next call carried a note saying so",
		wiring:    "stuck detector step stage (#278, #285)",
		// The model decides whether it repeats itself. So a run with no
		// repeat passes and says so, and a repeat with no event fails.
		run: func(r *runResult, _ string) (bool, string) {
			for _, ev := range r.events {
				if ev.Kind != host.HostRunnerEvent || ev.RunnerEvent.Kind != agent.EventStuck || ev.RunnerEvent.Stuck == nil {
					continue
				}
				step, note := ev.RunnerEvent.Step, "You have "+ev.RunnerEvent.Stuck.String()
				if step < 1 || step > len(r.requests) {
					return false, fmt.Sprintf("stuck event at step %d, but %d requests recorded", step, len(r.requests))
				}
				if !slices.ContainsFunc(r.requests[step-1].SystemTexts, func(s string) bool { return strings.HasPrefix(s, note) }) {
					return false, fmt.Sprintf("step %d had no note starting %q", step, note)
				}
				return true, ""
			}
			if name, n := longestRepeat(r.toolCalls()); n >= 3 {
				return false, fmt.Sprintf("%s ran %d times in a row with no stuck event (did its results differ?)", name, n)
			}
			return true, "the model didn't repeat itself"
		},
	},
	{
		id:        "steered_mid_turn",
		behaviour: "input sent during a turn reached the model inside that turn",
		wiring:    "step-boundary input queue (#149)",
		run: func(r *runResult, _ string) (bool, string) {
			if r.steerTurnSeen {
				return false, "the steer waited and ran as a turn of its own"
			}
			for _, req := range r.requests {
				for _, t := range req.UserTexts {
					if t == steerText {
						return true, ""
					}
				}
			}
			return false, "the steer never reached the model"
		},
	},
	{
		id:        "review_findings",
		behaviour: "a review sub-agent reported findings on the change",
		wiring:    "review sub-agent over ext/git (#151, #152)",
		run: func(r *runResult, _ string) (bool, string) {
			for _, ev := range r.events {
				if ev.Kind == host.HostSubAgentEvent && strings.Contains(strings.ToLower(ev.SubAgent.Scope), "review") {
					return true, ""
				}
			}
			return false, "no review sub-agent ran"
		},
	},
	{
		id:        "structured_result",
		behaviour: "the run returned a result valid against result.schema.json",
		wiring:    "headless exec with an output schema (#145)",
		run: func(r *runResult, _ string) (bool, string) {
			if r.turn == nil {
				return false, "the turn failed: " + r.turnErr
			}
			if raw := string(r.turn.Structured.Raw()); raw == "" || raw == "null" {
				return false, "no structured result"
			}
			if r.turn.StructuredError != "" {
				return false, "not valid against the schema: " + r.turn.StructuredError
			}
			var v map[string]any
			if err := json.Unmarshal(r.turn.Structured.Raw(), &v); err != nil {
				return false, err.Error()
			}
			for _, k := range []string{"status", "summary", "files_changed", "tests_passed"} {
				if _, ok := v[k]; !ok {
					return false, "missing " + k
				}
			}
			return true, ""
		},
	},
}

func toolUsed(name string) func(*runResult, string) (bool, string) {
	return func(r *runResult, _ string) (bool, string) {
		for _, c := range r.toolCalls() {
			if c.Name == name {
				return true, ""
			}
		}
		return false, "no " + name + " call; tools used: " + toolNames(r.toolCalls())
	}
}

// longestRepeat finds the longest run of identical calls, by name and
// arguments, and returns the tool's name and the run's length.
func longestRepeat(calls []agent.ToolCall) (string, int) {
	best, name, run := 0, "", 0
	for i, c := range calls {
		if i > 0 && c.Name == calls[i-1].Name && string(c.Args.Raw()) == string(calls[i-1].Args.Raw()) {
			run++
		} else {
			run = 1
		}
		if run > best {
			best, name = run, c.Name
		}
	}
	return name, best
}

func toolNames(calls []agent.ToolCall) string {
	seen := map[string]bool{}
	var names []string
	for _, c := range calls {
		if !seen[c.Name] {
			seen[c.Name] = true
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// firstRule returns AGENTS.md's first bullet. The instructions must include
// that text.
func firstRule(path string) string {
	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "- ") {
			return strings.TrimPrefix(line, "- ")
		}
	}
	return string(data)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
