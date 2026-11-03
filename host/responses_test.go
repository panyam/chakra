package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/chakra"
)

func TestDefaultProviderBuilder_OpenAIResponses(t *testing.T) {
	p, err := DefaultProviderBuilder(ConnectionConfig{Type: "openai-responses", Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*agent.ResponsesProvider); !ok {
		t.Fatalf("openai-responses built %T, want *agent.ResponsesProvider", p)
	}
	if u, _ := resolveBaseURL(ConnectionConfig{Type: "openai-responses"}); u != "https://api.openai.com/v1" {
		t.Fatalf("base = %q", u)
	}
}

// Switching between two Responses connections mid-session hands the new one
// a history it has no stored response for, so it sends the full history and
// the App reports that as a session-warn event.
func TestAppReportsResponsesFullHistory(t *testing.T) {
	var mu sync.Mutex
	var reqs []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		reqs = append(reqs, req)
		n := len(reqs)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"status\":\"completed\"}}\n\n", n)
	}))
	defer ts.Close()

	cfg := &Config{
		Connections: &ConnectionsConfig{
			Active: "a",
			Connections: map[string]ConnectionConfig{
				"a": {Type: "openai-responses", BaseURL: ts.URL, Model: "m"},
				"b": {Type: "openai-responses", BaseURL: ts.URL, Model: "m"},
			},
		},
		Approval: &ApprovalConfig{Mode: "allow"},
	}
	app, err := NewApp(cfg, io.Discard, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := app.Subscribe(ctx)

	for _, in := range []string{"one", "two"} {
		if err := app.RunTurn(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.SwitchProvider("b"); err != nil {
		t.Fatal(err)
	}
	if err := app.RunTurn(ctx, "three"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	if reqs[1]["previous_response_id"] != "resp_1" {
		t.Fatalf("the second turn on one connection should chain: %v", reqs[1])
	}
	if _, chained := reqs[2]["previous_response_id"]; chained {
		t.Fatalf("the switched-to connection cannot chain: %v", reqs[2])
	}
	mu.Unlock()

	for {
		select {
		case ev := <-events:
			if ev.Kind == HostSessionWarn && strings.Contains(ev.Err, agent.FullHistoryNoCursor) {
				return
			}
		case <-ctx.Done():
			t.Fatal("no session-warn for the full-history request")
		}
	}
}
