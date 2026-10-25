package files

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtensionContributesToolsAndPrompt(t *testing.T) {
	e, err := New(Config{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if e.Name() != "files" {
		t.Errorf("Name() = %q, want files", e.Name())
	}

	src, err := e.Tools()
	if err != nil {
		t.Fatal(err)
	}
	defs, err := src.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Named rather than counted. A count says a tool went missing without
	// saying which, and it fails on the day one is added, which is how it
	// stops being read.
	got := map[string]bool{}
	for _, d := range defs {
		got[d.Name] = true
	}
	for _, name := range []string{"read_file", "edit_file", "write_file", "delete_file", "list_files", "search_files"} {
		if !got[name] {
			t.Errorf("tool %q is not declared", name)
		}
	}

	sections := e.PromptSections()
	if len(sections) != 1 {
		t.Fatalf("got %d prompt sections, want 1", len(sections))
	}
	if sections[0].Section(context.Background()) == "" {
		t.Error("the prompt section is empty")
	}
}

// TestPromptStatesTheRulesTheToolsEnforce guards against the two drifting
// apart. A refusal the prompt never warned about reaches the model as a
// surprise it can only learn by failing.
func TestPromptStatesTheRulesTheToolsEnforce(t *testing.T) {
	e, err := New(Config{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := e.PromptSections()[0].Section(context.Background())
	for _, rule := range []string{"expect_hash", "exactly once", "together or none"} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("prompt does not mention %q, which the tools enforce:\n%s", rule, prompt)
		}
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New should refuse an empty Root")
	}
	if _, err := New(Config{Roots: []string{"/no/such/directory/anywhere"}}); err == nil {
		t.Fatal("New should refuse a Root that does not exist")
	}
}

func TestWritePathsResolvesAgainstTheRootNotTheCwd(t *testing.T) {
	root := t.TempDir()
	t.Chdir(t.TempDir())
	e, err := New(Config{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		want []string
	}{
		{"sub/a.go", []string{filepath.Join(root, "sub", "a.go")}},
		{filepath.Join(root, "b.go"), []string{filepath.Join(root, "b.go")}},
		{"../escape.go", nil},
		{"", nil},
	} {
		got := e.WritePaths(map[string]any{"path": c.path})
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("WritePaths(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
