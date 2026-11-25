package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
)

func twoConn() *ConnectionsConfig {
	return &ConnectionsConfig{
		Active: "local",
		Connections: map[string]ConnectionConfig{
			"local": {Type: "lmstudio", Model: "qwen"},
			"cloud": {Type: "openai", Model: "gpt-5", APIKeyEnv: "OPENAI_KEY"},
		},
	}
}

// namedStub is a StubProvider tagged with the connection name it was
// built for, so a test can assert which connection is active.
type namedStub struct {
	*agent.StubProvider
	name string
}

func stubBuilder(t *testing.T) ProviderBuilder {
	// maps model -> connection name for the twoConn fixture
	byModel := map[string]string{"qwen": "local", "gpt-5": "cloud"}
	return func(c ConnectionConfig) (agent.Provider, error) {
		name := byModel[c.Model]
		turns := make([]agent.StubTurn, 8)
		for i := range turns {
			turns[i] = agent.StubTurn{Text: "reply from " + name}
		}
		return &namedStub{StubProvider: agent.NewStubProvider(turns...), name: name}, nil
	}
}

func TestConnectionRegistry_LoadAndActive(t *testing.T) {
	reg, err := newConnectionRegistry(twoConn(), stubBuilder(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Names(); len(got) != 2 || got[0] != "cloud" || got[1] != "local" {
		t.Fatalf("Names() = %v, want sorted [cloud local]", got)
	}
	if reg.Active() != "local" {
		t.Fatalf("Active() = %q, want local", reg.Active())
	}
	p, err := reg.ActiveProvider()
	if err != nil {
		t.Fatal(err)
	}
	if p.(*namedStub).name != "local" {
		t.Fatalf("active provider is %q, want local", p.(*namedStub).name)
	}
}

func TestConnectionRegistry_SetActiveAndCache(t *testing.T) {
	reg, err := newConnectionRegistry(twoConn(), stubBuilder(t))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := reg.ActiveProvider()

	p, err := reg.SetActive("cloud")
	if err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if p.(*namedStub).name != "cloud" || reg.Active() != "cloud" {
		t.Fatalf("SetActive(cloud) did not switch: active=%q provider=%q", reg.Active(), p.(*namedStub).name)
	}
	// unknown name leaves active unchanged
	if _, err := reg.SetActive("nope"); err == nil {
		t.Fatal("SetActive(unknown) succeeded, want error")
	}
	if reg.Active() != "cloud" {
		t.Fatalf("failed SetActive changed active to %q", reg.Active())
	}
	// switching back returns the SAME cached instance
	back, _ := reg.SetActive("local")
	if back != first {
		t.Fatal("SetActive did not return the cached provider on revisit")
	}
}

func TestConnectionRegistry_ValidationErrors(t *testing.T) {
	cases := map[string]*ConnectionsConfig{
		"no active":        {Connections: map[string]ConnectionConfig{"a": {Type: "lmstudio", Model: "m"}}},
		"active undefined": {Active: "b", Connections: map[string]ConnectionConfig{"a": {Type: "lmstudio", Model: "m"}}},
		"no model":         {Active: "a", Connections: map[string]ConnectionConfig{"a": {Type: "lmstudio"}}},
		"unknown type":     {Active: "a", Connections: map[string]ConnectionConfig{"a": {Type: "bogus", Model: "m"}}},
		"empty":            {Active: "a"},
	}
	for name, cfg := range cases {
		if _, err := newConnectionRegistry(cfg, stubBuilder(t)); err == nil {
			t.Fatalf("%s: newConnectionRegistry succeeded, want error", name)
		}
	}
}

// TestEmbedderRole covers the ConnectionsConfig.Embedder selector: the lookup
// helper and registry validation of a bad/anthropic embedder name.
func TestEmbedderRole(t *testing.T) {
	cfg := &ConnectionsConfig{
		Active:   "chat",
		Embedder: "emb",
		Connections: map[string]ConnectionConfig{
			"chat": {Type: "openai", Model: "gpt-4o"},
			"emb":  {Type: "openai", Model: "text-embedding-3-small", Dim: 1536},
		},
	}
	conn, ok := cfg.EmbedderConnection()
	if !ok || conn.Model != "text-embedding-3-small" || conn.Dim != 1536 {
		t.Fatalf("EmbedderConnection = %+v, ok=%v", conn, ok)
	}
	if _, err := newConnectionRegistry(cfg, stubBuilder(t)); err != nil {
		t.Fatalf("valid embedder role should build: %v", err)
	}

	bad := *cfg
	bad.Embedder = "nope"
	if _, err := newConnectionRegistry(&bad, stubBuilder(t)); err == nil {
		t.Fatal("unknown embedder name should error")
	}

	anth := &ConnectionsConfig{
		Active:   "chat",
		Embedder: "chat",
		Connections: map[string]ConnectionConfig{
			"chat": {Type: "anthropic", Model: "claude-sonnet-5"},
		},
	}
	if _, err := newConnectionRegistry(anth, stubBuilder(t)); err == nil {
		t.Fatal("anthropic embedder connection should be rejected")
	}
}

// TestConnectionsConfigJSON pins the llm.json-inspired shape decodes.
func TestConnectionsConfigJSON(t *testing.T) {
	raw := `{
      "active": "local",
      "embedder": "emb",
      "connections": {
        "local": {"type": "lmstudio", "model": "qwen"},
        "emb":   {"type": "openai", "model": "text-embedding-3-small", "dim": 1536, "apiKeyEnv": "OPENAI_API_KEY"},
        "think": {"type": "lmstudio", "model": "deepseek",
                  "thinkingHint": {"openTag": "<think>", "closeTag": "</think>"}}
      }
    }`
	var c ConnectionsConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Active != "local" || c.Embedder != "emb" || len(c.Connections) != 3 {
		t.Fatalf("decoded wrong: %+v", c)
	}
	if h := c.Connections["think"].ThinkingHint; h == nil || h.OpenTag != "<think>" {
		t.Fatalf("thinkingHint not decoded: %+v", c.Connections["think"])
	}
	if e := c.Connections["emb"]; e.Dim != 1536 {
		t.Fatalf("dim tag not decoded: %+v", e)
	}
}

func TestApp_ProviderSwitchAtRuntime(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Connections = twoConn()

	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProviderBuilder(stubBuilder(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	names, active := app.Providers()
	if len(names) != 2 || active != "local" {
		t.Fatalf("Providers() = (%v, %q)", names, active)
	}

	if err := app.SwitchProvider("cloud"); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}
	if _, active := app.Providers(); active != "cloud" {
		t.Fatalf("active after switch = %q, want cloud", active)
	}
	// the Runner now streams from the cloud provider
	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reply from cloud") {
		t.Fatalf("turn did not use the switched provider:\n%s", out.String())
	}
}

func TestApp_NoConnectionsProvidersEmpty(t *testing.T) {
	ts := startTestServer(t)
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if names, active := app.Providers(); names != nil || active != "" {
		t.Fatalf("Providers() without registry = (%v, %q), want empty", names, active)
	}
	if err := app.SwitchProvider("x"); err == nil {
		t.Fatal("SwitchProvider without registry succeeded, want error")
	}
}

// A llamacpp connection needs no model and builds a provider that takes a
// grammar and a seed; an anthropic one reports neither; ollama a JSON schema
// and a seed.
// The runtime switch reports whichever is active.
func TestLocalConnectionsReportWhatTheyCanSend(t *testing.T) {
	var c ConnectionsConfig
	if err := json.Unmarshal([]byte(`{
      "active": "local",
      "connections": {
        "local":  {"type": "llamacpp"},
        "ollama": {"type": "ollama", "model": "qwen2.5-coder:7b"},
        "claude": {"type": "anthropic", "model": "claude-sonnet-5", "apiKeyEnv": "X"},
        "gpt4":   {"type": "openai", "model": "gpt-4.1-mini"},
        "gpt5":   {"type": "openai", "model": "gpt-5.5", "temperature": false},
        "resp":   {"type": "openai-responses", "model": "gpt-5.5"},
        "resp41": {"type": "openai-responses", "model": "gpt-4.1-mini", "temperature": true}
      }
    }`), &c); err != nil {
		t.Fatal(err)
	}
	r, err := newConnectionRegistry(&c, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]agent.Capabilities{
		"local":  {Guided: []string{agent.GuidedGrammar, agent.GuidedJSONSchema}, Seed: true, Temperature: true},
		"ollama": {Guided: []string{agent.GuidedJSONSchema}, Seed: true, Temperature: true},
		"claude": {},
		"gpt4":   {Seed: true, Temperature: true},
		"gpt5":   {Seed: true},
		"resp":   {},
		"resp41": {Temperature: true},
	} {
		p, err := r.Provider(name)
		if err != nil {
			t.Fatal(err)
		}
		got := agent.CapabilitiesOf(p)
		if strings.Join(got.Guided, ",") != strings.Join(want.Guided, ",") || got.Seed != want.Seed || got.Temperature != want.Temperature {
			t.Errorf("%s: %+v, want %+v", name, got, want)
		}
	}
	local, _ := r.Provider("local")
	claude, _ := r.Provider("claude")
	sw := newProviderSwitch(local)
	if !agent.CapabilitiesOf(sw).Seed {
		t.Errorf("switch on llamacpp reports no seed")
	}
	sw.set(claude)
	if agent.CapabilitiesOf(sw).Seed {
		t.Errorf("switch on anthropic still reports a seed")
	}
	if agent.CapabilitiesOf(sw).Temperature {
		t.Errorf("switch on anthropic still reports a temperature")
	}
	sw.set(local)
	if !agent.CapabilitiesOf(sw).Temperature {
		t.Errorf("switch back on llamacpp reports no temperature")
	}
	c.Connections["bare"] = ConnectionConfig{Type: "openai"}
	if _, err := newConnectionRegistry(&c, nil); err == nil || !strings.Contains(err.Error(), "has no model") {
		t.Errorf("control: an openai connection without a model: %v", err)
	}
}
