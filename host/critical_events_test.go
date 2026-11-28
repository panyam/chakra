package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// midTurnProvider delivers events from inside its first model call, as a
// page does when the user acts while the agent works, and then answers from
// the stub.
type midTurnProvider struct {
	*agent.StubProvider
	app     *App
	deliver func(*App)
	calls   int
}

func (p *midTurnProvider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	p.calls++
	if p.calls == 1 {
		p.deliver(p.app)
	}
	return p.StubProvider.Stream(ctx, req)
}

// An event whose hint says priority "critical" reaches the model before its
// next call in the same turn. Any other event waits for the next turn's
// start, as before (#383).
func TestACriticalEventReachesTheNextModelCallMidTurn(t *testing.T) {
	ts := startTestServer(t)
	// The first call asks for a tool that doesn't exist, so the turn takes
	// a second step.
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "no_such_tool", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: "done"},
		agent.StubTurn{Text: "next"},
	)
	prov := &midTurnProvider{StubProvider: stub, deliver: func(app *App) {
		deliverHinted(t, app, "card.moved", map[string]any{"priority": "critical", "template": "user moved {{card}}"}, `{"card":"C2"}`)
		deliverHinted(t, app, "page.scrolled", map[string]any{"template": "user scrolled to {{to}}"}, `{"to":"bottom"}`)
		if err := app.Settle(context.Background()); err != nil {
			t.Error(err)
		}
	}}
	app, err := NewApp(testConfig(ts.URL), &syncWriter{}, strings.NewReader(""), WithProvider(prov))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	prov.app = app

	if err := app.RunTurn(context.Background(), "tidy the board"); err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model calls in the first turn = %d, want 2", len(reqs))
	}
	if n := len(systemTexts(reqs[1], "user moved C2")); n != 1 {
		t.Errorf("the critical event reached the second call %d times, want once", n)
	}
	if n := len(systemTexts(reqs[1], "user scrolled")); n != 0 {
		t.Errorf("a medium event reached the model mid-turn")
	}
	if err := app.RunTurn(context.Background(), "and now?"); err != nil {
		t.Fatal(err)
	}
	next := stub.Requests()[2]
	if n := len(systemTexts(next, "user scrolled to bottom")); n != 1 {
		t.Errorf("the medium event reached the next turn %d times, want once", n)
	}
	if n := len(systemTexts(next, "user moved C2")); n != 1 {
		t.Errorf("the critical event appears %d times in the next turn, want once, from the record", n)
	}
}

// A critical event reaches a trigger's turn as well. Say the user drags C2
// to Done and the trigger's turn starts. The user then takes C3 back while
// that turn runs. The next model call in the turn hears about C3 (#400).
func TestACriticalEventReachesATriggersTurn(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Triggers = []TriggerConfig{{Event: "card.done", Instructions: "Check the card.", Label: "done-check"}}
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "no_such_tool", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: "checked"},
	)
	prov := &midTurnProvider{StubProvider: stub, deliver: func(app *App) {
		before := app.streams.consumed.Load()
		deliverHinted(t, app, "card.moved", map[string]any{"priority": "critical", "template": "user moved {{card}}"}, `{"card":"C3"}`)
		deadline := time.Now().Add(2 * time.Second)
		for app.streams.consumed.Load() == before && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}}
	app, err := NewApp(cfg, &syncWriter{}, strings.NewReader(""), WithProvider(prov))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	prov.app = app

	deliverHinted(t, app, "card.done", map[string]any{}, `{"card":"C2"}`)
	if err := app.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model calls in the trigger's turn = %d, want 2", len(reqs))
	}
	if n := len(systemTexts(reqs[1], "user moved C3")); n != 1 {
		t.Errorf("the critical event reached the turn's second call %d times, want once", n)
	}
}
