package copilot

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/mcpkit/core"
)

// Each step passes more sessions on the scripted model.
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
	if want := []int{0, 2, 4, 6, 7, 8}; !slices.Equal(got, want) {
		t.Errorf("passed per step = %v, want %v\n%s", got, want, r.Table())
	}
}

func callInfo(name, args string) agent.ToolCallInfo {
	return agent.ToolCallInfo{Call: agent.ToolCall{Name: name, Args: core.NewRawJSON([]byte(args))}}
}

func ok(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
	res := core.TextResult("done")
	return &res, nil
}

// The guard refuses an edit to a card the user changed after the agent's
// last read, as a free result naming the change, and lets it through once
// the agent has read the board again.
func TestStaleGuardRefusesAnEditTheAgentHasNotSeen(t *testing.T) {
	b := &board{}
	b.reset(nil)
	src, err := b.tools()
	if err != nil {
		t.Fatal(err)
	}
	read := func() {
		if _, err := src.Call(context.Background(), "read_board", map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if _, err := b.staleGuard(context.Background(), callInfo("rename_card", `{"card":"C7","title":"x"}`), ok); err != nil {
		t.Fatalf("edit with nothing changed: %v", err)
	}
	b.userDo(action{Kind: "rename", Card: "C7", To: "Launch plan v2"})
	_, err = b.staleGuard(context.Background(), callInfo("rename_card", `{"card":"c7","title":"x"}`), ok)
	var free *agent.FreeResultError
	if !errors.As(err, &free) || !strings.Contains(free.Text, `"Launch plan v2"`) {
		t.Fatalf("edit after the user's change = %v, want a free result naming the new title", err)
	}
	if _, err := b.staleGuard(context.Background(), callInfo("move_card", `{"card":"C1","column":"Done"}`), ok); err != nil {
		t.Errorf("an edit to another card was refused: %v", err)
	}
	read()
	if _, err := b.staleGuard(context.Background(), callInfo("rename_card", `{"card":"C7","title":"x"}`), ok); err != nil {
		t.Errorf("edit after re-reading: %v", err)
	}
}

// The page clock plays an action timed "before" a call ahead of the rest
// of the chain, and one timed "after" once the call has returned, on the
// nth call only.
func TestClockPlaysTimedActionsAroundTheNthCall(t *testing.T) {
	b := &board{}
	b.reset([]timed{
		{Before: "move_card", N: 2, Do: action{Kind: "rename", Card: "C1", To: "before"}},
		{After: "move_card", N: 2, Do: action{Kind: "rename", Card: "C2", To: "after"}},
	})
	var seen []string
	next := func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
		c1, _ := b.get("C1")
		c2, _ := b.get("C2")
		seen = append(seen, c1.Title+"/"+c2.Title)
		return ok(context.Background(), agent.ToolCallInfo{})
	}
	for range 2 {
		if _, err := b.clock(context.Background(), callInfo("move_card", `{}`), next); err != nil {
			t.Fatal(err)
		}
	}
	c2, _ := b.get("C2")
	if want := []string{"Fix login/Ship v2", "before/Ship v2"}; !slices.Equal(seen, want) || c2.Title != "after" {
		t.Errorf("the call saw %v and C2 ended %q; want %v and \"after\"", seen, c2.Title, want)
	}
}

// A user change during a turn reaches the next model call as a note, once.
// A change between turns doesn't, since page events carry those.
func TestMidTurnNotesCarryChangesMadeDuringTheTurn(t *testing.T) {
	b := &board{}
	b.reset(nil)
	stage := b.midTurnNotes()
	run := func() []agent.Message {
		v, err := stage.Run(context.Background(), agent.StepInfo{}, agent.StepView{})
		if err != nil {
			t.Fatal(err)
		}
		return v.Messages
	}
	b.userDo(action{Kind: "move", Card: "C2", To: "Done"})
	b.turnStarted()
	if got := run(); len(got) != 0 {
		t.Errorf("a change before the turn gave notes %v", got)
	}
	b.userDo(action{Kind: "move", Card: "C3", To: "To do"})
	got := run()
	if len(got) != 1 || got[0].Role != agent.RoleSystem || !strings.Contains(got[0].Text, "moved C3 (Update docs) from Doing to To do") {
		t.Errorf("notes = %+v, want one system note about C3", got)
	}
	if again := run(); len(again) != 0 {
		t.Errorf("the note came back on the next call: %v", again)
	}
}

// The graders catch an agent edit over a mid-turn user change, and a
// trigger that fired twice for one move.
func TestGradersCatchOverwritesAndDoubleFirings(t *testing.T) {
	b := &board{}
	b.reset(nil)
	src, err := b.tools()
	if err != nil {
		t.Fatal(err)
	}
	b.turnStarted()
	b.userDo(action{Kind: "move", Card: "C2", To: "To do"})
	if _, err := src.Call(context.Background(), "move_card", map[string]any{"card": "C2", "column": "Done"}); err != nil {
		t.Fatal(err)
	}
	if s := keptScore(b).Score(eval.Result{}); s.Pass || !strings.Contains(s.Detail, "C2's column") {
		t.Errorf("kept score = %+v, want a fail naming C2's column", s)
	}
	s := sessions[slices.IndexFunc(sessions, func(s session) bool { return s.trigger })]
	b.fired = 2
	if got := triggerScore(b, s).Score(eval.Result{}); got.Pass {
		t.Errorf("two firings for one move passed: %+v", got)
	}
	b.fired = 1
	if got := triggerScore(b, s).Score(eval.Result{}); !got.Pass {
		t.Errorf("one firing failed: %+v", got)
	}
}
