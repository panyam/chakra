package coding

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

func TestBuildNeedsDirAndRoot(t *testing.T) {
	if _, err := (Profile{Root: t.TempDir()}).Build(context.Background()); err == nil || !strings.Contains(err.Error(), "Dir and Root") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildConnectionNamesAMissingKey(t *testing.T) {
	t.Setenv("CODING_TEST_KEY", "")
	cfg := &host.Config{Connections: &host.ConnectionsConfig{Connections: map[string]host.ConnectionConfig{
		"cloud": {Type: "openai", Model: "m", APIKeyEnv: "CODING_TEST_KEY"},
	}}}
	if _, _, err := BuildConnection(cfg, "cloud", ""); err == nil || !strings.Contains(err.Error(), "CODING_TEST_KEY") {
		t.Errorf("err = %v, want the variable named", err)
	}
	if _, _, err := BuildConnection(cfg, "nope", ""); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("err = %v, want the unknown connection named", err)
	}
}

func TestCopyFixtureMakesACommittedRepository(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "pkg", "a.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "repo")
	if err := CopyFixture(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "pkg", "a.go")); err != nil || string(b) != "package pkg\n" {
		t.Errorf("copied file = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git", "HEAD")); err != nil {
		t.Errorf("no git repository: %v", err)
	}
}

func codingDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := `{"connections": {"active": "local", "connections": {"local": {"type": "lmstudio", "model": "qwen"}, "other": {"type": "lmstudio", "model": "llama"}}}}`
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, WorkspaceFile), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// agentweb's model picker lists the App's connections, so a session built
// from the config keeps them (#244). A wrapped or given provider can't be
// switched without bypassing the wrapper, so those sessions have none.
func TestBuildKeepsConnectionsUnlessTheProviderIsWrapped(t *testing.T) {
	dir := codingDir(t)
	app, err := Profile{Dir: dir, Root: t.TempDir(), Active: "other", Model: "llama-big"}.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	names, active := app.Providers()
	if !slices.Equal(names, []string{"local", "other"}) || active != "other" {
		t.Errorf("providers = %v, active %q; want both, with other active", names, active)
	}
	if got := app.ModelLabel(); got != "other (llama-big)" {
		t.Errorf("model label = %q, want the --coding-model override", got)
	}
	if err := app.SwitchProvider("local"); err != nil {
		t.Errorf("switch: %v", err)
	}

	wrapped, err := Profile{Dir: dir, Root: t.TempDir(), WrapProvider: func(p agent.Provider) agent.Provider { return p }}.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer wrapped.Close()
	if names, _ := wrapped.Providers(); len(names) != 0 {
		t.Errorf("a wrapped provider's session offers switches %v that bypass the wrapper", names)
	}
}

func TestBuildAttachesTheRunStore(t *testing.T) {
	store := agent.NewInMemoryRunStore()
	app, err := Profile{Dir: codingDir(t), Root: t.TempDir(), RunStore: store}.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.AttachRun(context.Background(), "coding-1"); err != nil {
		t.Fatalf("AttachRun: %v", err)
	}
}
