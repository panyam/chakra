package codeq

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

func fixture(t *testing.T) *Index {
	t.Helper()
	ix, err := Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestIndexReadsTheFixture(t *testing.T) {
	ix := fixture(t)
	if got, want := ix.PackagesIn("example.com/ledger"), []string{"example.com/ledger/audit", "example.com/ledger/journal", "example.com/ledger/money"}; !slices.Equal(got, want) {
		t.Errorf("ledger packages = %v, want %v", got, want)
	}
	importers := ix.ImportersOf("example.com/ledger/money")
	if !slices.Contains(importers, Edge{Path: "example.com/shop/payments", TestOnly: true}) ||
		!slices.Contains(importers, Edge{Path: "example.com/shop/orders"}) {
		t.Errorf("money's importers = %v, want payments marked test-only and orders not", importers)
	}
	if _, err := ix.CallersOf("Save"); err == nil || !strings.Contains(err.Error(), "qualified") {
		t.Errorf("callers of a bare name: %v, want a request for a qualified name", err)
	}
	if got, _ := ix.CallersOf("example.com/ledger/journal.Post"); !slices.Equal(got, []string{"example.com/ledger/audit.Replay", "example.com/shop/orders.Place", "example.com/shop/payments.Charge"}) {
		t.Errorf("journal.Post's callers = %v", got)
	}
	if got := ix.Find("Save"); len(got) != 1 || got[0][0] != "example.com/shop/internal/db.Save" || !strings.HasPrefix(got[0][1], "example.com/shop/internal/db/db.go:") {
		t.Errorf("find Save = %v", got)
	}
}

// The import questions' expected rows agree with go list, which reads the
// fixture the way the go command builds it, so they can't drift from the
// code. The callers and definition questions are checked by hand.
func TestImportAnswersAgreeWithGoList(t *testing.T) {
	type pkg struct {
		path    string
		imports []string // non-test imports only
	}
	var pkgs []pkg
	for _, mod := range []string{"ledger", "shop"} {
		cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./...")
		cmd.Dir = filepath.Join("testdata", mod)
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list in %s: %v", mod, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := strings.Fields(line)
			pkgs = append(pkgs, pkg{f[0], f[1:]})
		}
	}
	importers := func(module, target string) []string {
		var out []string
		for _, p := range pkgs {
			if strings.HasPrefix(p.path, module) && slices.Contains(p.imports, target) {
				out = append(out, p.path)
			}
		}
		return out
	}
	local := func(paths []string) []string {
		return slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return !strings.HasPrefix(p, "example.com/") })
	}
	byText := map[string][]string{}
	for _, q := range questions {
		byText[q.text] = q.want
	}
	for text, got := range map[string][]string{
		"Which packages does example.com/shop/orders import?":                                               local(pkgs[slices.IndexFunc(pkgs, func(p pkg) bool { return p.path == "example.com/shop/orders" })].imports),
		"Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?": importers("example.com/shop/", "example.com/ledger/money"),
		"Which packages in example.com/shop import example.com/ledger/journal?":                             importers("example.com/shop/", "example.com/ledger/journal"),
		"Which packages import example.com/shop/catalog?":                                                   importers("", "example.com/shop/catalog"),
		"Which packages in example.com/shop import example.com/shop/orders?":                                importers("example.com/shop/", "example.com/shop/orders"),
	} {
		want, ok := byText[text]
		if !ok {
			t.Errorf("no question %q", text)
			continue
		}
		if slices.Sort(got); !slices.Equal(got, want) {
			t.Errorf("%s\n  go list: %v\n  want:    %v", text, got, want)
		}
	}
}

// Each step passes more on the scripted model, through the real codeserver
// process. The multi-hop questions fail on every step until the routing
// step, whose note tells the model to walk, and pass on that one.
func TestEachStepPassesMore(t *testing.T) {
	l, stop, err := Ladder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	r := l.Run(context.Background(), eval.Suite{Config: agentConfig()})
	var got []int
	for _, s := range r.Steps {
		if s.Err != "" {
			t.Fatalf("%s: %s", s.Step, s.Err)
		}
		got = append(got, s.Report.Passed)
		for _, c := range s.Report.Cases {
			i := slices.IndexFunc(questions, func(q question) bool { return q.text == c.Case })
			if routed := s.Step == "+ routing"; questions[i].tag == multiHop && c.Pass != routed {
				t.Errorf("%s: multi-hop question %q passed = %v, want %v", s.Step, c.Case, c.Pass, routed)
			}
		}
	}
	if want := []int{4, 9, 10, 12, 20}; !slices.Equal(got, want) {
		t.Errorf("passed per step = %v, want %v\n%s", got, want, r.Table())
	}
}

func agentConfig() agent.RunnerConfig {
	return agent.RunnerConfig{Provider: Scripted(), Instructions: Instructions, MaxSteps: 8}
}

// Every codeserver tool says it's read-only, so agentchat's approval gate
// runs it under read-only-auto.
func TestServerToolsAreReadOnly(t *testing.T) {
	c, _, stop, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	tools, err := agent.NewClientSource(c).Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 5 {
		t.Fatalf("%d tools, want 5", len(tools))
	}
	for _, tool := range tools {
		if tool.Annotations["readOnlyHint"] != true {
			t.Errorf("%s: annotations %v, want readOnlyHint", tool.Name, tool.Annotations)
		}
	}
}
