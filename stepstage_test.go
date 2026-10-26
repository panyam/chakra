package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// bigTool returns n bytes per call, so a turn's own tool results push it past
// a compaction budget, the case #7 is about.
func bigTool(n int) *FuncSource {
	src := NewFuncSource()
	AddFunc(src, "dump", "dumps a lot", func(context.Context, struct{}) (string, error) {
		return strings.Repeat("x", n), nil
	})
	return src
}

func dumpCalls(n int) []StubTurn {
	turns := make([]StubTurn, 0, n+1)
	for i := range n {
		id := string(rune('a' + i))
		turns = append(turns, StubTurn{ToolCalls: []ToolCall{{ID: id, Name: "dump", Args: core.NewRawJSON(json.RawMessage(`{}`))}}})
	}
	return append(turns, StubTurn{Text: "done"})
}

// requestHasOrphanToolResult reports a tool message whose call isn't in the
// assistant message right before the run of tool messages it belongs to.
func requestHasOrphanToolResult(msgs []Message) bool {
	for i, m := range msgs {
		if m.Role != RoleTool {
			continue
		}
		j := i - 1
		for j >= 0 && msgs[j].Role == RoleTool {
			j--
		}
		if j < 0 || msgs[j].Role != RoleAssistant || len(msgs[j].ToolCalls) == 0 {
			return true
		}
	}
	return false
}

func TestMidTurnCompactionKeepsALongTurnUnderBudget(t *testing.T) {
	summarizer := NewStubProvider(StubTurn{Text: "s1"}, StubTurn{Text: "s2"}, StubTurn{Text: "s3"}, StubTurn{Text: "s4"})
	compactor, _ := NewSummarizingCompactor(SummarizingConfig{Provider: summarizer, MaxTokens: 600, KeepRecent: 2})
	chat := NewStubProvider(dumpCalls(5)...)
	r, err := NewRunner(RunnerConfig{Provider: chat, Tools: bigTool(1000), Compactor: compactor, MaxSteps: 10})
	if err != nil {
		t.Fatal(err)
	}
	emit, events := collectEvents()
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "dump five times"}}, emit)
	if err != nil {
		t.Fatal(err)
	}

	midTurn := 0
	for _, ev := range *events {
		if ev.Kind == EventCompaction && ev.Step > 1 {
			midTurn++
		}
	}
	if midTurn == 0 {
		t.Fatalf("no compaction after step 1; events %v", kinds(*events))
	}
	est := CharTokenEstimator{}
	for i, req := range chat.Requests() {
		if n := est.Estimate(req.Messages); n > 600 {
			t.Errorf("step %d sent ~%d tokens, over the 600 budget", i+1, n)
		}
		if requestHasOrphanToolResult(req.Messages) {
			t.Errorf("step %d sent a tool result without its call", i+1)
		}
	}
	// The record keeps every message; compaction only changes what was sent.
	tools := 0
	for _, m := range res.Messages {
		if m.Role == RoleTool {
			tools++
		}
	}
	if tools != 5 || res.Text != "done" {
		t.Errorf("record has %d tool results and text %q, want all 5 and the answer", tools, res.Text)
	}
}

func TestShortTurnNeverCallsTheSummarizer(t *testing.T) {
	summarizer := NewStubProvider() // errors if called
	compactor, _ := NewSummarizingCompactor(SummarizingConfig{Provider: summarizer, MaxTokens: 100000})
	r, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(dumpCalls(3)...), Tools: bigTool(10), Compactor: compactor})
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(summarizer.Requests()); n != 0 {
		t.Errorf("summarizer called %d times under budget", n)
	}
}

// When even the kept tail is over budget, compacting again would only
// summarize the summary, so the turn ends and keeps what it did.
func TestTurnThatCannotFitEndsWithContextBudget(t *testing.T) {
	summarizer := NewStubProvider(StubTurn{Text: "s"}, StubTurn{Text: "s"}, StubTurn{Text: "s"})
	compactor, _ := NewSummarizingCompactor(SummarizingConfig{Provider: summarizer, MaxTokens: 100, KeepRecent: 2})
	r, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(dumpCalls(4)...), Tools: bigTool(2000), Compactor: compactor, MaxSteps: 10})
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if !errors.Is(err, ErrContextBudget) {
		t.Fatalf("err = %v, want ErrContextBudget", err)
	}
	if res == nil || len(res.Messages) == 0 {
		t.Fatalf("no partial result to keep: %+v", res)
	}
	if last := res.Messages[len(res.Messages)-1]; last.Role == RoleAssistant && len(last.ToolCalls) > 0 {
		t.Errorf("partial result ends on an unanswered tool call")
	}
}

// Step 1 sends what the user gave it, as compaction always did.
func TestFirstStepIsNotHeldToTheFloor(t *testing.T) {
	summarizer := NewStubProvider(StubTurn{Text: "s"})
	compactor, _ := NewSummarizingCompactor(SummarizingConfig{Provider: summarizer, MaxTokens: 10, KeepRecent: 1})
	r, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "ok"}), Compactor: compactor})
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: strings.Repeat("long ", 100)}}, nil)
	if err != nil || res.Text != "ok" {
		t.Fatalf("res=%+v err=%v, want the first step sent as before", res, err)
	}
}

func TestStepBudgetNoteShowsNearTheCapAndIsNotKept(t *testing.T) {
	chat := NewStubProvider(dumpCalls(4)...)
	r, _ := NewRunner(RunnerConfig{Provider: chat, Tools: bigTool(1), MaxSteps: 5, StepStages: []StepStage{StepBudgetStage(2)}})
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	notes := func(msgs []Message) []string {
		var out []string
		for _, m := range msgs {
			if m.Role == RoleSystem && strings.HasPrefix(m.Text, "Step ") {
				out = append(out, m.Text)
			}
		}
		return out
	}
	reqs := chat.Requests()
	for i, req := range reqs {
		got := notes(req.Messages)
		step := i + 1
		switch {
		case 5-step > 2 && len(got) != 0:
			t.Errorf("step %d has a note with %d steps left: %v", step, 5-step, got)
		case 5-step <= 2 && len(got) != 1:
			t.Errorf("step %d has %d notes, want exactly this step's: %v", step, len(got), got)
		}
	}
	if got := notes(reqs[4].Messages); len(got) != 1 || !strings.Contains(got[0], "last step") {
		t.Errorf("last step's note = %v", got)
	}
	if len(notes(res.Messages)) != 0 {
		t.Error("a request-scoped note reached the record")
	}
}

func TestScopesDecideWhatIsKept(t *testing.T) {
	note := func(name string, scope StepScope) StepStage {
		return StepStage{Name: name, Scope: scope, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
			if scope != ScopeRequest && info.Step != 1 {
				return v, nil
			}
			v.Messages = append(v.Messages, Message{Role: RoleSystem, Text: name})
			return v, nil
		}}
	}
	chat := NewStubProvider(dumpCalls(1)...)
	r, _ := NewRunner(RunnerConfig{Provider: chat, Tools: bigTool(1), StepStages: []StepStage{
		note("request", ScopeRequest), note("turn", ScopeTurn), note("session", ScopeSession),
	}})
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	texts := func(msgs []Message) string {
		var b []string
		for _, m := range msgs {
			if m.Role == RoleSystem {
				b = append(b, m.Text)
			}
		}
		return strings.Join(b, ",")
	}
	reqs := chat.Requests()
	// Scope order, not registration order: session, then turn, then request.
	if got := texts(reqs[0].Messages); got != "session,turn,request" {
		t.Errorf("step 1 sent %q", got)
	}
	// Step 2: session and turn messages carried over, request one re-added once.
	if got := texts(reqs[1].Messages); got != "session,turn,request" {
		t.Errorf("step 2 sent %q", got)
	}
	if got := texts(res.Messages); got != "session" {
		t.Errorf("record kept %q, want only the session stage's message", got)
	}
}

func TestSessionStageMayOnlyAppend(t *testing.T) {
	rewrite := StepStage{Name: "bad", Scope: ScopeSession, Run: func(_ context.Context, _ StepInfo, v StepView) (StepView, error) {
		v.Messages = v.Messages[1:]
		return v, nil
	}}
	r, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "x"}), StepStages: []StepStage{rewrite}})
	_, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "a"}, {Role: RoleUser, Text: "b"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "may only append") {
		t.Fatalf("err = %v, want the append-only refusal", err)
	}
}
