package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

func TestResolveBaseURL(t *testing.T) {
	if u, _ := ResolveBaseURL(Config{Type: "lmstudio"}); u != "http://localhost:1234/v1" {
		t.Fatalf("lmstudio base = %q", u)
	}
	if u, _ := ResolveBaseURL(Config{Type: "lmstudio", BaseURL: "http://x/v1"}); u != "http://x/v1" {
		t.Fatalf("BaseURL should override type, got %q", u)
	}
	if _, err := ResolveBaseURL(Config{Model: "m"}); err == nil {
		t.Fatal("no type and no baseUrl should error")
	}
	if u, _ := ResolveBaseURL(Config{Type: "gemini"}); u != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatalf("gemini base = %q", u)
	}
	if u, _ := ResolveBaseURL(Config{Type: "anthropic"}); u != "https://api.anthropic.com" {
		t.Fatalf("anthropic base = %q", u)
	}
	if u, _ := ResolveBaseURL(Config{Type: "openrouter"}); u != "https://openrouter.ai/api/v1" {
		t.Fatalf("openrouter base = %q", u)
	}
	if u, _ := ResolveBaseURL(Config{Type: "litellm"}); u != "http://localhost:4000/v1" {
		t.Fatalf("litellm base = %q", u)
	}
	// a router connection can override the preset base (hosted litellm)
	if u, _ := ResolveBaseURL(Config{Type: "litellm", BaseURL: "https://gw.example/v1"}); u != "https://gw.example/v1" {
		t.Fatalf("litellm BaseURL override = %q", u)
	}
}

// TestThinkingHintWrapsProvider pins that a ThinkingHint on a connection makes
// Build wrap the provider in the inline-reasoning parser, and
// that no hint leaves it unwrapped.
func TestThinkingHintWrapsProvider(t *testing.T) {
	plain, err := Build(Config{Type: "lmstudio", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.(*agent.OpenAIProvider); !ok {
		t.Fatalf("no hint should be the bare provider, got %T", plain)
	}
	hinted, err := Build(Config{
		Type: "lmstudio", Model: "m",
		ThinkingHint: &ThinkingHint{OpenTag: "<think>", CloseTag: "</think>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hinted.(*agent.OpenAIProvider); ok {
		t.Fatal("a ThinkingHint should wrap the provider, but got the bare *agent.OpenAIProvider")
	}
}

// TestRouterPresets_BuildOpenAIWire pins that the router types build the
// OpenAI-wire provider (they are gateways, not a native provider).
func TestRouterPresets_BuildOpenAIWire(t *testing.T) {
	for _, typ := range []string{"openrouter", "litellm"} {
		p, err := Build(Config{Type: typ, Model: "m"})
		if err != nil {
			t.Fatalf("%s build: %v", typ, err)
		}
		if _, ok := p.(*agent.OpenAIProvider); !ok {
			t.Fatalf("%s built %T, want *agent.OpenAIProvider", typ, p)
		}
	}
}

// TestBuildProviderType pins that only "anthropic" routes to
// the native Messages-API provider; every other type (gemini included) builds
// the OpenAI-wire provider.
func TestBuildProviderType(t *testing.T) {
	anth, err := Build(Config{Type: "anthropic", Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatalf("anthropic build: %v", err)
	}
	if _, ok := anth.(*agent.AnthropicProvider); !ok {
		t.Fatalf("anthropic type built %T, want *agent.AnthropicProvider", anth)
	}
	gem, err := Build(Config{Type: "gemini", Model: "gemini-2.5-flash"})
	if err != nil {
		t.Fatalf("gemini build: %v", err)
	}
	if _, ok := gem.(*agent.OpenAIProvider); !ok {
		t.Fatalf("gemini type built %T, want *agent.OpenAIProvider", gem)
	}
}

// TestBuildEmbedder pins that openai/gemini connections build an OpenAI-wire
// embedder and anthropic (no embeddings API) is rejected.
func TestBuildEmbedder(t *testing.T) {
	for _, typ := range []string{"openai", "gemini", "lmstudio"} {
		e, err := BuildEmbedder(Config{Type: typ, Model: "embed-model", Dim: 1536}, nil)
		if err != nil {
			t.Fatalf("%s embedder: %v", typ, err)
		}
		if _, ok := e.(*agent.OpenAIEmbedder); !ok {
			t.Fatalf("%s built %T, want *agent.OpenAIEmbedder", typ, e)
		}
	}
	if _, err := BuildEmbedder(Config{Type: "anthropic", Model: "claude"}, nil); err == nil {
		t.Fatal("anthropic embedder should error (no embeddings API)")
	}
}

// "responseSchema": false on a connection makes the built provider ask for
// json_object rather than json_schema, so the structured finalize can run on
// DeepSeek (#215). As a control, a connection without it still sends
// json_schema.
func TestResponseSchemaFalseSendsJSONObject(t *testing.T) {
	var format string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		format = body.ResponseFormat.Type
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`)
	}))
	defer ts.Close()

	for _, tc := range []struct{ conn, want string }{
		{`{"baseUrl": "` + ts.URL + `", "model": "deepseek-chat", "responseSchema": false}`, "json_object"},
		{`{"baseUrl": "` + ts.URL + `", "model": "gpt-4.1-mini"}`, "json_schema"},
	} {
		var conn Config
		if err := json.Unmarshal([]byte(tc.conn), &conn); err != nil {
			t.Fatal(err)
		}
		p, err := Build(conn)
		if err != nil {
			t.Fatal(err)
		}
		format = ""
		if _, err := p.Generate(context.Background(), agent.ProviderRequest{
			Messages:       []agent.Message{{Role: agent.RoleUser, Text: "hi"}},
			ResponseSchema: core.NewRawJSON(json.RawMessage(`{"type":"object"}`)),
		}); err != nil {
			t.Fatal(err)
		}
		if format != tc.want {
			t.Errorf("%s sent response_format %q, want %q", tc.conn, format, tc.want)
		}
	}
}

func TestCost(t *testing.T) {
	cachedRate := 0.25
	usage := &agent.Usage{InputTokens: 1_000_000, CachedInputTokens: 400_000, OutputTokens: 100_000}
	cases := []struct {
		name   string
		price  *Price
		usage  *agent.Usage
		want   float64
		wantOK bool
	}{
		{"cached tokens at the cached rate", &Price{Input: 2, CachedInput: &cachedRate, Output: 10}, usage, 0.6*2 + 0.4*0.25 + 0.1*10, true},
		{"cached tokens at the input rate when no cached rate", &Price{Input: 2, Output: 10}, usage, 1*2 + 0.1*10, true},
		{"no price is unknown", nil, usage, 0, false},
		{"no usage is unknown", &Price{Input: 2, Output: 10}, nil, 0, false},
		{"cached above input is capped", &Price{Input: 1, CachedInput: &cachedRate, Output: 0}, &agent.Usage{InputTokens: 100, CachedInputTokens: 500}, 100 * 0.25 / 1e6, true},
	}
	for _, c := range cases {
		got, ok := Config{Price: c.price}.Cost(c.usage)
		if ok != c.wantOK || math.Abs(got-c.want) > 1e-12 {
			t.Errorf("%s: Cost = %v, %v; want %v, %v", c.name, got, ok, c.want, c.wantOK)
		}
	}
}

func TestPriceLoadsFromJSON(t *testing.T) {
	var conn Config
	if err := json.Unmarshal([]byte(`{"type":"openai-responses","model":"gpt-5.5","price":{"input":1.25,"cachedInput":0.125,"output":10}}`), &conn); err != nil {
		t.Fatal(err)
	}
	if conn.Price == nil || conn.Price.Input != 1.25 || conn.Price.CachedInput == nil || *conn.Price.CachedInput != 0.125 || conn.Price.Output != 10 {
		t.Errorf("price = %+v", conn.Price)
	}
}

// A key set on the connection reaches the endpoint and wins over APIKeyEnv,
// and it never appears when the connection is marshalled into a config.
func TestBuildAPIKeyInHand(t *testing.T) {
	t.Setenv("SOME_KEY_ENV", "from-env")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		http.Error(w, "no", http.StatusTeapot)
	}))
	defer srv.Close()

	conn := Config{BaseURL: srv.URL, Model: "m", APIKey: "in-hand", APIKeyEnv: "SOME_KEY_ENV"}
	p, err := Build(conn)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, _ = p.Generate(context.Background(), agent.ProviderRequest{Messages: []agent.Message{{Role: agent.RoleUser, Text: "hi"}}})
	if got != "Bearer in-hand" {
		t.Fatalf("Authorization = %q, want the in-hand key", got)
	}

	b, err := json.Marshal(conn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "in-hand") {
		t.Fatalf("marshalled connection carries the key: %s", b)
	}
}

// An anthropic connection's maxTokens reaches the request as max_tokens, and
// a connection without it keeps the provider's default (#264).
func TestBuildAnthropicMaxTokens(t *testing.T) {
	var got []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body.MaxTokens)
		http.Error(w, "no", http.StatusTeapot)
	}))
	defer srv.Close()

	var conn Config
	if err := json.Unmarshal([]byte(`{"type":"anthropic","baseUrl":"`+srv.URL+`","model":"m","maxTokens":64000}`), &conn); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Config{conn, {Type: "anthropic", BaseURL: srv.URL, Model: "m"}} {
		p, err := Build(c)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		_, _ = p.Generate(context.Background(), agent.ProviderRequest{Messages: []agent.Message{{Role: agent.RoleUser, Text: "hi"}}})
	}
	if len(got) != 2 || got[0] != 64000 || got[1] != 4096 {
		t.Fatalf("max_tokens sent = %v, want [64000 4096]", got)
	}
}

// A binary that links Anthropic's SDK builds "anthropic" connections its own
// way (#274). Every other type keeps the default, and the builder gets the
// key and base URL already resolved, so it doesn't repeat that logic.
func TestTypesOverrideOneType(t *testing.T) {
	t.Setenv("CH_TEST_KEY", "sekrit")
	var seen []Config
	stub := agent.NewStubProvider(agent.StubTurn{Text: "from the override"})
	types := Types{"anthropic": func(c Config) (agent.Provider, error) {
		seen = append(seen, c)
		return stub, nil
	}}

	p, err := types.Build(Config{Type: "anthropic", Model: "claude-sonnet-5-5", APIKeyEnv: "CH_TEST_KEY", MaxTokens: 64000})
	if err != nil {
		t.Fatal(err)
	}
	if p != stub || len(seen) != 1 {
		t.Fatalf("anthropic built %T, override called %d times", p, len(seen))
	}
	if c := seen[0]; c.APIKey != "sekrit" || c.BaseURL != "https://api.anthropic.com" || c.MaxTokens != 64000 {
		t.Errorf("the override got %+v, want the key and base URL resolved", c)
	}

	p, err = types.Build(Config{Type: "ollama", Model: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*agent.OpenAIProvider); !ok || len(seen) != 1 {
		t.Errorf("ollama built %T, override called %d times", p, len(seen))
	}

	p, err = types.Build(Config{Type: "anthropic", Model: "m", ThinkingHint: &ThinkingHint{OpenTag: "<think>", CloseTag: "</think>"}})
	if err != nil {
		t.Fatal(err)
	}
	if p == stub {
		t.Errorf("a ThinkingHint wasn't applied over the override")
	}

	if p, err := (Types{}).Build(Config{Type: "anthropic", Model: "m"}); err != nil {
		t.Fatal(err)
	} else if _, ok := p.(*agent.AnthropicProvider); !ok {
		t.Errorf("control: with no override, anthropic built %T", p)
	}
}

// An embedder connection with its key in hand sends that key, as Build does,
// and it wins over APIKeyEnv.
func TestBuildEmbedderAPIKeyInHand(t *testing.T) {
	t.Setenv("SOME_KEY_ENV", "from-env")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		http.Error(w, "no", http.StatusTeapot)
	}))
	defer srv.Close()

	e, err := BuildEmbedder(Config{BaseURL: srv.URL, Model: "m", APIKey: "in-hand", APIKeyEnv: "SOME_KEY_ENV"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.Embed(context.Background(), []string{"hi"})
	if got != "Bearer in-hand" {
		t.Fatalf("Authorization = %q, want the in-hand key", got)
	}
}

// A local connection's maxTokens reaches the request, and with none set the
// preset's default does (agent.DefaultLocalMaxTokens).
func TestLocalConnectionsCapEachReply(t *testing.T) {
	var sent []int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		sent = append(sent, body.MaxTokens)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer ts.Close()
	for _, c := range []Config{
		{Type: "llamacpp", BaseURL: ts.URL},
		{Type: "llamacpp", BaseURL: ts.URL, MaxTokens: 777},
		{Type: "ollama", BaseURL: ts.URL, Model: "qwen2.5-coder:14b", MaxTokens: 778},
	} {
		p, err := Build(c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Generate(context.Background(), agent.ProviderRequest{Messages: []agent.Message{{Role: agent.RoleUser, Text: "q"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(sent) != fmt.Sprint([]int{agent.DefaultLocalMaxTokens, 777, 778}) {
		t.Errorf("max_tokens sent %v", sent)
	}
}
