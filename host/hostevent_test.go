package host

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
)

// recordObserver captures the HostEvent stream so a test can assert what the
// host emitted, independent of any terminal formatting — the proof that a
// non-terminal surface (web) gets everything it needs.
type recordObserver struct {
	mu  sync.Mutex
	evs []HostEvent
}

func (s *recordObserver) On(ev HostEvent) {
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
}

func (s *recordObserver) kinds() []HostEventKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HostEventKind, len(s.evs))
	for i, e := range s.evs {
		out[i] = e.Kind
	}
	return out
}

func (s *recordObserver) has(k HostEventKind) bool {
	for _, got := range s.kinds() {
		if got == k {
			return true
		}
	}
	return false
}

func TestSurface_TurnEmitsStructuredEvents(t *testing.T) {
	ts := startTestServer(t)
	rec := &recordObserver{}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "hello"})
	app, err := NewApp(testConfig(ts.URL), nil, strings.NewReader(""),
		WithProvider(stub), WithObserver(rec))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	// the turn streamed as runner events and ended with a turn-done
	if !rec.has(HostRunnerEvent) {
		t.Fatalf("no runner events emitted: %v", rec.kinds())
	}
	if !rec.has(HostTurnDone) {
		t.Fatalf("no turn-done emitted: %v", rec.kinds())
	}
	// the final event carries the TurnResult, not a formatted string
	last := rec.evs[len(rec.evs)-1]
	if last.Kind != HostTurnDone || last.Result == nil || last.Result.Text != "hello" {
		t.Fatalf("turn-done payload = %+v", last)
	}
}

func TestSurface_CommandEmitsUICommand(t *testing.T) {
	ts := startTestServer(t)
	rec := &recordObserver{}
	cfg := testConfig(ts.URL)
	cfg.Connections = twoConn()
	app, err := NewApp(cfg, nil, strings.NewReader(""),
		WithProviderBuilder(stubBuilder(t)), WithObserver(rec))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// drive a command through the real REPL path
	if err := app.REPL(context.Background(), strings.NewReader("/provider\n/quit\n"), nil); err != nil {
		t.Fatal(err)
	}
	var providers *HostEvent
	for i := range rec.evs {
		if rec.evs[i].Kind == HostCommandResult && rec.evs[i].Command.Kind == CmdProviders {
			providers = &rec.evs[i]
		}
	}
	if providers == nil {
		t.Fatalf("no HostCommandResult/CmdProviders emitted: %v", rec.kinds())
	}
	if providers.Command.ActiveProvider != "local" || len(providers.Command.Providers) != 2 {
		t.Fatalf("provider command payload = %+v", providers.Command)
	}
}

// TestSurface_DefaultRendererStillFormats pins that the default terminal
// path is unchanged: with no WithObserver, output still lands on the writer.
func TestSurface_DefaultRendererStillFormats(t *testing.T) {
	ts := startTestServer(t)
	var out strings.Builder
	stub := agent.NewStubProvider(agent.StubTurn{Text: "world"})
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "world") || !strings.Contains(out.String(), "step(s)") {
		t.Fatalf("default renderer did not format the turn:\n%s", out.String())
	}
}

// TestObserver_FanOutToMany pins that events reach every registered
// observer — the reason these are HostEvents, not UI events: a renderer
// and a tracer (here two recorders) both see the stream.
func TestObserver_FanOutToMany(t *testing.T) {
	ts := startTestServer(t)
	a, b := &recordObserver{}, &recordObserver{}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "hi"})
	app, err := NewApp(testConfig(ts.URL), nil, strings.NewReader(""),
		WithProvider(stub), WithObserver(a), WithObserver(b))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if !a.has(HostTurnDone) || !b.has(HostTurnDone) {
		t.Fatalf("both observers should see turn-done: a=%v b=%v", a.kinds(), b.kinds())
	}
}

// A turn announces the user's input first, as typed, so a surface that replays
// the log can show what was asked.
func TestSurface_TurnAnnouncesUserInput(t *testing.T) {
	ts := startTestServer(t)
	rec := &recordObserver{}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "hello"})
	app, err := NewApp(testConfig(ts.URL), nil, strings.NewReader(""), WithProvider(stub), WithObserver(rec))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "what is 2+2"); err != nil {
		t.Fatal(err)
	}
	// Server connection events may come first; the input must precede the turn.
	for _, ev := range rec.evs {
		if ev.Kind == HostRunnerEvent {
			t.Fatalf("the turn began before its input was announced; kinds %v", rec.kinds())
		}
		if ev.Kind == HostUserInput {
			if ev.Message != "what is 2+2" {
				t.Fatalf("user-input Message = %q, want the typed text", ev.Message)
			}
			return
		}
	}
	t.Fatalf("no user-input event; kinds %v", rec.kinds())
}
