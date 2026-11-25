package codefix

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// Each step fixes one more task on the scripted model, through the real
// file and exec tools in a copy of the module.
func TestEachStepFixesOneMore(t *testing.T) {
	l, stop, err := Ladder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	r := l.Run(context.Background(), eval.Suite{Config: agent.RunnerConfig{Provider: Scripted(), Instructions: Instructions, MaxSteps: 10}})
	var got []int
	for _, s := range r.Steps {
		if s.Err != "" {
			t.Fatalf("%s: %s", s.Step, s.Err)
		}
		got = append(got, s.Report.Passed)
	}
	if want := []int{1, 2, 3, 4, 5}; !slices.Equal(got, want) {
		t.Errorf("passed per step = %v, want %v\n%s", got, want, r.Table())
	}
}

// Before a case sets up its copy, the tools still list, so a walk can say
// what a step offers, and a call is refused, so nothing edits the fixture.
func TestToolsListButRefuseCallsWithoutAWorkspace(t *testing.T) {
	k := &kit{ws: &workspace{}, tests: true}
	defs, err := k.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	if !slices.Contains(names, "edit_file") || !slices.Contains(names, "run_tests") {
		t.Errorf("tools = %v, want edit_file and run_tests", names)
	}
	if _, err := k.Call(context.Background(), "read_file", map[string]any{"path": "go.mod"}); !errors.Is(err, errNoWorkspace) {
		t.Errorf("a call without a workspace = %v, want errNoWorkspace", err)
	}
}

// The repo map shows that cart's Priciest calls money.Max, which is what
// sends a model from the report package to the bug.
func TestRepoMapLinksPriciestToMax(t *testing.T) {
	dir, err := tasks[3].workspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := repoMap(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m, "cart/cart.go: (*Cart).Total, (*Cart).Priciest; calls money.Sum, money.Max") {
		t.Errorf("repo map:\n%s", m)
	}
}
