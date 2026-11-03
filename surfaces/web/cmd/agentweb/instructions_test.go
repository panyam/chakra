package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestInstructionsFlagReachesTheSession checks --instructions picks how a
// workspace's AGENTS.md loads: on by default, off when asked.
func TestInstructionsFlagReachesTheSession(t *testing.T) {
	var mu sync.Mutex
	var systems []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req.Messages {
			if m.Role == "system" {
				mu.Lock()
				systems = append(systems, m.Content)
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	cfg := writeConfig(t, fmt.Sprintf(`{"model": {"baseUrl": %q, "model": "m"}}`, srv.URL))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Never edit the tests."), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		instructions string
		want         bool
	}{{"", true}, {"codex", true}, {"off", false}} {
		systems = nil
		app, err := buildApp(appOptions{configPath: cfg, workspace: []string{root}, noCheckpoint: true, instructions: c.instructions})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.RunTurn(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		app.Close()
		mu.Lock()
		got := strings.Contains(strings.Join(systems, "\n"), "Never edit the tests.")
		mu.Unlock()
		if got != c.want {
			t.Errorf("--instructions %q: AGENTS.md in the system prompt = %v, want %v", c.instructions, got, c.want)
		}
	}

	if err := checkInstructions("no-such-agent"); err == nil {
		t.Error("an unknown preset passed the startup check")
	}
}
