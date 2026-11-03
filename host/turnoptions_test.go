package host

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

type turnDoneLog struct{ results []*agent.TurnResult }

func (l *turnDoneLog) On(ev HostEvent) {
	if ev.Kind == HostTurnDone {
		l.results = append(l.results, ev.Result)
	}
}

// TestRunTurnWithSchemaReachesTheRunner checks the wiring: a per-turn schema
// reaches the Runner, and the structured answer arrives on turn-done. The
// coercion itself is tested in the agent package.
func TestRunTurnWithSchemaReachesTheRunner(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "done"}, agent.StubTurn{Text: `{"ok":true}`}, agent.StubTurn{Text: "plain"})
	log := &turnDoneLog{}
	app, err := NewApp(testConfig(ts.URL), io.Discard, strings.NewReader(""), WithProvider(stub), WithObserver(log))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	schema := core.NewRawJSON(json.RawMessage(`{"type":"object","required":["ok"]}`))
	if err := app.RunTurnWith(context.Background(), "go", TurnOptions{ResponseSchema: schema}); err != nil {
		t.Fatal(err)
	}
	if len(log.results) != 1 || string(log.results[0].Structured.Raw()) != `{"ok":true}` {
		t.Fatalf("turn-done results = %+v", log.results)
	}
	if err := app.RunTurn(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	if n := len(stub.Requests()); n != 3 {
		t.Errorf("a plain turn after a schema turn should make one call, not finalize: %d requests in all", n)
	}
}
