package host

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// TestRendererSubAgentGutter pins the border-left tree gutter (issue 1063 B4).
// Depth drives the number of gutter segments, the scope is tagged, and a tool
// call shows name + compact args.
func TestRendererSubAgentGutter(t *testing.T) {
	var out strings.Builder
	r := newRenderer(&out, false) // color off strips ANSI so the gutter glyph is asserted directly

	r.subAgent(agent.SubAgentEvent{Scope: "research", Depth: 1, Event: agent.Event{
		Kind:     agent.EventToolBegin,
		ToolCall: &agent.ToolCall{Name: "grep", Args: core.NewRawJSON(json.RawMessage(`{"q":"retry"}`))},
	}})
	r.subAgent(agent.SubAgentEvent{Scope: "research/inner", Depth: 2, Event: agent.Event{
		Kind:   agent.EventTurnEnd,
		Result: &agent.TurnResult{Text: "found 3"},
	}})

	got := out.String()
	if !strings.Contains(got, "│ [research] · grep(") || !strings.Contains(got, `"q":"retry"`) {
		t.Fatalf("depth-1 gutter/tool line wrong:\n%q", got)
	}
	if !strings.Contains(got, "│ │ [research/inner] → found 3") {
		t.Fatalf("depth-2 line should carry two gutter segments:\n%q", got)
	}
}

// TestEnvColorEnabled pins the surface-agnostic env decision (issue 1063 E2).
// NO_COLOR present (any value) or TERM=dumb turns color off, and a normal
// terminal keeps it on.
func TestEnvColorEnabled(t *testing.T) {
	cases := []struct {
		name       string
		noColorSet bool
		noColorVal string
		term       string
		want       bool
	}{
		{"normal terminal", false, "", "xterm-256color", true},
		{"NO_COLOR=1", true, "1", "xterm-256color", false},
		{"NO_COLOR empty still off", true, "", "xterm-256color", false},
		{"TERM=dumb", false, "", "dumb", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.noColorSet {
				t.Setenv("NO_COLOR", c.noColorVal)
			} else {
				os.Unsetenv("NO_COLOR")
			}
			t.Setenv("TERM", c.term)
			if got := envColorEnabled(); got != c.want {
				t.Fatalf("envColorEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestRendererColorOffStripsANSI pins that a color-off renderer emits no ANSI
// escapes. --no-color, NO_COLOR and TERM=dumb all resolve to a color-off
// renderer.
func TestRendererColorOffStripsANSI(t *testing.T) {
	var out strings.Builder
	r := newRenderer(&out, false)
	r.turnDone(&agent.TurnResult{Steps: 1})
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("color-off render leaked ANSI: %q", out.String())
	}
}

// TestRendererToolCancelledDistinctFromError pins that a user cancel
// renders as an interrupt, not a failure.
func TestRendererToolCancelledDistinctFromError(t *testing.T) {
	var out strings.Builder
	r := newRenderer(&out, false)
	r.handle(agent.Event{
		Kind:     agent.EventToolCancelled,
		Step:     1,
		ToolCall: &agent.ToolCall{ID: "c1", Name: "slow"},
		Reason:   "cancelled by user",
	})
	got := out.String()
	if !strings.Contains(got, "slow cancelled: cancelled by user") {
		t.Fatalf("tool-cancelled render = %q", got)
	}
	if strings.Contains(got, "failed") {
		t.Fatalf("tool-cancelled rendered as a failure: %q", got)
	}
}

// Imagine a host message arriving while the model's text is mid-line. A
// TurnEnder's announcement does this before the turn's end marker. The
// message starts on a line of its own.
func TestAHostMessageStartsItsOwnLine(t *testing.T) {
	var b strings.Builder
	r := newRenderer(&b, false)
	r.handle(agent.Event{Kind: agent.EventTextDelta, Text: "done"})
	r.On(HostEvent{Kind: HostMessage, Message: "git: this turn changed 1 file (+1 -0)"})
	if got := b.String(); got != "done\ngit: this turn changed 1 file (+1 -0)\n" {
		t.Errorf("rendered %q", got)
	}
}

// A plan renders as a checklist under the model's line, whatever was mid-line.
func TestAPlanRendersAsAChecklist(t *testing.T) {
	var b strings.Builder
	r := newRenderer(&b, false)
	r.handle(agent.Event{Kind: agent.EventTextDelta, Text: "starting"})
	r.handle(agent.Event{Kind: agent.EventPlan, Plan: &agent.Plan{Steps: []agent.PlanStep{{Step: "read", Status: agent.PlanCompleted}, {Step: "fix", Status: agent.PlanInProgress}, {Step: "test", Status: agent.PlanPending}}}})
	if got := b.String(); got != "starting\nplan:\n  [x] read\n  [~] fix\n  [ ] test\n" {
		t.Errorf("rendered %q", got)
	}
}

// An unrouted step prints nothing, so the TUI shows what it did before
// every step started emitting (#262).
func TestRendererSkipsUnnamedStep(t *testing.T) {
	var out strings.Builder
	r := newRenderer(&out, false)
	r.handle(agent.Event{Kind: agent.EventTextDelta, Text: "looking"})
	r.handle(agent.Event{Kind: agent.EventStep, Step: 1, Usage: &agent.Usage{InputTokens: 5}})
	r.handle(agent.Event{Kind: agent.EventTextDelta, Text: " still"})
	if got, want := out.String(), "looking still"; got != want {
		t.Fatalf("render = %q, want %q", got, want)
	}
}

// A routed step prints which connection answered it (#215).
func TestRendererStepLine(t *testing.T) {
	var out strings.Builder
	r := newRenderer(&out, false)
	r.handle(agent.Event{Kind: agent.EventTextDelta, Text: "looking"})
	r.handle(agent.Event{Kind: agent.EventStep, Step: 2, Model: "mini"})
	if got, want := out.String(), "looking\n↳ step 2 on mini\n"; got != want {
		t.Fatalf("render = %q, want %q", got, want)
	}
}

func TestRendererShowsAStuckRun(t *testing.T) {
	var out strings.Builder
	r := newRenderer(&out, false)
	r.handle(agent.Event{Kind: agent.EventStuck, Step: 4, Stuck: &agent.StuckFinding{Pattern: agent.StuckRepeat, Count: 3, Tools: []string{"run_tests"}}})
	if got := out.String(); !strings.Contains(got, "stuck: repeated run_tests 3 times with the same result") {
		t.Fatalf("render = %q", got)
	}
}
