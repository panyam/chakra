package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

type stepExt struct{ BaseExtension }

func (stepExt) Name() string { return "stepnote" }
func (stepExt) StepStages() []agent.StepStage {
	return []agent.StepStage{{
		Name:  "stepnote.note",
		Scope: agent.ScopeRequest,
		Run: func(_ context.Context, info agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			v.Messages = append(v.Messages, agent.Message{Role: agent.RoleSystem, Text: fmt.Sprintf("note for step %d", info.Step)})
			return v, nil
		},
	}}
}

// TestExtensionStepStagesRunBeforeEveryModelCall checks the wiring: an
// extension's step stage reaches the main Runner and runs before each model
// call. Scope behaviour is tested in the agent package.
func TestExtensionStepStagesRunBeforeEveryModelCall(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"hi"}`))}}},
		agent.StubTurn{Text: "done"},
	)
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub), WithExtension(stepExt{}))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "echo hi"); err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model calls, want 2", len(reqs))
	}
	for i, req := range reqs {
		var notes []string
		for _, m := range req.Messages {
			if strings.HasPrefix(m.Text, "note for step") {
				notes = append(notes, m.Text)
			}
		}
		want := fmt.Sprintf("note for step %d", i+1)
		if len(notes) != 1 || notes[0] != want {
			t.Errorf("call %d carried notes %q, want just %q", i+1, notes, want)
		}
	}
}

type announceExt struct {
	BaseExtension
	announce func(string)
}

func (*announceExt) Name() string                  { return "announcer" }
func (e *announceExt) SetAnnounce(fn func(string)) { e.announce = fn }
func (e *announceExt) TurnStart(context.Context) error {
	e.announce("instruction files loaded")
	return nil
}

type messageLog struct {
	mu   sync.Mutex
	msgs []string
}

func (l *messageLog) On(ev HostEvent) {
	if ev.Kind == HostMessage {
		l.mu.Lock()
		l.msgs = append(l.msgs, ev.Message)
		l.mu.Unlock()
	}
}

// TestAnnouncerLinesReachSurfaces checks an extension implementing Announcer
// is handed a function whose lines arrive as HostMessage events.
func TestAnnouncerLinesReachSurfaces(t *testing.T) {
	ts := startTestServer(t)
	log := &messageLog{}
	app, err := NewApp(testConfig(ts.URL), io.Discard, strings.NewReader(""),
		WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "ok"})), WithExtension(&announceExt{}), WithObserver(log))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if !slices.Contains(log.msgs, "instruction files loaded") {
		t.Errorf("messages = %q, want the extension's line", log.msgs)
	}
}

type askExt struct {
	BaseExtension
	ask func(context.Context, string) (bool, error)
}

func (*askExt) Name() string                                            { return "asker" }
func (e *askExt) SetAsk(fn func(context.Context, string) (bool, error)) { e.ask = fn }

// TestAskerQuestionsGoThroughTheAskQueue checks the wiring: an Asker extension
// gets a function whose question is answered the way approval asks are, here
// on the terminal.
func TestAskerQuestionsGoThroughTheAskQueue(t *testing.T) {
	ts := startTestServer(t)
	ext := &askExt{}
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader("y\n"), WithProvider(agent.NewStubProvider()), WithExtension(ext))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if ext.ask == nil {
		t.Fatal("SetAsk was never called")
	}
	ok, err := ext.ask(context.Background(), "Allow commands to connect to example.test?")
	if err != nil || !ok || !strings.Contains(out.String(), "example.test") {
		t.Errorf("ok %v, err %v, terminal %q", ok, err, out.String())
	}
}
