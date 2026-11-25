package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every agent's ladder climbs on its scripted model with no step failing to
// configure, and its table has a row per step. This is the mission's
// exercise as CI runs it.
func TestEveryLadderClimbs(t *testing.T) {
	for _, a := range All {
		r, err := Climb(context.Background(), a, Model{Provider: a.Scripted(), Name: "scripted"}, nil)
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
	r, err := Climb(context.Background(), a, Model{Provider: a.Scripted(), Name: "scripted"}, nil)
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

func writeProfile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentchat.json")
	os.WriteFile(path, []byte(`{"connections": {"active": "mini", "connections": {
		"mini": {"type": "openai", "model": "gpt-5.5-mini", "price": {"input": 0.25, "output": 2}},
		"local": {"type": "llamacpp"}
	}}}`), 0o600)
	return path
}

// Connections lists a profile's connections by name, with what each runs.
func TestConnectionsListsTheProfile(t *testing.T) {
	cs, err := Connections(writeProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Name != "local" || cs[1].Name != "mini" || cs[1].Model != "gpt-5.5-mini" || cs[1].Price == nil {
		t.Errorf("connections = %+v, want local then mini, with mini's model and price", cs)
	}
	if _, err := Connections(filepath.Join(t.TempDir(), "none.json")); err == nil || !strings.Contains(err.Error(), "none.json") {
		t.Errorf("a missing profile: %v", err)
	}
}

// A connection the profile lacks is reported with the names it has.
func TestLiveNamesTheConnectionsItHas(t *testing.T) {
	_, err := Live(writeProfile(t), "sonnet5.5")
	if err == nil || !strings.Contains(err.Error(), "local, mini") {
		t.Errorf("err = %v, want it to list local, mini", err)
	}
}

// codeq's chat config keeps the profile's connections, sets the final
// step's instructions, and starts codeserver as an MCP server.
func TestChatConfigForCodeq(t *testing.T) {
	a, err := Find("codeq")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ChatConfig(a, writeProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Connections struct {
			Connections map[string]any `json:"connections"`
		} `json:"connections"`
		Instructions string `json:"instructions"`
		Servers      []struct {
			ID, Command string
			Args        []string
		} `json:"servers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Connections.Connections) != 2 {
		t.Errorf("connections = %v, want the profile's two", cfg.Connections.Connections)
	}
	if !strings.Contains(cfg.Instructions, `"in <module>"`) {
		t.Errorf("instructions = %q, want the scoping rules", cfg.Instructions)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].ID != "codeq" || !slices.Contains(cfg.Servers[0].Args, "-root") {
		t.Errorf("servers = %+v, want codeserver with -root", cfg.Servers)
	}
	if _, err := ChatConfig(All[0], writeProfile(t)); err == nil {
		t.Errorf("calc has no chat setup, and ChatConfig didn't say so")
	}
}

// With a progress writer, Climb prints each step as it starts and each case
// as it finishes, so a live run shows where it is.
func TestClimbPrintsProgress(t *testing.T) {
	a, err := Find("calc")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if _, err := Climb(context.Background(), a, Model{Provider: a.Scripted(), Name: "scripted"}, &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"[1/2 model only]", "[2/2 + calc tool]", "PASS", "What is 2 + 3?", "FAIL", `want substring "391" in "about 400"`} {
		if !strings.Contains(out, want) {
			t.Errorf("progress lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "[1/2") > strings.Index(out, "What is 2 + 3?") {
		t.Errorf("a case printed before its step's header:\n%s", out)
	}
}
