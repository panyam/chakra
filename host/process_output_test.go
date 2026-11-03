package host

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	agent "github.com/panyam/chakra"
)

type processExt struct {
	BaseExtension
	report func(ProcessOutput)
}

func (*processExt) Name() string                              { return "procs" }
func (e *processExt) SetProcessOutput(fn func(ProcessOutput)) { e.report = fn }
func (e *processExt) TurnStart(context.Context) error {
	e.report(ProcessOutput{Source: "procs", ID: "p1", Command: "serve", Text: "listening on :8080\n"})
	return nil
}

type processLog struct {
	mu  sync.Mutex
	evs []ProcessOutput
}

func (l *processLog) On(ev HostEvent) {
	if ev.Kind == HostProcessOutput && ev.ProcessOutput != nil {
		l.mu.Lock()
		l.evs = append(l.evs, *ev.ProcessOutput)
		l.mu.Unlock()
	}
}

// TestProcessOutputReachesSurfacesNotTheModel checks the wiring: a
// ProcessReporter's output arrives as HostProcessOutput, during a turn and
// between turns, and never in what the model is sent.
func TestProcessOutputReachesSurfacesNotTheModel(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"}, agent.StubTurn{Text: "ok again"})
	ext := &processExt{}
	log := &processLog{}
	app, err := NewApp(testConfig(ts.URL), io.Discard, strings.NewReader(""), WithProvider(stub), WithExtension(ext), WithObserver(log))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if ext.report == nil {
		t.Fatal("SetProcessOutput was never called")
	}
	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	ext.report(ProcessOutput{Source: "procs", ID: "p1", Text: "GET /\n", Exited: true, ExitCode: 0})
	if err := app.RunTurn(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}

	log.mu.Lock()
	got := append([]ProcessOutput(nil), log.evs...)
	log.mu.Unlock()
	if len(got) != 3 || got[0].ID != "p1" || got[1].Text != "GET /\n" || !got[1].Exited {
		t.Fatalf("process events = %+v", got)
	}
	for i, req := range stub.Requests() {
		for _, m := range req.Messages {
			if strings.Contains(m.Text, "listening on") || strings.Contains(m.Text, "GET /") {
				t.Errorf("request %d carried process output: %q", i, m.Text)
			}
		}
	}
}

func TestRendererProcessOutputJoinsSplitLines(t *testing.T) {
	var out strings.Builder
	r := newRenderer(&out, false)
	r.processOutput(&ProcessOutput{ID: "p1", Text: "build sta"})
	r.processOutput(&ProcessOutput{ID: "p2", Text: "other\n"})
	r.processOutput(&ProcessOutput{ID: "p1", Text: "rted\nhalf"})
	r.processOutput(&ProcessOutput{ID: "p1", Dropped: 40, Paused: true})
	r.processOutput(&ProcessOutput{ID: "p1", Text: " done", Exited: true, ExitCode: 2})
	want := "[p2] other\n" +
		"[p1] build started\n" +
		"[p1] ... 40 bytes dropped ...\n" +
		"[p1] output streaming paused; the model still reads it\n" +
		"[p1] half done\n" +
		"[p1 exited 2]\n"
	if out.String() != want {
		t.Errorf("rendered\n%s\nwant\n%s", out.String(), want)
	}
}
