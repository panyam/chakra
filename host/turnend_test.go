package host

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
)

// judgeExt contributes turn-end hooks and records what its TurnEnder hears.
type judgeExt struct {
	BaseExtension
	hooks []agent.TurnEndHook

	mu        sync.Mutex
	endResult *agent.TurnResult
	endErr    error
}

func (*judgeExt) Name() string                        { return "judge" }
func (e *judgeExt) TurnEndHooks() []agent.TurnEndHook { return e.hooks }

func (e *judgeExt) TurnEnd(_ context.Context, result *agent.TurnResult, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.endResult, e.endErr = result, err
}

// The hook refuses the first answer, so the model is asked again and the
// second answer is the turn's.
func TestExtensionTurnEndHookJudgesTheAnswer(t *testing.T) {
	calls := 0
	ext := &judgeExt{hooks: []agent.TurnEndHook{func(_ context.Context, info agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
		calls++
		if info.Text == "done" {
			return agent.TurnEndVerdict{Continue: "the tests are still red"}, nil
		}
		return agent.TurnEndVerdict{}, nil
	}}}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "done"}, agent.StubTurn{Text: "fixed it"})
	app := extApp(t, stub, ext)

	if err := app.RunTurn(context.Background(), "fix the tests"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("hook ran %d times, want 2", calls)
	}
	last := app.history[len(app.history)-1]
	if last.Role != agent.RoleAssistant || last.Text != "fixed it" {
		t.Fatalf("last message = %+v, want the second answer", last)
	}
}

// A hook's Stop ends the turn with ErrTurnStopped. The turn keeps its
// messages with the refused answer last, as a step-capped turn does, and the
// next turn sees them.
func TestStoppedTurnStaysInHistory(t *testing.T) {
	ext := &judgeExt{hooks: []agent.TurnEndHook{func(context.Context, agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
		return agent.TurnEndVerdict{Stop: "same draft again"}, nil
	}}}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "draft"}, agent.StubTurn{Text: "next"})
	var out strings.Builder
	ts := startTestServer(t)
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub), WithExtension(ext))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	err = app.RunTurn(context.Background(), "translate this")
	if !errors.Is(err, agent.ErrTurnStopped) {
		t.Fatalf("want ErrTurnStopped, got %v", err)
	}
	if len(app.history) != 2 {
		t.Fatalf("stopped turn kept %d messages, want 2: %+v", len(app.history), app.history)
	}
	if app.history[0].Text != "translate this" || app.history[1].Text != "draft" {
		t.Fatalf("history = %+v, want the user message then the refused answer", app.history)
	}
	if !strings.Contains(out.String(), "same draft again") {
		t.Fatalf("transcript missing the stop reason:\n%s", out.String())
	}

	ext.mu.Lock()
	endResult, endErr := ext.endResult, ext.endErr
	ext.mu.Unlock()
	if endResult == nil || !errors.Is(endErr, agent.ErrTurnStopped) {
		t.Fatalf("TurnEnder heard (%+v, %v), want the partial result and ErrTurnStopped", endResult, endErr)
	}

	if err := app.RunTurn(context.Background(), "try again"); !errors.Is(err, agent.ErrTurnStopped) {
		t.Fatalf("second turn: want ErrTurnStopped, got %v", err)
	}
	reqs := stub.Requests()
	next := reqs[len(reqs)-1].Messages
	if len(next) != 3 || next[1].Text != "draft" || next[2].Text != "try again" {
		t.Fatalf("next turn sent %+v, want the stopped turn then the new input", next)
	}
}
