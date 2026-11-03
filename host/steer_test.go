package host

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// pokeExt offers a tool that calls back into the test while step 1 runs,
// which is when a user would steer.
type pokeExt struct {
	BaseExtension
	onCall func()
}

func (pokeExt) Name() string { return "poke" }
func (e pokeExt) Tools() (agent.ToolSource, error) {
	fs := agent.NewFuncSource()
	type in struct{}
	err := agent.AddFunc(fs, "poke", "", func(context.Context, in) (string, error) {
		e.onCall()
		return "poked", nil
	}, agent.ReadOnly())
	return fs, err
}

type inputLog struct {
	mu     sync.Mutex
	inputs []string // message, with " [steer]" when Label says so
}

func (l *inputLog) On(ev HostEvent) {
	if ev.Kind != HostUserInput {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := ev.Message
	if ev.Label == "steer" {
		s += " [steer]"
	}
	l.inputs = append(l.inputs, s)
}

func (l *inputLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.inputs...)
}

func pokeCall() agent.StubTurn {
	return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "poke", Args: core.NewRawJSON(json.RawMessage(`{}`))}}}
}

func lastUser(msgs []agent.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == agent.RoleUser {
			return msgs[i].Text
		}
	}
	return ""
}

func TestSteerDuringATurnReachesTheNextModelCall(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(pokeCall(), agent.StubTurn{Text: "done"}, agent.StubTurn{Text: "next"})
	log := &inputLog{}
	var app *App
	var steered bool
	var steerErr error
	ext := pokeExt{onCall: func() { steered, steerErr = app.Submit(context.Background(), "also check Mean", TurnOptions{}) }}
	app, err := NewApp(testConfig(ts.URL), io.Discard, strings.NewReader(""), WithProvider(stub), WithExtension(ext), WithObserver(log))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if s, err := app.Submit(context.Background(), "fix Sum", TurnOptions{}); s || err != nil {
		t.Fatalf("first Submit: steered %v, err %v; want a turn run", s, err)
	}
	if !steered || steerErr != nil {
		t.Fatalf("Submit during the turn: steered %v, err %v; want it queued", steered, steerErr)
	}
	reqs := stub.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model calls, want 2: the steer should join the turn, not start one", len(reqs))
	}
	if got := lastUser(reqs[1].Messages); got != "also check Mean" {
		t.Errorf("step 2's last user message = %q", got)
	}
	if got := strings.Join(log.all(), " | "); got != "fix Sum | also check Mean [steer]" {
		t.Errorf("user-input events: %s", got)
	}

	// It's in history, so the next turn sees it.
	if err := app.RunTurn(context.Background(), "thanks"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range stub.Requests()[2].Messages {
		if m.Role == agent.RoleUser && m.Text == "also check Mean" {
			found = true
		}
	}
	if !found {
		t.Error("the steer didn't make it into history")
	}
}

// lateProvider queues a steer while the turn's last model call is in
// flight, after the steer stage has run for it.
type lateProvider struct {
	*agent.StubProvider
	onCall func(n int)
	n      int
}

func (p *lateProvider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	p.n++
	p.onCall(p.n)
	return p.StubProvider.Stream(ctx, req)
}

func TestSteerAfterTheLastModelCallRunsNext(t *testing.T) {
	ts := startTestServer(t)
	log := &inputLog{}
	var app *App
	prov := &lateProvider{StubProvider: agent.NewStubProvider(agent.StubTurn{Text: "done"}, agent.StubTurn{Text: "on it"})}
	prov.onCall = func(n int) {
		if n == 1 {
			if s, err := app.Submit(context.Background(), "one more thing", TurnOptions{}); !s || err != nil {
				t.Errorf("late Submit: steered %v, err %v", s, err)
			}
		}
	}
	app, err := NewApp(testConfig(ts.URL), io.Discard, strings.NewReader(""), WithProvider(prov), WithObserver(log))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "fix Sum"); err != nil {
		t.Fatal(err)
	}
	reqs := prov.Requests()
	if len(reqs) != 2 || lastUser(reqs[1].Messages) != "one more thing" {
		t.Fatalf("the late steer didn't run as the next turn: %d calls", len(reqs))
	}
	if got := strings.Join(log.all(), " | "); got != "fix Sum | one more thing [steer]" {
		t.Errorf("user-input events: %s (the late steer should be announced once)", got)
	}
}

func TestSubmitWithNoTurnRunningRunsOne(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "hi"})
	app, err := NewApp(testConfig(ts.URL), io.Discard, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if s, err := app.Submit(context.Background(), "hello", TurnOptions{}); s || err != nil || len(stub.Requests()) != 1 {
		t.Fatalf("steered %v, err %v, calls %d", s, err, len(stub.Requests()))
	}
}
