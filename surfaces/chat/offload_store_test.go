package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/surfaces"
	"github.com/panyam/mcpkit/core"
)

// buildWithOffload builds agentchat's App from a config file whose offload
// block is set, plus the given flags.
func buildWithOffload(t *testing.T, flags ...string) *appSetup {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	body := `{"model": {"baseUrl": "http://127.0.0.1:1/v1", "model": "m"}, "offload": {"thresholdBytes": 100}}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	root, v := newRoot()
	if err := root.ParseFlags(append([]string{"--config", cfgPath}, flags...)); err != nil {
		t.Fatal(err)
	}
	setup, err := buildApp(context.Background(), v, nil, nil, &discard{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.close)
	return setup
}

type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }

// #93. Offload set in config.json, with no --offload-threshold, still puts
// blobs in --session-store's backend, so a resumed session's stubs resolve
// after a restart.
func TestConfigOffloadUsesTheSessionStoreForBlobs(t *testing.T) {
	db := filepath.Join(t.TempDir(), "s.db")
	setup := buildWithOffload(t, "--session-store", "sqlite://"+db)
	if setup.toolResults == nil {
		t.Fatal("offload from config.json left blobs in memory")
	}
	ctx := context.Background()
	if _, err := setup.toolResults.PutToolResult(ctx, agent.PutToolResultRequest{
		Ref:    "res:a",
		Result: core.ToolResult{Content: []core.Content{{Type: "text", Text: "payload"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// A later process opens the same file.
	again, err := surfaces.ToolResultStoreFromSpec("sqlite://"+db, "")
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := again.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:a"}); err != nil || !resp.Found {
		t.Fatalf("the blob didn't survive a reopen: %+v, %v", resp, err)
	}
}

func TestConfigOffloadUsesTheOffloadDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blobs")
	if setup := buildWithOffload(t, "--offload-dir", dir); setup.toolResults == nil {
		t.Fatal("--offload-dir with offload from config.json left blobs in memory")
	}
}
