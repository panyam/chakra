package host

import (
	"io"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
)

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
