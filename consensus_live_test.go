package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestLlamaCppConsensusRepeatsWithTheSameSeeds runs a vote twice against a
// real llama-server at temperature 1, sequentially. The same seeds must give
// the same draws, with the second vote running on the prefix the first one
// cached. It needs a single-slot server: with several slots the same seed
// lands on a different slot's cache and drew differently in 2 of 8 runs, so
// a server started without -np 1 is skipped rather than failed.
//
//	llama-server -m stories260K.gguf --port 18089 --chat-template chatml -np 1
//	CHAKRA_LLAMACPP_URL=http://127.0.0.1:18089/v1 go test -run LlamaCppConsensus -v .
func TestLlamaCppConsensusRepeatsWithTheSameSeeds(t *testing.T) {
	url := os.Getenv("CHAKRA_LLAMACPP_URL")
	if url == "" {
		t.Skip("CHAKRA_LLAMACPP_URL is not set")
	}
	if n := llamaSlots(t, url); n != 1 {
		t.Skipf("the server has %d slots; start it with -np 1 for reproducible draws", n)
	}
	p, err := NewLlamaCppProvider(LlamaCppConfig{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(RunnerConfig{Provider: p, Instructions: "Tell a story.", Generation: GenerationParams{Temperature: floatPtr(1.0), MaxTokens: 24}})
	if err != nil {
		t.Fatal(err)
	}
	c := &Consensus{Runner: r, Draws: 4, Key: func(res *TurnResult) (string, bool) { return res.Text, res.Text != "" }}
	req := TurnRequest{History: []Message{{Role: RoleUser, Text: "Once upon a time"}}}
	first, err := c.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Seeded {
		t.Fatal("llama.cpp takes a seed, so the draws should be seeded")
	}
	distinct := map[string]bool{}
	for i := range first.Draws {
		a, b := first.Draws[i], second.Draws[i]
		t.Logf("seed %d: %q", i, a.Key)
		if a.Err != "" || b.Err != "" {
			t.Fatalf("draw %d failed: %q / %q", i, a.Err, b.Err)
		}
		if a.Key != b.Key {
			t.Fatalf("seed %d drew %q then %q", i, a.Key, b.Key)
		}
		distinct[a.Key] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("four seeds at temperature 1 gave one answer, so seeds aren't reaching the sampler")
	}
	t.Logf("vote: %q %d/%d split=%v", first.WinnerKey, first.Agree, first.Counted, first.Split)
}

// llamaSlots reads total_slots from llama-server's /props.
func llamaSlots(t *testing.T, base string) int {
	t.Helper()
	resp, err := http.Get(strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/v1") + "/props")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var props struct {
		TotalSlots int `json:"total_slots"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&props); err != nil {
		t.Fatal(err)
	}
	return props.TotalSlots
}
