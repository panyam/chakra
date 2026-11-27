package main

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/examples/agents/copilot"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay"
)

// reader is a stand-in model. In each turn it calls read_board once, then
// answers "ok". A turn starts at a user message, or at the trigger's
// instructions, which a proactive turn adds as a system message. It records every request, so the test can read what reached
// the model.
type reader struct {
	mu   sync.Mutex
	reqs []agent.ProviderRequest
}

func (r *reader) turn(req agent.ProviderRequest) agent.StubTurn {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	lastUser := -1
	for i, m := range req.Messages {
		if m.Role == agent.RoleUser || m.Role == agent.RoleSystem && m.Text == copilot.DoneCheck {
			lastUser = i
		}
	}
	for _, m := range req.Messages[lastUser+1:] {
		if m.Role == agent.RoleTool {
			return agent.StubTurn{Text: "ok"}
		}
	}
	return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "r1", Name: "read_board", Args: core.NewRawJSON([]byte(`{}`))}}}
}

func (r *reader) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return agent.NewStubProvider(r.turn(req)).Stream(ctx, req)
}

func (r *reader) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return agent.NewStubProvider(r.turn(req)).Generate(ctx, req)
}

// saw reports whether a request from the nth on shows text in a system note.
func (r *reader) saw(from int, text string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, req := range r.reqs[min(from, len(r.reqs)):] {
		for _, m := range req.Messages {
			if m.Role == agent.RoleSystem && strings.Contains(m.Text, text) {
				return true
			}
		}
	}
	return false
}

func (r *reader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

type observer func(host.HostEvent)

func (o observer) On(e host.HostEvent) { o(e) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A page joins the board's room and offers read_board. A rename the user
// makes while a turn's read_board runs reaches the agent before its next
// model call, since the app marks the event critical. Then the page moves a
// card with open items to Done. The agent hears it as card.moved, the
// done-check trigger starts a turn, and the turn reads the board through the
// page's tool.
func TestThePageReachesTheAgentThroughItsRoom(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, err := newApp("http://agent.invalid", nil) // a Go page sends no Origin
	if err != nil {
		t.Fatal(err)
	}
	appSrv := httptest.NewServer(app)
	defer appSrv.Close()
	defer app.close()

	model := &reader{}
	var fired, done atomic.Int32
	a, err := buildApp(appSrv.URL, model, host.WithObserver(observer(func(e host.HostEvent) {
		switch e.Kind {
		case host.HostTriggerFired:
			if e.Label == "done-check" {
				fired.Add(1)
			}
		case host.HostTurnDone, host.HostTurnFailed:
			done.Add(1)
		}
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	token, _, err := app.local.Signer().MintRoomToken(ctx, subject, room)
	if err != nil {
		t.Fatal(err)
	}
	page, err := rooms.JoinPage(ctx, massrelay.Dial(appSrv.URL+"/relay", token), room,
		rooms.TrustBridges(app.local.BridgeSubjects()...))
	if err != nil {
		t.Fatal(err)
	}
	defer page.Leave()
	var reads atomic.Int32
	var renameMidTurn atomic.Bool
	page.RegisterTool(rooms.PageTool{
		Name: "read_board", Description: "Read the board.", ReadOnly: true,
		InputSchema: map[string]any{"type": "object"},
		Execute: func(context.Context, rooms.PageCall) (core.ToolResult, error) {
			reads.Add(1)
			if renameMidTurn.Load() {
				_ = page.Emit("card.renamed", map[string]string{"card": "C2", "from": "Ship v2", "to": "Ship v2.1"})
				// The event crosses the relay and the event stream; the
				// read takes long enough for it to arrive.
				time.Sleep(500 * time.Millisecond)
			}
			return core.ToolResult{Content: []core.Content{{Type: "text", Text: "C2 | Done | Ship v2 | checklist 1/3"}}}, nil
		},
	})
	page.SetAgentsAllowed(true)

	// The host opens its event streams on its own schedule. A rename between
	// user turns shows when they are live: the next turn's context carries
	// it.
	waitFor(t, "the page's events to reach a turn", func() bool {
		from := model.count()
		_ = page.Emit("card.renamed", map[string]string{"card": "C7", "from": "Launch plan", "to": "Launch plan v2"})
		time.Sleep(100 * time.Millisecond)
		_ = a.RunTurn(ctx, "hello")
		return model.saw(from, `The user renamed C7 from "Launch plan" to "Launch plan v2".`)
	})
	if reads.Load() == 0 {
		t.Fatal("the agent's turns never reached the page's read_board")
	}
	if fired.Load() != 0 {
		t.Fatalf("the trigger fired %d times on renames", fired.Load())
	}

	// The user renames C2 while the agent's read_board runs in a typed
	// turn. The app marks card.renamed critical, so the rename reaches the
	// turn's next model call, after the read's result.
	from := model.count()
	renameMidTurn.Store(true)
	_ = a.RunTurn(ctx, "Tidy up the board.")
	renameMidTurn.Store(false)
	if !model.renamedMidTurn(from, "Tidy up the board.", `The user renamed C2 from "Ship v2" to "Ship v2.1".`) {
		t.Error("the rename made during the turn's read_board never reached the turn's next model call")
	}

	from, readsBefore, turnsBefore := model.count(), reads.Load(), done.Load()
	if err := page.Emit("card.moved", map[string]string{"card": "C2", "title": "Ship v2", "from": "Doing", "to": "Done"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the done-check turn to finish", func() bool { return fired.Load() > 0 && done.Load() > turnsBefore })
	time.Sleep(300 * time.Millisecond)
	if got := fired.Load(); got != 1 {
		t.Errorf("the done-check trigger fired %d times, want 1", got)
	}
	if reads.Load() == readsBefore {
		t.Error("the done-check turn never called the page's read_board")
	}
	if !model.saw(from, "The user moved C2 (Ship v2) from Doing to Done.") {
		t.Error("the done-check turn's context lacks the move, in the app's template")
	}
}

// renamedMidTurn reports whether a request from the nth on has note after
// the tool result that follows the user message input.
func (r *reader) renamedMidTurn(from int, input, note string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, req := range r.reqs[min(from, len(r.reqs)):] {
		at := slices.IndexFunc(req.Messages, func(m agent.Message) bool { return m.Role == agent.RoleUser && m.Text == input })
		if at < 0 {
			continue
		}
		tool := false
		for _, m := range req.Messages[at:] {
			tool = tool || m.Role == agent.RoleTool
			if tool && m.Role == agent.RoleSystem && m.Text == note {
				return true
			}
		}
	}
	return false
}
