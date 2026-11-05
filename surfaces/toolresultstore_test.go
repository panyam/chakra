package surfaces

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

func TestToolResultStoreFromSpec(t *testing.T) {
	if s, err := ToolResultStoreFromSpec("", ""); err != nil || s != nil {
		t.Fatalf(`ToolResultStoreFromSpec("") = (%v, %v), want (nil, nil)`, s, err)
	}
	// One store per call, so a surface that builds it once shares it.
	if s, err := ToolResultStoreFromSpec("memory", ""); err != nil || s == nil {
		t.Fatalf("ToolResultStoreFromSpec(memory) = (%v, %v), want a store", s, err)
	}
	for _, bad := range []string{"bogus", "sqlite://", "redis://"} {
		if _, err := ToolResultStoreFromSpec(bad, ""); err == nil {
			t.Fatalf("ToolResultStoreFromSpec(%q) succeeded, want error", bad)
		}
	}
}

func putAndGet(t *testing.T, put, get agent.ToolResultStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := put.PutToolResult(ctx, agent.PutToolResultRequest{
		Ref:    "res:a",
		Result: core.ToolResult{Content: []core.Content{{Type: "text", Text: "payload"}}},
	}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	resp, err := get.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:a"})
	if err != nil || !resp.Found {
		t.Fatalf("GetToolResult = (%+v, %v)", resp, err)
	}
}

// A sqlite spec gives blobs on a local file, and a second store over the same
// file, as after a restart, finds them.
func TestToolResultStoreFromSpecSQLiteSurvivesReopen(t *testing.T) {
	spec := "sqlite://" + filepath.Join(t.TempDir(), "blobs.db")
	a, err := ToolResultStoreFromSpec(spec, "")
	if err != nil || a == nil {
		t.Fatalf("ToolResultStoreFromSpec(sqlite) = (%v, %v)", a, err)
	}
	b, err := ToolResultStoreFromSpec(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	putAndGet(t, a, b)
}

// A dir gives a filesystem store whatever the session-store spec says.
func TestToolResultStoreFromSpecDirWins(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blobs")
	a, err := ToolResultStoreFromSpec("memory", dir)
	if err != nil || a == nil {
		t.Fatalf("ToolResultStoreFromSpec(dir) = (%v, %v)", a, err)
	}
	if _, ok := a.(*agent.FileToolResultStore); !ok {
		t.Fatalf("dir gave %T, want a file store", a)
	}
	b, err := ToolResultStoreFromSpec("", dir)
	if err != nil {
		t.Fatal(err)
	}
	putAndGet(t, a, b)
}
