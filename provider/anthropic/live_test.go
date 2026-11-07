package anthropic

import (
	"context"
	"os"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	agent "github.com/panyam/chakra"
)

// TestLiveSecondCallReadsTheCache runs against the real API. Two calls share
// a long prefix, and the second one should read most of it from the cache.
// It spends a few cents, so it runs only when a model is named:
//
//	CHAKRA_ANTHROPIC_MODEL=claude-sonnet-5-5 go test -run Live -v .
//
// The SDK finds the key itself, from ANTHROPIC_API_KEY or an `ant auth
// login` profile.
func TestLiveSecondCallReadsTheCache(t *testing.T) {
	model := os.Getenv("CHAKRA_ANTHROPIC_MODEL")
	if model == "" {
		t.Skip("CHAKRA_ANTHROPIC_MODEL is not set")
	}
	c := sdk.NewClient()
	p, err := New(&c, Config{Model: model, MaxTokens: 64, Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	// The minimum cacheable prefix is model-dependent, at most 4096 tokens.
	rules := strings.Repeat("When you edit Go code, run gofmt, keep exported names documented, and prefer table tests over copy-pasted cases. ", 400)
	req := agent.ProviderRequest{
		Instructions: "You are a terse assistant. " + rules,
		Messages:     []agent.Message{{Role: agent.RoleUser, Text: "Reply with the word ok."}},
	}
	ctx := context.Background()
	first, err := p.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first %+v, second %+v", *first.Usage, *second.Usage)
	if second.Usage.CachedInputTokens == 0 || second.Usage.CachedInputTokens*2 < second.Usage.InputTokens {
		t.Fatalf("second call read %d of %d input tokens from the cache, want most of them", second.Usage.CachedInputTokens, second.Usage.InputTokens)
	}

	s, err := p.Stream(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var u *agent.Usage
	for {
		d, err := s.Recv()
		if err != nil {
			break
		}
		if d.Kind == agent.DeltaUsage {
			u = d.Usage
		}
	}
	if u == nil || u.CachedInputTokens == 0 {
		t.Fatalf("a streamed call reported usage %+v, want cache reads", u)
	}
}
