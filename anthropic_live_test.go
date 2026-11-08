package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// TestAnthropicLiveStructuredTurn runs a structured turn through the no-SDK
// provider on the real API. Current models answer 400 to the forced
// tool_choice this provider used to send (#287), so this checks that the
// output_config.format request is accepted and the turn ends with a JSON
// document that fits the schema. It spends a cent or so. Run it with:
//
//	CHAKRA_ANTHROPIC_MODEL=claude-sonnet-5-5 go test -run AnthropicLive -v .
//
// The key comes from ANTHROPIC_API_KEY.
func TestAnthropicLiveStructuredTurn(t *testing.T) {
	model := os.Getenv("CHAKRA_ANTHROPIC_MODEL")
	if model == "" {
		t.Skip("CHAKRA_ANTHROPIC_MODEL is not set")
	}
	p, err := NewAnthropicProvider(AnthropicConfig{Model: model, APIKey: os.Getenv("ANTHROPIC_API_KEY"), MaxTokens: 2000})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(RunnerConfig{
		Provider:       p,
		ResponseSchema: core.NewRawJSON(json.RawMessage(`{"type":"object","properties":{"city":{"type":"string","minLength":1},"population_millions":{"type":"number","minimum":0}},"required":["city","population_millions"]}`)),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "Name the largest city in Japan and its population in millions."}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("structured %s, usage %+v", res.Structured.Raw(), res.Usage)
	if res.StructuredError != "" {
		t.Fatalf("structured error: %s", res.StructuredError)
	}
	var got struct {
		City string  `json:"city"`
		Pop  float64 `json:"population_millions"`
	}
	if err := json.Unmarshal(res.Structured.Raw(), &got); err != nil || got.City == "" {
		t.Fatalf("structured = %s (%v)", res.Structured.Raw(), err)
	}
}
