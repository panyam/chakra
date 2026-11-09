package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

func slotSession(t *testing.T, store agent.RunStore, stub *agent.StubProvider) (*App, *SlotsExtension) {
	t.Helper()
	slots, err := agent.NewSlots(agent.SlotSpec{Name: "plan"}, agent.SlotSpec{Name: "facts", Mode: agent.SlotAppend})
	if err != nil {
		t.Fatal(err)
	}
	ext := NewSlotsExtension(slots, "facts")
	ts := startTestServer(t)
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""),
		WithProvider(stub), WithRunStore(store), WithExtension(ext, NewPlanSlotExtension(slots, "plan")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app, ext
}

func slotCall(id, slot, text string) agent.StubTurn {
	raw, _ := json.Marshal(map[string]string{"slot": slot, "text": text})
	return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: id, Name: agent.SlotToolName, Args: core.NewRawJSON(raw)}}}
}

// Imagine a session that wrote a fact and a plan yesterday, and an agentweb
// restart that resumes it today. The messages come back from the RunStore,
// and the slots come back from its event log, so the first call of the new
// session shows both.
func TestSlotsComeBackWhenASessionResumes(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	plan := agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "p1", Name: agent.PlanToolName,
		Args: core.NewRawJSON(json.RawMessage(`{"plan":[{"step":"fix Sum","status":"in_progress"}]}`))}}}
	first, _ := slotSession(t, store, agent.NewStubProvider(slotCall("c1", "facts", "go test needs -race"), plan, agent.StubTurn{Text: "noted"}))
	if err := first.RunTurn(ctx, "start"); err != nil {
		t.Fatal(err)
	}

	stub := agent.NewStubProvider(agent.StubTurn{Text: "carrying on"})
	second, ext := slotSession(t, store, stub)
	if got := ext.Slots().Get("facts"); got != "" {
		t.Fatalf("a new session starts with facts %q", got)
	}
	if err := second.Resume(ctx, first.RunID()); err != nil {
		t.Fatal(err)
	}
	if err := second.RunTurn(ctx, "go on"); err != nil {
		t.Fatal(err)
	}
	req := stub.Requests()[0]
	last := req.Messages[len(req.Messages)-1]
	if last.Role != agent.RoleSystem || !strings.Contains(last.Text, "go test needs -race") || !strings.Contains(last.Text, "fix Sum") {
		t.Errorf("the resumed session's first call ends with %q, want the restored facts and plan", last.Text)
	}
}

func TestAttachRunStartsANewRunWithEmptySlots(t *testing.T) {
	ctx := context.Background()
	app, ext := slotSession(t, agent.NewInMemoryRunStore(), agent.NewStubProvider())
	if _, err := ext.Slots().Write(ctx, "facts", "from the last run"); err != nil {
		t.Fatal(err)
	}
	if err := app.AttachRun(ctx, "fresh"); err != nil {
		t.Fatal(err)
	}
	if got := ext.Slots().Get("facts"); got != "" {
		t.Errorf("a newly created run kept facts %q", got)
	}
}
