package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// TestSessionStageCarriesInputSentDuringAStep is the Runner side of steering
// (#149): input that arrives while step 1's tool runs is added to the record
// by a session-scope stage before step 2, so step 2's request carries it
// after the tool result and the turn's messages keep it.
func TestSessionStageCarriesInputSentDuringAStep(t *testing.T) {
	var mu sync.Mutex
	var queue []string
	fs := NewFuncSource()
	type in struct{}
	if err := AddFunc(fs, "poke", "", func(context.Context, in) (string, error) {
		mu.Lock()
		queue = append(queue, "also check Mean")
		mu.Unlock()
		return "poked", nil
	}); err != nil {
		t.Fatal(err)
	}
	drain := StepStage{Name: "test.steer", Scope: ScopeSession, Run: func(_ context.Context, _ StepInfo, v StepView) (StepView, error) {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range queue {
			v.Messages = append(v.Messages, Message{Role: RoleUser, Text: s})
		}
		queue = nil
		return v, nil
	}}
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "poke", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		StubTurn{Text: "done"},
	)
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: fs, StepStages: []StepStage{drain}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "fix Sum"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	step2 := stub.Requests()[1].Messages
	if last := step2[len(step2)-1]; last.Role != RoleUser || last.Text != "also check Mean" {
		t.Fatalf("step 2 ended with %+v, want the steer after the tool result", last)
	}
	if prev := step2[len(step2)-2]; prev.Role != RoleTool {
		t.Errorf("the steer came before the tool result: %+v", prev)
	}
	found := false
	for _, m := range res.Messages {
		if m.Role == RoleUser && m.Text == "also check Mean" {
			found = true
		}
	}
	if !found {
		t.Error("the steer isn't in the turn's messages, so history and the session store would lose it")
	}
	if n := len(stub.Requests()[0].Messages); n != 1 {
		t.Errorf("step 1 carried %d messages, want just the task", n)
	}
}
