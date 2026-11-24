package longmemeval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/mcpkit/core"
)

// scriptedMemory records what it ingests, reports a fixed spend per session,
// and answers with one tool that names how many sessions it holds.
type scriptedMemory struct {
	got    []Session
	spend  map[string]agent.Usage
	failOn string
	tools  *agent.FuncSource
}

func newScriptedMemory(t *testing.T, spend map[string]agent.Usage) *scriptedMemory {
	t.Helper()
	m := &scriptedMemory{spend: spend, tools: agent.NewFuncSource()}
	if err := agent.AddFunc(m.tools, "sessions", "counts the sessions in memory", func(ctx context.Context, _ struct{}) (string, error) {
		ids := make([]string, len(m.got))
		for i, s := range m.got {
			ids[i] = s.ID
		}
		return "sessions: " + strings.Join(ids, ","), nil
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *scriptedMemory) Ingest(ctx context.Context, s Session) (map[string]agent.Usage, error) {
	if s.ID == m.failOn {
		return map[string]agent.Usage{"extractor": {InputTokens: 1}}, errors.New("extractor down")
	}
	m.got = append(m.got, s)
	return m.spend, nil
}

func (m *scriptedMemory) Tools() agent.ToolSource { return m.tools }

func threeSessions() Instance {
	return Instance{
		QuestionID:         "q1",
		QuestionType:       "temporal-reasoning",
		Question:           "how many sessions?",
		QuestionDate:       "2023/06/01 (Thu) 09:00",
		HaystackSessionIDs: []string{"s-a", "", "s-c"},
		HaystackDates:      []string{"2023/05/01 (Mon) 10:00", "2023/05/02 (Tue) 11:00"},
		HaystackSessions: [][]Turn{
			{{Role: "user", Content: "first"}},
			{{Role: "user", Content: "second"}, {Role: "assistant", Content: "ok"}},
			{{Role: "user", Content: "third"}},
		},
	}
}

func TestMemoryArmIngestsSessionsInOrderWithDates(t *testing.T) {
	var built []*scriptedMemory
	arm := MemoryArm(func() (SessionMemory, error) {
		m := newScriptedMemory(t, nil)
		built = append(built, m)
		return m, nil
	})

	st, err := arm(context.Background(), threeSessions())
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 {
		t.Fatalf("built %d memories for one instance", len(built))
	}
	got := built[0].got
	want := []Session{
		{ID: "s-a", Date: "2023/05/01 (Mon) 10:00"},
		{ID: "session-1", Date: "2023/05/02 (Tue) 11:00"},
		{ID: "s-c", Date: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("ingested %d sessions, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].ID != w.ID || got[i].Date != w.Date {
			t.Errorf("session %d = %s %q, want %s %q", i, got[i].ID, got[i].Date, w.ID, w.Date)
		}
	}
	if len(got[1].Turns) != 2 || got[1].Turns[1].Content != "ok" {
		t.Errorf("session 1 turns = %+v", got[1].Turns)
	}
	if st.Tools != agent.ToolSource(built[0].tools) {
		t.Errorf("setup tools are not the memory's tools")
	}
	if st.Instructions != "" || st.History != nil {
		t.Errorf("the arm should leave instructions and history to the adapter: %+v", st)
	}

	if _, err := arm(context.Background(), threeSessions()); err != nil {
		t.Fatal(err)
	}
	if len(built) != 2 || len(built[1].got) != 3 {
		t.Errorf("a second instance must get a fresh memory, got %d memories", len(built))
	}
}

func TestMemoryArmReportsIngestSpendAsSetupUsage(t *testing.T) {
	inst := threeSessions()
	spend := map[string]agent.Usage{
		"extractor": {InputTokens: 100, OutputTokens: 10},
		"":          {InputTokens: 5},
	}
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "sessions", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: "three"},
	)
	adapter := DatasetAdapter{Arm: MemoryArm(func() (SessionMemory, error) { return newScriptedMemory(t, spend), nil })}
	sc := adapter.caseFor(inst)
	sc.Scorers = func(agent.Provider) []eval.Scorer { return []eval.Scorer{eval.ToolCalled("sessions")} }

	report := eval.Suite{
		Config:    agent.RunnerConfig{Provider: stub},
		ModelName: "answerer",
		Cases:     []eval.SuiteCase{sc},
	}.Run(context.Background())

	c := report.Cases[0]
	if !c.Pass {
		t.Fatalf("the agent should answer with the memory's tool: %+v", c)
	}
	if got := c.SetupUsage["extractor"]; got.InputTokens != 300 || got.OutputTokens != 30 {
		t.Errorf("extractor setup usage = %+v, want three sessions summed", got)
	}
	if got := c.SetupUsage["answerer"]; got.InputTokens != 15 {
		t.Errorf("unnamed setup usage = %+v, want it under the suite's model name", got)
	}
	if !strings.Contains(stub.Requests()[0].Instructions, inst.QuestionDate) {
		t.Errorf("instructions lost the question date: %q", stub.Requests()[0].Instructions)
	}
}

func TestMemoryArmKeepsTheSpendWhenAnIngestFails(t *testing.T) {
	spend := map[string]agent.Usage{"extractor": {InputTokens: 100}}
	m := newScriptedMemory(t, spend)
	m.failOn = "session-1"
	arm := MemoryArm(func() (SessionMemory, error) { return m, nil })

	st, err := arm(context.Background(), threeSessions())
	if err == nil || !strings.Contains(err.Error(), "session-1") {
		t.Fatalf("err = %v, want the failing session named", err)
	}
	if got := st.Usage["extractor"]; got.InputTokens != 101 {
		t.Errorf("usage = %+v, want the first session and the failed attempt", st.Usage)
	}
	if len(m.got) != 1 {
		t.Errorf("ingested %d sessions, want the arm to stop at the failure", len(m.got))
	}
}
