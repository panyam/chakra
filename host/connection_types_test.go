package host

import (
	"io"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
)

// A binary that links Anthropic's SDK builds "anthropic" connections its own
// way (#274). Every other type keeps the default, and the builder gets the
// key and base URL already resolved, so it doesn't repeat that logic.
func TestConnectionTypesOverrideOneType(t *testing.T) {
	t.Setenv("CH_TEST_KEY", "sekrit")
	var seen []ConnectionConfig
	stub := agent.NewStubProvider(agent.StubTurn{Text: "from the override"})
	types := ConnectionTypes{"anthropic": func(c ConnectionConfig) (agent.Provider, error) {
		seen = append(seen, c)
		return stub, nil
	}}

	p, err := types.Build(ConnectionConfig{Type: "anthropic", Model: "claude-sonnet-5-5", APIKeyEnv: "CH_TEST_KEY", MaxTokens: 64000})
	if err != nil {
		t.Fatal(err)
	}
	if p != stub || len(seen) != 1 {
		t.Fatalf("anthropic built %T, override called %d times", p, len(seen))
	}
	if c := seen[0]; c.APIKey != "sekrit" || c.BaseURL != "https://api.anthropic.com" || c.MaxTokens != 64000 {
		t.Errorf("the override got %+v, want the key and base URL resolved", c)
	}

	p, err = types.Build(ConnectionConfig{Type: "ollama", Model: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*agent.OpenAIProvider); !ok || len(seen) != 1 {
		t.Errorf("ollama built %T, override called %d times", p, len(seen))
	}

	p, err = types.Build(ConnectionConfig{Type: "anthropic", Model: "m", ThinkingHint: &ThinkingHint{OpenTag: "<think>", CloseTag: "</think>"}})
	if err != nil {
		t.Fatal(err)
	}
	if p == stub {
		t.Errorf("a ThinkingHint wasn't applied over the override")
	}

	if p, err := (ConnectionTypes{}).Build(ConnectionConfig{Type: "anthropic", Model: "m"}); err != nil {
		t.Fatal(err)
	} else if _, ok := p.(*agent.AnthropicProvider); !ok {
		t.Errorf("control: with no override, anthropic built %T", p)
	}
}

func TestAppUsesConnectionTypes(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Connections = &ConnectionsConfig{Active: "claude", Connections: map[string]ConnectionConfig{
		"claude": {Type: "anthropic", Model: "claude-sonnet-5-5"},
	}}
	calls := 0
	types := ConnectionTypes{"anthropic": func(c ConnectionConfig) (agent.Provider, error) {
		calls++
		return agent.NewStubProvider(agent.StubTurn{Text: "hi"}), nil
	}}
	app, err := NewApp(cfg, io.Discard, strings.NewReader(""), WithConnectionTypes(types))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if calls != 1 {
		t.Fatalf("the override built %d providers, want the active one", calls)
	}
}
