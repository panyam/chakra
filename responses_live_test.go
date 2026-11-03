package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestResponsesLiveToolLoopChains runs a tool-using turn against the real
// Responses API and checks that every step after the first chains from the
// step before. No other test checks that the server accepts the chained
// request shape. Run it with:
//
//	CHAKRA_RESPONSES_MODEL=gpt-5-mini go test -run ResponsesLive -v .
//
// The key comes from OPENAI_API_KEY; CHAKRA_RESPONSES_URL overrides the base.
func TestResponsesLiveToolLoopChains(t *testing.T) {
	model := os.Getenv("CHAKRA_RESPONSES_MODEL")
	if model == "" {
		t.Skip("CHAKRA_RESPONSES_MODEL is not set")
	}
	base := os.Getenv("CHAKRA_RESPONSES_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	rec := &chainRecorder{next: http.DefaultTransport}
	var notes []string
	p, err := NewResponsesProvider(ResponsesConfig{
		BaseURL: base, Model: model, APIKey: os.Getenv("OPENAI_API_KEY"),
		ReasoningEffort: "low",
		HTTPClient:      &http.Client{Transport: rec},
		OnFullHistory:   func(r string) { notes = append(notes, r) },
	})
	if err != nil {
		t.Fatal(err)
	}
	src := NewFuncSource()
	AddFunc(src, "lookup", "returns the secret word for a numbered slot", func(_ context.Context, in struct {
		Slot int `json:"slot"`
	}) (string, error) {
		return []string{"amber", "basalt", "cobalt"}[in.Slot%3], nil
	})
	r, err := NewRunner(RunnerConfig{Provider: p, Tools: src, Instructions: "Use the lookup tool. Call it for slot 0, then, after seeing that result, for slot 1. Then reply with both words."})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var reasoning strings.Builder
	res, err := r.Run(ctx, []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventThinkingDelta {
			reasoning.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("text=%q steps=%d usage=%+v reasoning=%d chars", res.Text, len(rec.prev), res.Usage, reasoning.Len())
	if len(rec.prev) < 2 {
		t.Fatalf("expected a multi-step turn, got %d requests", len(rec.prev))
	}
	if rec.prev[0] != "" {
		t.Fatalf("first request chained from %q", rec.prev[0])
	}
	for i, prev := range rec.prev[1:] {
		if prev == "" {
			t.Fatalf("request %d did not chain", i+2)
		}
	}
	if len(notes) != 0 {
		t.Fatalf("OnFullHistory fired: %v", notes)
	}
	if !strings.Contains(strings.ToLower(res.Text), "amber") || !strings.Contains(strings.ToLower(res.Text), "basalt") {
		t.Fatalf("answer lost a tool result: %q", res.Text)
	}
}

type chainRecorder struct {
	next http.RoundTripper
	mu   sync.Mutex
	prev []string
}

func (c *chainRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	var probe struct {
		Prev string `json:"previous_response_id"`
	}
	json.Unmarshal(body, &probe)
	c.mu.Lock()
	c.prev = append(c.prev, probe.Prev)
	c.mu.Unlock()
	return c.next.RoundTrip(req)
}

// TestResponsesLiveReasoningSummary checks that reasoning summaries arrive as
// DeltaReasoning. A trivial prompt can finish with no summary, so this one
// asks for some work.
func TestResponsesLiveReasoningSummary(t *testing.T) {
	model := os.Getenv("CHAKRA_RESPONSES_MODEL")
	if model == "" {
		t.Skip("CHAKRA_RESPONSES_MODEL is not set")
	}
	base := os.Getenv("CHAKRA_RESPONSES_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	p, err := NewResponsesProvider(ResponsesConfig{BaseURL: base, Model: model, APIKey: os.Getenv("OPENAI_API_KEY"), ReasoningEffort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := p.Stream(ctx, ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "How many primes are there between 100 and 200? Think it through."}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res := drain(t, s)
	t.Logf("text=%q reasoning=%d chars", res.Text, len(res.Reasoning))
	if res.Reasoning == "" || res.Text == "" || res.FinishReason != "stop" {
		t.Fatalf("res = %+v", res)
	}
}
