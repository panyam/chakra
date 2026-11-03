package coding

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
