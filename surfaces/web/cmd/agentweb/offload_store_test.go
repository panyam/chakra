package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// countingStore counts the blobs a turn offloads into it.
type countingStore struct {
	agent.ToolResultStore
	puts atomic.Int32
}

func (c *countingStore) PutToolResult(ctx context.Context, req agent.PutToolResultRequest) (agent.PutToolResultResponse, error) {
	c.puts.Add(1)
	return c.ToolResultStore.PutToolResult(ctx, req)
}

// longExt offers one tool whose result is over any small offload threshold.
type longExt struct{ host.BaseExtension }

func (longExt) Name() string { return "long" }

func (longExt) Tools() (agent.ToolSource, error) {
	src := agent.NewFuncSource()
	err := agent.AddFunc(src, "long", "returns a long text", func(context.Context, struct{}) (string, error) {
		return strings.Repeat("line of output\n", 100), nil
	})
	return src, err
}

// #93. Every App agentweb builds takes its stores from storeOpts. An
// offloaded result has to land in the store built from --session-store, which
// all sessions share, so a rehydrated or restarted session's stubs resolve.
func TestStoreOptsOffloadIntoTheSharedStore(t *testing.T) {
	store := &countingStore{ToolResultStore: agent.NewInMemoryToolResultStore()}
	o := appOptions{store: agent.NewInMemoryRunStore(), toolResults: store}
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "long", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: "done"},
	)
	cfg := &host.Config{
		Model:   host.ModelConfig{BaseURL: "http://stub", Model: "stub"},
		Offload: &host.OffloadConfig{ThresholdBytes: 300},
	}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""),
		append(o.storeOpts(), host.WithProvider(stub), host.WithExtension(longExt{}))...)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if store.puts.Load() == 0 {
		t.Fatal("the offloaded result didn't reach the shared store")
	}
}
