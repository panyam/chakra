package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// fakeModel is an OpenAI-compatible endpoint that asks for one write_file call
// and then, once the tool result comes back, answers with plain text.
func fakeModel(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		answered := false
		for _, m := range req.Messages {
			if m.Role == "tool" {
				answered = true
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(chunk string) { fmt.Fprintf(w, "data: %s\n\n", chunk) }
		if answered {
			send(`{"choices":[{"delta":{"content":"done"}}]}`)
			send(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		} else {
			args, _ := json.Marshal(`{"path":"note.txt","content":"hello"}`)
			send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"write_file","arguments":` + string(args) + `}}]}}]}`)
			send(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
		}
		send("[DONE]")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// askingApp builds agentweb's --config App over a temp workspace, with every
// write gated behind an ask and the model played by fakeModel.
func askingApp(t *testing.T) (*host.App, string) {
	t.Helper()
	srv := fakeModel(t)
	cfg := writeConfig(t, fmt.Sprintf(`{"model": {"baseUrl": %q, "model": "m"}, "approval": {"mode": "ask"}}`, srv.URL))
	root := t.TempDir()
	app, err := buildApp(appOptions{configPath: cfg, workspace: []string{root}, noCheckpoint: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	return app, root
}

// TestConfigModeAsksWaitForASurface is issue 121: the empty local stdin used to
// decline every ask before a browser could answer it.
func TestConfigModeAsksWaitForASurface(t *testing.T) {
	app, root := askingApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	events := app.Subscribe(ctx)
	turn := make(chan error, 1)
	go func() { turn <- app.RunTurn(ctx, "write the note") }()

	var resolvedBy string
	for resolvedBy == "" {
		select {
		case ev := <-events:
			switch ev.Kind {
			case host.HostElicitRequest:
				// Lose the race on purpose: a local responder that answers at
				// all would have done so by now.
				time.Sleep(200 * time.Millisecond)
				if err := app.RespondToAsk(int(ev.AskID), core.ElicitationResult{Action: "accept", Content: map[string]any{"confirm": true}}, "browser"); err != nil {
					t.Fatalf("RespondToAsk: %v (the ask was already resolved by another responder)", err)
				}
			case host.HostElicitResolved:
				resolvedBy = ev.By
			}
		case <-ctx.Done():
			t.Fatal("no ask was raised or resolved")
		}
	}
	if resolvedBy != "browser" {
		t.Fatalf("ask resolved by %q, want the browser's answer", resolvedBy)
	}
	if err := <-turn; err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || strings.TrimSpace(string(b)) != "hello" {
		t.Fatalf("approved write did not land: %q, %v", b, err)
	}
}

func TestConfigModeUnansweredAskEndsWithTheTurn(t *testing.T) {
	app, root := askingApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := app.Subscribe(ctx)
	turn := make(chan error, 1)
	turnCtx, cancelTurn := context.WithCancel(ctx)
	go func() { turn <- app.RunTurn(turnCtx, "write the note") }()

	for ev := range events {
		if ev.Kind == host.HostElicitRequest {
			break
		}
	}
	cancelTurn()
	select {
	case err := <-turn:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTurn = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling the turn did not release a pending ask")
	}
	if _, err := os.Stat(filepath.Join(root, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("unanswered write ran anyway: %v", err)
	}
}
