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
// 1 MB one. Start it with one slot (-np 1): with several, the same seed can
// draw differently from run to run (see NOTES.md on Consensus), which would
// make the seed check below flaky.
//
//	llama-server -m stories260K.gguf --port 18089 --chat-template chatml -np 1
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

// TestLlamaCppRunnerGuidedAnswerWithTools runs a Runner turn that offers a
// tool and holds its answer to a GBNF grammar against a real llama-server,
// the shape jaala's translator needs: tools while the model works, the
// grammar on the answer. The control runs the same turns without the grammar
// and shows the model leaving it, so the grammar keeps the answer in.
func TestLlamaCppRunnerGuidedAnswerWithTools(t *testing.T) {
	url := os.Getenv("CHAKRA_LLAMACPP_URL")
	if url == "" {
		t.Skip("CHAKRA_LLAMACPP_URL is not set")
	}
	p, err := NewLlamaCppProvider(LlamaCppConfig{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	inGrammar := regexp.MustCompile(`^ans\([a-z]{1,8}\)$`)
	run := func(seed int, g *GuidedDecoding) (*TurnResult, []ProviderRequest) {
		var reqs []ProviderRequest
		rec := recordingProvider{Provider: p, seen: &reqs}
		r, err := NewRunner(RunnerConfig{
			Provider:       rec,
			Tools:          lookSource(t),
			Instructions:   "Answer with ans(word).",
			ResponseGuided: g,
			Generation:     GenerationParams{Temperature: floatPtr(1.0), MaxTokens: 24, Seed: intPtr(seed)},
		})
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.RunTurn(context.Background(), TurnRequest{History: []Message{{Role: RoleUser, Text: "Name a colour."}}})
		if err != nil {
			t.Fatal(err)
		}
		return res, reqs
	}
	outside := 0
	for seed := range 5 {
		res, reqs := run(seed, &GuidedDecoding{Grammar: `root ::= "ans(" [a-z]{1,8} ")"`})
		if !inGrammar.MatchString(res.Text) {
			t.Fatalf("seed %d: guided answer %q is outside the grammar", seed, res.Text)
		}
		if last := reqs[len(reqs)-1]; last.Guided == nil || len(last.Tools) != 0 || len(reqs[0].Tools) == 0 {
			t.Fatalf("seed %d: want tool steps then one guided call without tools, got %d calls", seed, len(reqs))
		}
		if free, _ := run(seed, nil); !inGrammar.MatchString(free.Text) {
			outside++
		}
	}
	if outside == 0 {
		t.Fatal("control: every unguided answer fit the grammar anyway, so the test shows nothing")
	}
	t.Logf("unguided answers outside the grammar: %d of 5", outside)

	r, err := NewRunner(RunnerConfig{Provider: p, Tools: lookSource(t), ResponseGuided: &GuidedDecoding{Grammar: `root ::= "unclosed`}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RunTurn(context.Background(), TurnRequest{History: []Message{{Role: RoleUser, Text: "Name a colour."}}})
	var perr *ProviderError
	if !errors.As(err, &perr) {
		t.Fatalf("control: a broken grammar should fail the turn with the server's error, got %v", err)
	}
}

// recordingProvider passes calls through to Provider and keeps each request,
// reporting the wrapped provider's capabilities.
type recordingProvider struct {
	Provider
	seen *[]ProviderRequest
}

func (r recordingProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	*r.seen = append(*r.seen, req)
	return r.Provider.Stream(ctx, req)
}

func (r recordingProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	*r.seen = append(*r.seen, req)
	return r.Provider.Generate(ctx, req)
}

func (r recordingProvider) ProviderCapabilities() Capabilities { return CapabilitiesOf(r.Provider) }
