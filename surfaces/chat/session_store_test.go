package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// The RunStore-spec mapping moved to surfaces.RunStoreFromSpec, and its table
// and reopen tests are in surfaces/runstore_test.go. Only the tool-result store
// mapping is still here, because it is local to agentchat.

func TestBuildToolResultStoreSpecs(t *testing.T) {
	// memory / empty -> nil (host uses its in-memory default)
	for _, spec := range []string{"", "memory"} {
		if s, err := buildToolResultStore(spec, ""); err != nil || s != nil {
			t.Fatalf("buildToolResultStore(%q) = (%v, %v), want (nil, nil)", spec, s, err)
		}
	}
	for _, bad := range []string{"bogus", "sqlite://", "redis://"} {
		if _, err := buildToolResultStore(bad, ""); err == nil {
			t.Fatalf("buildToolResultStore(%q) succeeded, want error", bad)
		}
	}
}

// TestBuildToolResultStoreSQLite pins the no-server offload path. A sqlite spec
// yields a usable durable blob store on a local file.
func TestBuildToolResultStoreSQLite(t *testing.T) {
	spec := "sqlite://" + filepath.Join(t.TempDir(), "blobs.db")
	s, err := buildToolResultStore(spec, "")
	if err != nil || s == nil {
		t.Fatalf("buildToolResultStore(sqlite) = (%v, %v)", s, err)
	}
	ctx := context.Background()
	if _, err := s.PutToolResult(ctx, agent.PutToolResultRequest{
		Ref:    "res:a",
		Result: core.ToolResult{Content: []core.Content{{Type: "text", Text: "payload"}}},
	}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	resp, err := s.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:a"})
	if err != nil || !resp.Found {
		t.Fatalf("GetToolResult = (%+v, %v)", resp, err)
	}
}

// TestBuildToolResultStoreOffloadDir pins the no-server local path: a dir
// yields a filesystem store regardless of --session-store.
func TestBuildToolResultStoreOffloadDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blobs")
	s, err := buildToolResultStore("", dir) // no session store, dir wins
	if err != nil || s == nil {
		t.Fatalf("buildToolResultStore(dir) = (%v, %v)", s, err)
	}
	ctx := context.Background()
	if _, err := s.PutToolResult(ctx, agent.PutToolResultRequest{
		Ref:    "res:f",
		Result: core.ToolResult{Content: []core.Content{{Type: "text", Text: "on disk"}}},
	}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	if resp, _ := s.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:f"}); !resp.Found {
		t.Fatal("file store did not retain the blob")
	}
}
