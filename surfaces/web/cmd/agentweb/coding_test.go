package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codingDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := `{"approval": {"mode": "ask"}, "connections": {"active": "local", "connections": {"local": {"type": "lmstudio", "model": "qwen"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCodingFlagsAreChecked(t *testing.T) {
	dir := codingDir(t)
	for _, c := range []struct {
		o    codingOptions
		want string
	}{
		{codingOptions{dir: dir}, "needs --coding-workspace"},
		{codingOptions{dir: dir, workspace: "a", fixture: "b"}, "can't both be set"},
		{codingOptions{dir: t.TempDir(), workspace: "a"}, "config.json"},
		{codingOptions{dir: dir, workspace: "a", instructions: "no-such-agent"}, "no preset"},
	} {
		if _, err := codingProfile(c.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err = %v, want %q", c.o, err, c.want)
		}
	}
}

// Each coding session on a fixture gets its own fresh copy, so two browser
// tabs don't edit the same files.
func TestEachCodingSessionGetsItsOwnFixtureCopy(t *testing.T) {
	fixture := t.TempDir()
	if err := os.WriteFile(filepath.Join(fixture, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prof, err := codingProfile(codingOptions{dir: codingDir(t), fixture: fixture})
	if err != nil {
		t.Fatal(err)
	}
	if prof.Name != "coding" || prof.Path != "/code/" {
		t.Errorf("profile = %q at %q", prof.Name, prof.Path)
	}
	var roots []string
	for range 2 {
		app, err := prof.Build(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(app.Close)
		rs := app.Roots()
		if len(rs) != 1 {
			t.Fatalf("roots = %v", rs)
		}
		root := strings.TrimPrefix(rs[0].URI, "file://")
		if _, err := os.Stat(filepath.Join(root, "main.go")); err != nil {
			t.Errorf("session root %s lacks the fixture: %v", root, err)
		}
		if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
			t.Errorf("session root %s isn't a git repository", root)
		}
		roots = append(roots, root)
	}
	if roots[0] == roots[1] {
		t.Errorf("both sessions work in %s", roots[0])
	}
}
