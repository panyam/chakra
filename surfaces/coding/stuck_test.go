package coding

import (
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Imagine the agent running go test ./... three times before fixing
// anything. Each run fails the same way, but the timings differ, so the
// default key sees three different results. The coding key strips the
// timings, so the three runs count as one repeat.
func TestStuckKeyIgnoresTimings(t *testing.T) {
	rec := func(out string) agent.StepRecord {
		return agent.StepRecord{
			Assistant: agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "c1", Name: "shell", Args: core.NewRawJSON([]byte(`{"command":["go","test","./..."]}`))}}},
			Results:   []agent.Message{{Role: agent.RoleTool, ToolCallID: "c1", Text: out}},
		}
	}
	a := rec("--- FAIL: TestSum (0.00s)\n    stats_test.go:9: got 5, want 6\nFAIL\nFAIL\tfixture/stats\t0.214s\nok  \tfixture/fmtx\t(cached)\nexit status 1 after 1.32s")
	b := rec("--- FAIL: TestSum (0.01s)\n    stats_test.go:9: got 5, want 6\nFAIL\nFAIL\tfixture/stats\t0.187s\nok  \tfixture/fmtx\t(cached)\nexit status 1 after 950ms")
	c := rec("--- FAIL: TestSum (0.00s)\n    stats_test.go:9: got 4, want 6\nFAIL\nFAIL\tfixture/stats\t0.214s\nok  \tfixture/fmtx\t(cached)\nexit status 1 after 1.32s")

	da, _ := agent.DefaultStuckKey(a)
	db, _ := agent.DefaultStuckKey(b)
	if da.Result == db.Result {
		t.Fatalf("control: the default key already ignores timings, so this test proves nothing")
	}
	ka, ok := stuckKey(a)
	kb, _ := stuckKey(b)
	kc, _ := stuckKey(c)
	if !ok || ka.Call != kb.Call || ka.Result != kb.Result {
		t.Errorf("runs that differ only in timings keyed differently:\n%+v\n%+v", ka, kb)
	}
	if ka.Result == kc.Result {
		t.Errorf("a different failure keyed the same")
	}
	if _, ok := stuckKey(agent.StepRecord{Assistant: agent.Message{Role: agent.RoleAssistant, Text: "done"}}); ok {
		t.Errorf("a step with no tool call was keyed")
	}
}
