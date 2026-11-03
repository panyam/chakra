package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// TestLlamaCppServerHonorsSeedAndGuided runs NewLlamaCppProvider against a real
// llama-server, which is the only way to see a grammar actually constrain a
// model. Any model works since the constraint is what's tested; jaala keeps a
// 1 MB one:
//
//	llama-server -m stories260K.gguf --port 18089 --chat-template chatml
//	CHAKRA_LLAMACPP_URL=http://127.0.0.1:18089/v1 go test -run LlamaCppServer -v .
func TestLlamaCppServerHonorsSeedAndGuided(t *testing.T) {
	url := os.Getenv("CHAKRA_LLAMACPP_URL")
	if url == "" {
		t.Skip("CHAKRA_LLAMACPP_URL is not set")
	}
	p, err := NewLlamaCppProvider(LlamaCppConfig{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	if c := CapabilitiesOf(p); !c.Seed || !c.CanGuide(&GuidedDecoding{Grammar: "g"}) {
		t.Fatalf("the preset reports %+v, want grammar and seed", c)
	}
	ctx := context.Background()
	ask := func(seed int, g GuidedDecoding) (string, error) {
		resp, err := p.Generate(ctx, ProviderRequest{
			Instructions: "Answer.",
			Messages:     []Message{{Role: RoleUser, Text: "Is the sky blue?"}},
			Temperature:  floatPtr(1.0),
			MaxTokens:    48,
			Seed:         intPtr(seed),
			Guided:       &g,
		})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	}

	t.Run("grammar constrains the reply", func(t *testing.T) {
		for seed := range 8 {
			got, err := ask(seed, GuidedDecoding{Grammar: `root ::= "yes" | "no"`})
			if err != nil {
				t.Fatal(err)
			}
			if got != "yes" && got != "no" {
				t.Fatalf("seed %d: reply %q is outside the grammar", seed, got)
			}
		}
	})

	t.Run("same seed draws the same reply", func(t *testing.T) {
		free := GuidedDecoding{Grammar: `root ::= [a-z ]{8,40}`}
		a, err := ask(7, free)
		if err != nil {
			t.Fatal(err)
		}
		b, err := ask(7, free)
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("seed 7 drew %q then %q", a, b)
		}
		if !regexp.MustCompile(`^[a-z ]{8,40}$`).MatchString(a) {
			t.Fatalf("reply %q is outside the grammar", a)
		}
	})

	t.Run("json schema constrains the reply", func(t *testing.T) {
		schema := core.NewRawJSON([]byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`))
		got, err := ask(1, GuidedDecoding{JSONSchema: schema})
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			OK *bool `json:"ok"`
		}
		if err := json.Unmarshal([]byte(got), &v); err != nil || v.OK == nil {
			t.Fatalf("reply %q does not match the schema: %v", got, err)
		}
	})

	t.Run("control: a broken grammar is rejected by the server", func(t *testing.T) {
		_, err := ask(1, GuidedDecoding{Grammar: `root ::= (`})
		var perr *ProviderError
		if !errors.As(err, &perr) || perr.StatusCode != 400 {
			t.Fatalf("broken grammar answered %v, so the server may not read grammars at all", err)
		}
	})

	t.Run("a shared prefix is reported as cached", func(t *testing.T) {
		req := ProviderRequest{
			Instructions: "Once upon a time a little girl named Lily played in the park with her dog every day.",
			Messages:     []Message{{Role: RoleUser, Text: "Tell me"}},
			MaxTokens:    4,
			Seed:         intPtr(1),
		}
		if _, err := p.Generate(ctx, req); err != nil {
			t.Fatal(err)
		}
		check := func(how string, u *Usage) {
			if u == nil || u.CachedInputTokens <= 0 || u.CachedInputTokens > u.InputTokens {
				t.Errorf("%s: usage %+v, want 0 < CachedInputTokens <= InputTokens", how, u)
			}
		}
		resp, err := p.Generate(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		check("Generate", resp.Usage)
		s, err := p.Stream(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		check("Stream", drain(t, s).Usage)
	})
}
