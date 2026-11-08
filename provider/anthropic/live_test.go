package anthropic

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
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

// lookupTools serves one tool that returns a stored value by name, so the
// model has to call it several times and carry what it learned between
// calls.
type lookupTools struct{}

func (lookupTools) Tools(context.Context) ([]core.ToolDef, error) {
	return []core.ToolDef{{
		Name:        "lookup",
		Description: "Returns the value stored under a name. The names are a, b and c.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}},
	}}, nil
}

func (lookupTools) Call(_ context.Context, _ string, args map[string]any) (*core.ToolResult, error) {
	v := map[string]string{"a": "17", "b": "23", "c": "41"}[fmt.Sprint(args["name"])]
	if v == "" {
		v = "no such name"
	}
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: v}}}, nil
}

// TestLiveThinkingIsResentAndAccepted runs a multi-step turn on the real API
// and checks that the provider resends thinking and the API keeps every
// block it was sent. Then it rewrites the head of the conversation, the way
// a compactor does, and runs another turn on it. That turn must not fail.
// Last, it forces a stale block onto a rewritten history, so the API has
// something to drop and drop_block is shown to be wired.
//
//	CHAKRA_ANTHROPIC_MODEL=claude-sonnet-5-5 go test -run LiveThinking -v .
func TestLiveThinkingIsResentAndAccepted(t *testing.T) {
	model := os.Getenv("CHAKRA_ANTHROPIC_MODEL")
	if model == "" {
		t.Skip("CHAKRA_ANTHROPIC_MODEL is not set")
	}
	c := sdk.NewClient()
	p, err := New(&c, Config{Model: model, MaxTokens: 4000, Effort: "medium", Thinking: "adaptive", ThinkingDisplay: "summarized"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := agent.NewRunner(agent.RunnerConfig{
		Provider:     p,
		Tools:        lookupTools{},
		MaxSteps:     8,
		Instructions: "Use the lookup tool. Call it once per name, one call per step, and think about what you have so far before each call.",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	history := []agent.Message{{Role: agent.RoleUser, Text: "Look up a, b and c, then tell me (a*b) mod c."}}
	res, err := r.Run(ctx, history, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := p.ThinkingStats()
	t.Logf("turn 1: %d steps, answer %q, usage %+v, stats %+v", res.Steps, res.Text, res.Usage, first)
	if first.Resent == 0 {
		t.Fatalf("a %d-step turn resent no thinking", res.Steps)
	}
	if first.Dropped != 0 {
		t.Fatalf("the API dropped %d of %d resent blocks", first.Dropped, first.Resent)
	}

	// A compactor replaces the head with a summary and keeps the rest.
	compacted := append([]agent.Message{{Role: agent.RoleUser, Text: "Summary so far: the user asked for (a*b) mod c, and the lookups ran."}}, res.Messages...)
	compacted = append(compacted, agent.Message{Role: agent.RoleUser, Text: "Now look up a again and tell me a+1."})
	res2, err := r.Run(ctx, compacted, nil)
	if err != nil {
		t.Fatalf("the turn after a compaction failed: %v", err)
	}
	second := p.ThinkingStats()
	t.Logf("turn 2: %d steps, answer %q, usage %+v, stats %+v", res2.Steps, res2.Text, res2.Usage, second)
	if second.Dropped != 0 {
		t.Fatalf("after a compaction the API dropped %d blocks", second.Dropped)
	}

	// Force the first stored reply onto a rewritten history, as if the
	// provider had misjudged the prefix. The API should drop the block,
	// not refuse the request.
	req := agent.ProviderRequest{Instructions: "Use the lookup tool. Call it once per name, one call per step, and think about what you have so far before each call."}
	req.Tools, _ = lookupTools{}.Tools(ctx)
	names := agent.NewToolNames(req.Tools)
	var stale storedTurn
	var assistant agent.Message
	for _, m := range res.Messages {
		if m.Role != agent.RoleAssistant {
			continue
		}
		b, _ := p.params(agent.ProviderRequest{Instructions: req.Instructions, Tools: req.Tools, Messages: history}, names)
		if t, ok := p.thinking.get(b.end.at(m)); ok {
			stale, assistant = t, m
			break
		}
	}
	if stale.blocks == 0 {
		t.Fatal("no stored reply for the first step")
	}
	edited := []agent.Message{{Role: agent.RoleUser, Text: "Look up a, b and c, then tell me (a*b) mod c. Be brief."}}
	b, _ := p.params(agent.ProviderRequest{Instructions: req.Instructions, Tools: req.Tools, Messages: edited}, names)
	p.thinking.put(b.end.at(assistant), stale)
	var results []agent.Message
	for _, tc := range assistant.ToolCalls {
		results = append(results, agent.Message{Role: agent.RoleTool, ToolCallID: tc.ID, Text: "17"})
	}
	req.Messages = append(append(edited, assistant), results...)
	before := p.ThinkingStats()
	if _, err := p.Generate(ctx, req); err != nil {
		t.Fatalf("a stale block failed the request instead of being dropped: %v", err)
	}
	after := p.ThinkingStats()
	t.Logf("forced stale resend: stats %+v -> %+v", before, after)
	if after.Dropped == before.Dropped {
		t.Fatal("the API kept a block resent after an edit, so drop_block wasn't exercised")
	}
}
