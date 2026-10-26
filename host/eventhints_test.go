package host

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

// hintMeta is an events/list _meta carrying a context hint with a template.
func hintMeta(template string) map[string]any {
	return map[string]any{agent.MetaKeyContextHint: map[string]any{"template": template}}
}

// startHintedEventsServer serves cart.changed with a server-declared hint, and
// returns the registry so a test can add event types later.
func startHintedEventsServer(t *testing.T, template string) (*httptest.Server, func(context.Context, cartData) error, *events.Registry) {
	t.Helper()
	srv := testutil.NewTestServer()
	src, yield := events.NewYieldingSource[cartData](events.EventDef{
		Name: "cart.changed", Description: "cart contents changed", Meta: hintMeta(template),
	})
	reg := events.Register(events.Config{
		Sources:                  []events.EventSource{src},
		Server:                   srv,
		UnsafeAnonymousPrincipal: "agentchat-test",
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts, yield, reg
}

// injectedText yields until an occurrence reaches the durable injection stage
// and returns the system text it produced, or "" on timeout. An occurrence
// yielded before the stream opens is never delivered, so it keeps yielding.
func injectedText(t *testing.T, app *App, yield func() error, contains string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := yield(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		app.turnMu.Lock()
		app.history = app.context.durable[0].Run(context.Background(), app.history)
		var found string
		for _, m := range app.history {
			if m.Role == agent.RoleSystem && strings.Contains(m.Text, contains) {
				found = m.Text
			}
		}
		app.turnMu.Unlock()
		if found != "" {
			return found
		}
	}
	return ""
}

func hintApp(t *testing.T, url string, evs []EventConfig) *App {
	t.Helper()
	cfg := testConfig(url)
	cfg.Servers[0].Events = evs
	app, err := NewApp(cfg, &syncWriter{}, strings.NewReader(""), WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	return app
}

// #81: a hint the server declares on its event type applies with nothing in
// host config. Before, only config hints were read, and this rendered raw.
func TestServerDeclaredEventHintApplies(t *testing.T) {
	ts, yield, _ := startHintedEventsServer(t, "the cart holds {{count}} items")
	app := hintApp(t, ts.URL, []EventConfig{{Name: "cart.changed"}})
	got := injectedText(t, app, func() error { return yield(context.Background(), cartData{Count: 4}) }, "4")
	if got != "the cart holds 4 items" {
		t.Fatalf("injected %q, want the server's template rendered", got)
	}
}

func TestConfigHintBeatsTheServersHint(t *testing.T) {
	ts, yield, _ := startHintedEventsServer(t, "server says {{count}}")
	app := hintApp(t, ts.URL, []EventConfig{{Name: "cart.changed", Hint: &agent.ContextHint{Template: "config says {{count}}"}}})
	got := injectedText(t, app, func() error { return yield(context.Background(), cartData{Count: 5}) }, "5")
	if got != "config says 5" {
		t.Fatalf("injected %q, want the config template", got)
	}
}

// An event type the server adds after connect takes effect without a restart:
// its hint is read and, since it is configured, it gets subscribed.
func TestEventTypeAddedLaterGetsItsHintAndSubscription(t *testing.T) {
	ts, _, reg := startHintedEventsServer(t, "cart {{count}}")
	app := hintApp(t, ts.URL, []EventConfig{{Name: "cart.changed"}, {Name: "cart.cleared"}})
	waitReady(t, app)

	src, yield := events.NewYieldingSource[cartData](events.EventDef{
		Name: "cart.cleared", Description: "cart emptied", Meta: hintMeta("cleared after {{count}} items"),
	})
	if err := reg.AddSource(src); err != nil {
		t.Fatal(err)
	}
	got := injectedText(t, app, func() error { return yield(context.Background(), cartData{Count: 6}) }, "6")
	if got != "cleared after 6 items" {
		t.Fatalf("injected %q, want the new event type's own template", got)
	}
}

func waitReady(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c := app.clientByID("test"); c != nil {
			if _, ok := c.ServerExtensionCapability(events.ExtensionID); ok {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server never became ready")
}
