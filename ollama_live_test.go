package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// TestOllamaServerHonorsSchemaAndSeed runs NewOllamaProvider against a real
// Ollama, the only way to see its response_format actually hold a reply to a
// schema. Any small model works:
//
//	ollama serve & ollama pull qwen2.5:0.5b
//	CHAKRA_OLLAMA_MODEL=qwen2.5:0.5b go test -run OllamaServer -v .
//
// CHAKRA_OLLAMA_URL overrides the default address.
func TestOllamaServerHonorsSchemaAndSeed(t *testing.T) {
	model := os.Getenv("CHAKRA_OLLAMA_MODEL")
	if model == "" {
		t.Skip("CHAKRA_OLLAMA_MODEL is not set")
	}
	p, err := NewOllamaProvider(OllamaConfig{BaseURL: os.Getenv("CHAKRA_OLLAMA_URL"), Model: model})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	schema := core.NewRawJSON([]byte(`{"type":"object","properties":{"answer":{"type":"string","enum":["yes","no"]},"confidence":{"type":"integer"}},"required":["answer","confidence"]}`))
	req := ProviderRequest{
		Instructions: "Answer in one word.",
		Messages:     []Message{{Role: RoleUser, Text: "Is the sky blue?"}},
		Temperature:  floatPtr(1.0),
		MaxTokens:    64,
	}
	fits := func(how, text string) {
		t.Helper()
		var v struct {
			Answer     string `json:"answer"`
			Confidence *int   `json:"confidence"`
		}
		if err := json.Unmarshal([]byte(text), &v); err != nil || (v.Answer != "yes" && v.Answer != "no") || v.Confidence == nil {
			t.Errorf("%s: reply %q is outside the schema (%v)", how, text, err)
		}
	}

	t.Run("a schema holds the reply", func(t *testing.T) {
		g := req
		g.Guided = &GuidedDecoding{JSONSchema: schema}
		for seed := range 4 {
			g.Seed = intPtr(seed)
			resp, err := p.Generate(ctx, g)
			if err != nil {
				t.Fatal(err)
			}
			fits("Generate", resp.Text)
		}
		s, err := p.Stream(ctx, g)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		fits("Stream", drain(t, s).Text)
	})

	t.Run("same seed draws the same reply", func(t *testing.T) {
		g := req
		g.Seed = intPtr(7)
		a, err := p.Generate(ctx, g)
		if err != nil {
			t.Fatal(err)
		}
		b, err := p.Generate(ctx, g)
		if err != nil {
			t.Fatal(err)
		}
		if a.Text != b.Text {
			t.Fatalf("seed 7 drew %q then %q", a.Text, b.Text)
		}
	})

	t.Run("control: without the schema the reply is free text", func(t *testing.T) {
		resp, err := p.Generate(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if json.Unmarshal([]byte(resp.Text), &v) == nil && v["answer"] != nil {
			t.Fatalf("reply %q fits the schema unasked, so the test can't tell the schema was used", resp.Text)
		}
	})
}
