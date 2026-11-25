package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra/examples/agents/common"
	"github.com/panyam/demokit"
)

// walk runs a's walk on its scripted model with no prompts, replaying
// inputs when given, and returns each step's recorded output by step ID.
func walk(t *testing.T, a common.Agent, replay []demokit.TraceEntry) map[string]string {
	t.Helper()
	d, stop, err := common.Walk(context.Background(), a, common.Model{Provider: a.Scripted(), Name: "scripted"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	trace := filepath.Join(t.TempDir(), "walk.json")
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"walk", "--non-interactive", "--record", trace}
	if replay != nil {
		d.WithReplay(replay)
	}
	d.Execute()
	entries, err := demokit.LoadTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.Kind == demokit.KindStep {
			out[e.StepID] += e.Output
		}
	}
	return out
}

// Every agent walks every step, and each step passes what the exercise
// passes, so the walk and make exercise-agents agree.
func TestEveryAgentWalks(t *testing.T) {
	for _, a := range All {
		r, err := common.Climb(context.Background(), a, common.Model{Provider: a.Scripted(), Name: "scripted"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := walk(t, a, nil)
		for i, s := range r.Steps {
			id := fmt.Sprintf("step-%d", i+1)
			want := fmt.Sprintf("%s passed %d of %d", s.Step, s.Report.Passed, s.Report.Total)
			if !strings.Contains(out[id], want) {
				t.Errorf("%s %s: output lacks %q:\n%s", a.Name, id, want, out[id])
			}
		}
		if !strings.Contains(out["table"], "step") || !strings.Contains(out["table"], r.Steps[len(r.Steps)-1].Step) {
			t.Errorf("%s: no table at the end:\n%s", a.Name, out["table"])
		}
	}
}

// Each step says what it changed: codeq's second step adds instructions,
// its third a step stage, and its fourth a turn-end hook.
func TestWalkSaysWhatEachStepAdds(t *testing.T) {
	a, err := Find("codeq")
	if err != nil {
		t.Fatal(err)
	}
	out := walk(t, a, nil)
	for id, want := range map[string]string{
		"step-1": "+ tools: packages, imports, importers, find, callers",
		"step-2": `When a question says "in <module>"`,
		"step-3": "+ 1 step stage(s)",
		"step-4": "+ 1 turn-end hook(s)",
	} {
		if !strings.Contains(out[id], want) {
			t.Errorf("%s lacks %q:\n%s", id, want, out[id])
		}
	}
}

// Watching a case prints its transcript, tool calls included, and its
// scores, and leaves the step out of the table.
func TestWalkWatchesOneCase(t *testing.T) {
	a, err := Find("codeq")
	if err != nil {
		t.Fatal(err)
	}
	replay := []demokit.TraceEntry{
		{Kind: demokit.KindStep, StepID: "step-1", Inputs: map[string]any{"action": "run", "case": 1}},
		{Kind: demokit.KindStep, StepID: "step-2", Inputs: map[string]any{"action": "watch", "case": 5}},
	}
	out := walk(t, a, replay)
	for _, want := range []string{"Watching case 5: Which packages in example.com/shop import example.com/ledger/journal?", `tool call: importers({"package":"example.com/ledger/journal"})`, "answer: example.com/shop/orders", "PASS Rows"} {
		if !strings.Contains(out["step-2"], want) {
			t.Errorf("step-2 lacks %q:\n%s", want, out["step-2"])
		}
	}
	for _, line := range strings.Split(out["table"], "\n") {
		if strings.HasPrefix(line, "+ instructions") {
			t.Errorf("a watched step made the table:\n%s", out["table"])
		}
	}
	if !strings.Contains(out["table"], "tools only") {
		t.Errorf("the step that ran every case is missing from the table:\n%s", out["table"])
	}
}

// A recorded trace stores the case to watch as a JSON number, which decodes
// as a float64, and replaying it still watches that case.
func TestWalkWatchesTheCaseFromARecording(t *testing.T) {
	a, err := Find("calc")
	if err != nil {
		t.Fatal(err)
	}
	out := walk(t, a, []demokit.TraceEntry{{Kind: demokit.KindStep, StepID: "step-1", Inputs: map[string]any{"action": "watch", "case": float64(3)}}})
	if !strings.Contains(out["step-1"], "Watching case 3: What is 17 * 23?") {
		t.Errorf("step-1 didn't watch case 3:\n%s", out["step-1"])
	}
}
