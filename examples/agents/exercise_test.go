package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every agent's ladder climbs on its scripted model with no step failing to
// configure, and its table has a row per step. This is the mission's
// exercise as CI runs it.
func TestEveryLadderClimbs(t *testing.T) {
	for _, a := range All {
		r, err := Climb(context.Background(), a, Model{Provider: a.Scripted(), Name: "scripted"})
		if err != nil {
			t.Fatalf("%s: %v", a.Name, err)
		}
		if len(r.Steps) == 0 {
			t.Errorf("%s: no steps ran", a.Name)
		}
		for _, s := range r.Steps {
			if s.Err != "" {
				t.Errorf("%s: %s", a.Name, s.Err)
			}
			if !strings.Contains(r.Table(), s.Step) {
				t.Errorf("%s: table has no row for %q:\n%s", a.Name, s.Step, r.Table())
			}
		}
		if _, err := os.Stat(filepath.Join(a.Name, "README.md")); err != nil {
			t.Errorf("%s: no README.md tutorial: %v", a.Name, err)
		}
	}
}

// calc's calculator step passes the four products and quotients that the
// model alone guesses at.
func TestCalcToolStepPassesMore(t *testing.T) {
	a, err := Find("calc")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Climb(context.Background(), a, Model{Provider: a.Scripted(), Name: "scripted"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Steps[0].Report.Passed; got != 2 {
		t.Errorf("model only passed %d, want 2 (the sum and the difference)", got)
	}
	if got := r.Steps[1].Report.Passed; got != 6 {
		t.Errorf("+ calc tool passed %d, want 6:\n%s", got, r.Steps[1].Report)
	}
	if d := r.Steps[1].Delta(); d != 4 {
		t.Errorf("+ calc tool change = %+d, want +4", d)
	}
}

// Live reads a connection from an agentchat config, and its Price prices
// the run.
func TestLiveReadsTheProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentchat.json")
	os.WriteFile(path, []byte(`{"connections": {"active": "local", "connections": {
		"local": {"type": "llamacpp"},
		"mini": {"type": "openai", "model": "gpt-5.5-mini", "price": {"input": 0.25, "output": 2}}
	}}}`), 0o600)
	m, err := Live(path, "mini")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "mini" || m.Provider == nil || m.Prices["mini"].InputPerM != 0.25 || m.Prices["mini"].OutputPerM != 2 {
		t.Errorf("mini = %+v", m)
	}
	if m, err := Live(path, "local"); err != nil || m.Prices != nil {
		t.Errorf("local = %+v, %v; want a model with no prices", m, err)
	}
	if _, err := Live(path, "nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("a missing connection: %v", err)
	}
}
