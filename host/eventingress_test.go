package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

type roomData struct {
	Room string `json:"room"`
	V    int    `json:"v"`
}

// roomServer serves room.event, whose subscriptions need a room argument
// and match only that room's events. With needsAttach set, a subscription
// is refused until the session has called the attach tool, the way a shared
// room refuses an early stream. Its log records attach calls and accepted
// subscriptions in order.
type roomServer struct {
	ts       *httptest.Server
	yield    func(context.Context, roomData) error
	mu       sync.Mutex
	log      []string
	attached bool
	failNext bool
}

func (r *roomServer) record(s string) {
	r.mu.Lock()
	r.log = append(r.log, s)
	r.mu.Unlock()
}

func (r *roomServer) entries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

func startRoomServer(t *testing.T, needsAttach bool) *roomServer {
	t.Helper()
	r := &roomServer{}
	srv := testutil.NewTestServer()
	src, yield := events.NewYieldingSource[roomData](events.EventDef{
		Name:        "room.event",
		Description: "something happened in a room",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"room": map[string]any{"type": "string"}},
			"required":   []any{"room"},
		},
		Match: func(_ events.HookContext, ev events.Event, params map[string]any) bool {
			var d roomData
			_ = json.Unmarshal(ev.Data, &d)
			return d.Room == params["room"]
		},
		OnSubscribe: func(_ events.HookContext, params map[string]any) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if needsAttach && !r.attached {
				return errors.New("attach before subscribing")
			}
			r.log = append(r.log, "subscribe "+params["room"].(string))
			return nil
		},
	})
	r.yield = yield
	events.Register(events.Config{Sources: []events.EventSource{src}, Server: srv, UnsafeAnonymousPrincipal: "room-test"})
	srv.Register(core.TextTool[struct{}]("attach", "attach the session to the room",
		func(core.ToolContext, struct{}) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.failNext {
				r.failNext = false
				return "", errors.New("room is closed")
			}
			r.attached = true
			r.log = append(r.log, "attach")
			return "attached", nil
		}))
	r.ts = httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(r.ts.Close)
	return r
}

// warnLog collects session warnings.
type warnLog struct {
	mu    sync.Mutex
	warns []string
}

func (w *warnLog) On(ev HostEvent) {
	if ev.Kind == HostSessionWarn {
		w.mu.Lock()
		w.warns = append(w.warns, ev.Err)
		w.mu.Unlock()
	}
}

func (w *warnLog) has(sub string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.warns {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitConsumed waits until the event consumer has handled n events.
func waitConsumed(t *testing.T, app *App, n int64) {
	t.Helper()
	waitUntil(t, "the event consumer", func() bool { return app.streams.consumed.Load() >= n })
}

// systemTexts returns the system messages of one provider request that
// contain sub.
func systemTexts(req agent.ProviderRequest, sub string) []string {
	var out []string
	for _, m := range req.Messages {
		if m.Role == agent.RoleSystem && strings.Contains(m.Text, sub) {
			out = append(out, m.Text)
		}
	}
	return out
}

// Arguments reach the server, two subscriptions to one event name with
// different arguments both deliver, and one the server refuses is a
// startup warning (#310).
func TestEventSubscriptionArguments(t *testing.T) {
	r := startRoomServer(t, false)
	cfg := testConfig(r.ts.URL)
	hint := &agent.ContextHint{Retention: "session", Template: "room {{room}} is at {{v}}"}
	cfg.Servers[0].Events = []EventConfig{
		{Name: "room.event", Arguments: map[string]any{"room": "a"}, Hint: hint},
		{Name: "room.event", Arguments: map[string]any{"room": "b"}, Hint: hint},
		{Name: "room.event"},
	}
	warns := &warnLog{}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	app, err := NewApp(cfg, &syncWriter{}, strings.NewReader(""), WithProvider(stub), WithObserver(warns))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	waitUntil(t, "two subscriptions", func() bool { return len(app.listSubscriptions()) == 2 })
	waitUntil(t, "the refusal warning", func() bool { return warns.has("events/stream room.event on test") })

	for _, d := range []roomData{{"a", 1}, {"b", 1}, {"a", 2}, {"c", 9}} {
		if err := r.yield(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	waitConsumed(t, app, 3)
	if err := app.RunTurn(context.Background(), "where are the rooms?"); err != nil {
		t.Fatal(err)
	}
	got := systemTexts(stub.Requests()[0], "room ")
	if len(got) != 2 || !strings.Contains(strings.Join(got, "|"), "room a is at 2") || !strings.Contains(strings.Join(got, "|"), "room b is at 1") {
		t.Fatalf("want rooms a=2 and b=1 injected, got %v", got)
	}
}

func roomAttachConfig(url string) *Config {
	cfg := testConfig(url)
	cfg.Servers[0].OnConnect = []ToolCallConfig{{Name: "attach"}}
	cfg.Servers[0].Events = []EventConfig{{Name: "room.event", Arguments: map[string]any{"room": "a"}}}
	return cfg
}

// OnConnect runs before the subscription opens, and again before the host
// reopens a stream that dropped (#312).
func TestOnConnectRunsBeforeSubscriptionAndAfterADrop(t *testing.T) {
	old := reopenBackoff
	reopenBackoff.min, reopenBackoff.max = 20*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { reopenBackoff = old })

	r := startRoomServer(t, true)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	app, err := NewApp(roomAttachConfig(r.ts.URL), &syncWriter{}, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	waitUntil(t, "attach then subscribe", func() bool { return len(r.entries()) == 2 })
	if got := r.entries(); got[0] != "attach" || got[1] != "subscribe a" {
		t.Fatalf("server saw %v, want [attach, subscribe a]", got)
	}

	r.mu.Lock()
	r.attached = false
	r.mu.Unlock()
	r.ts.CloseClientConnections()

	waitUntil(t, "a second attach then subscribe", func() bool { return len(r.entries()) >= 4 })
	if got := r.entries(); got[2] != "attach" || got[3] != "subscribe a" {
		t.Fatalf("server saw %v, want attach before the reopened subscribe", got)
	}
	if err := r.yield(context.Background(), roomData{"a", 5}); err != nil {
		t.Fatal(err)
	}
	waitConsumed(t, app, 1)
}

// A failed OnConnect call leaves the server's streams closed and warns
// (#312).
func TestOnConnectFailureKeepsStreamsClosed(t *testing.T) {
	r := startRoomServer(t, true)
	r.failNext = true
	warns := &warnLog{}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	app, err := NewApp(roomAttachConfig(r.ts.URL), &syncWriter{}, strings.NewReader(""), WithProvider(stub), WithObserver(warns))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	waitUntil(t, "the onConnect warning", func() bool { return warns.has("onConnect on test: attach") })
	time.Sleep(100 * time.Millisecond)
	if subs := app.listSubscriptions(); len(subs) != 0 {
		t.Fatalf("no stream may open after a failed onConnect, got %d", len(subs))
	}
	if got := r.entries(); len(got) != 0 {
		t.Fatalf("server saw %v, want nothing", got)
	}
}

func deliverHinted(t *testing.T, app *App, name string, hint map[string]any, payload string) {
	t.Helper()
	err := app.Deliver(context.Background(), agent.IncomingEvent{
		Server: "local",
		Name:   name,
		Data:   core.NewRawJSON(json.RawMessage(payload)),
		Meta:   map[string]any{agent.MetaKeyContextHint: hint},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A delivered event reaches injection with the hint on its own _meta (#313).
// Injection writes into history, so each request carries every earlier
// injection. Turn retention adds the note once and never again. Session
// retention adds the latest view on every turn until a newer one
// supersedes it.
func TestDeliverInjectsWithItsHint(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "1"}, agent.StubTurn{Text: "2"}, agent.StubTurn{Text: "3"})
	app, err := NewApp(testConfig(ts.URL), &syncWriter{}, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	view := map[string]any{"retention": "session", "template": "viewing {{page}}"}
	deliverHinted(t, app, "page.note", map[string]any{"template": "note: {{text}}"}, `{"text":"hello"}`)
	deliverHinted(t, app, "page.view", view, `{"page":"home"}`)
	waitConsumed(t, app, 2)
	wantViews := [][]string{
		{"viewing home"},
		{"viewing home", "viewing home"},
		{"viewing home", "viewing home", "viewing cart"},
	}
	for i, want := range wantViews {
		if i == 2 {
			deliverHinted(t, app, "page.view", view, `{"page":"cart"}`)
			waitConsumed(t, app, 3)
		}
		if err := app.RunTurn(context.Background(), "next"); err != nil {
			t.Fatal(err)
		}
		req := stub.Requests()[i]
		if n := len(systemTexts(req, "note: hello")); n != 1 {
			t.Fatalf("turn %d: note appears %d times, want 1", i, n)
		}
		if got := systemTexts(req, "viewing "); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("turn %d: views %v, want %v", i, got, want)
		}
	}
}

// overlapProvider counts model calls that run at the same time.
type overlapProvider struct {
	agent.Provider
	inflight, max atomic.Int32
}

func (p *overlapProvider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	n := p.inflight.Add(1)
	defer p.inflight.Add(-1)
	for {
		m := p.max.Load()
		if n <= m || p.max.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	return p.Provider.Stream(ctx, req)
}

// A trigger fires from a delivered event, and delivered and streamed events
// share one consumer, so their proactive turns never overlap (#313).
func TestDeliverFiresTriggersWithoutOverlap(t *testing.T) {
	ts, yield := startEventsServer(t)
	cfg := testConfig(ts.URL)
	cfg.Servers[0].Events = []EventConfig{{Name: "cart.changed"}}
	cfg.Triggers = []TriggerConfig{
		{Event: "page.click", Instructions: "The user clicked.", Label: "click"},
		{Event: "cart.changed", Instructions: "The cart changed.", Label: "cart"},
	}
	turns := make([]agent.StubTurn, 4)
	for i := range turns {
		turns[i] = agent.StubTurn{Text: "noted"}
	}
	prov := &overlapProvider{Provider: agent.NewStubProvider(turns...)}
	out := &syncWriter{}
	app, err := NewApp(cfg, out, strings.NewReader(""), WithProvider(prov))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	waitUntil(t, "the cart subscription", func() bool { return len(app.listSubscriptions()) == 1 })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = app.Deliver(context.Background(), agent.IncomingEvent{Server: "local", Name: "page.click", Data: core.NewRawJSON(json.RawMessage(`{}`))})
	}()
	go func() {
		defer wg.Done()
		_ = yield(context.Background(), cartData{Count: 1})
	}()
	wg.Wait()
	waitFor(t, out, "· trigger: click")
	waitFor(t, out, "· trigger: cart")
	waitConsumed(t, app, 2)
	if m := prov.max.Load(); m != 1 {
		t.Fatalf("proactive turns overlapped: %d model calls at once", m)
	}
}
