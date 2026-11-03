package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
