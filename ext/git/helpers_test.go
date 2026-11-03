package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// repo makes a git repository in a temp dir with one commit holding a.txt
// and lib/b.go, on a branch called main.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sh(t, dir, "init", "-q", "-b", "main")
	sh(t, dir, "config", "user.email", "t@example.com")
	sh(t, dir, "config", "user.name", "T")
	sh(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "a.txt", "one\ntwo\n")
	write(t, dir, "lib/b.go", "package lib\n")
	sh(t, dir, "add", "-A")
	sh(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func source(t *testing.T, cfg Config) *Source {
	t.Helper()
	s, err := NewSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// call runs a tool and returns its text and whether it was an error result.
func call(t *testing.T, s *Source, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.Call(context.Background(), name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].Text, res.IsError
}

func names(defs []core.ToolDef) string {
	var n []string
	for _, d := range defs {
		n = append(n, d.Name)
	}
	return strings.Join(n, " ")
}
