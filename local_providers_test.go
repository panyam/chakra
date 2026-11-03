package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// recordingServer answers every chat completion with "ok" and keeps the
// last request body and how many requests arrived.
func recordingServer(t *testing.T) (*httptest.Server, *json.RawMessage, *atomic.Int32) {
	t.Helper()
	var body json.RawMessage
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(ts.Close)
	return ts, &body, &hits
}

// The llama.cpp preset sends what jaala's llamacpp client sends, the same
// golden TestOpenAILlamaCppRequestGolden pins for a hand-built config.
func TestLlamaCppPresetSendsTheGoldenRequest(t *testing.T) {
	ts, body, _ := recordingServer(t)
	p, err := NewLlamaCppProvider(LlamaCppConfig{BaseURL: ts.URL, Model: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Generate(context.Background(), ProviderRequest{
		Instructions: "card",
		Messages:     []Message{{Role: RoleUser, Text: "question"}},
		Temperature:  floatPtr(0.7),
		MaxTokens:    512,
		Seed:         intPtr(3),
		Guided:       &GuidedDecoding{Grammar: `root ::= "a"`},
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/llamacpp_request.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, exp := canonJSON(t, *body), canonJSON(t, want); got != exp {
		t.Fatalf("request body\n got %s\nwant %s", got, exp)
	}
}

// With nothing set, the preset points at llama-server's default address and
// sends a placeholder model, since chakra always sends one.
func TestLlamaCppPresetDefaults(t *testing.T) {
	p, err := NewLlamaCppProvider(LlamaCppConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if p.cfg.BaseURL != "http://localhost:8080/v1" || p.cfg.Model != "llama.cpp" {
		t.Errorf("defaults: base %q, model %q", p.cfg.BaseURL, p.cfg.Model)
	}
	ts, body, _ := recordingServer(t)
	p, _ = NewLlamaCppProvider(LlamaCppConfig{BaseURL: ts.URL})
	if _, err := p.Generate(context.Background(), ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "q"}}}); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Model       string   `json:"model"`
		Temperature *float64 `json:"temperature"`
	}
	json.Unmarshal(*body, &sent)
	if sent.Model != "llama.cpp" || sent.Temperature != nil {
		t.Errorf("sent model %q, temperature %v: the preset must not pick a temperature", sent.Model, sent.Temperature)
	}
}

// Ollama's preset sends a seed and refuses a grammar before any request
// leaves. control: the same request without Guided goes out.
func TestOllamaPresetSendsSeedAndRefusesAGrammar(t *testing.T) {
	ts, body, hits := recordingServer(t)
	p, err := NewOllamaProvider(OllamaConfig{BaseURL: ts.URL, Model: "qwen2.5-coder:7b"})
	if err != nil {
		t.Fatal(err)
	}
	req := ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "q"}}, Seed: intPtr(4), Guided: &GuidedDecoding{Grammar: `root ::= "a"`}}
	_, gerr := p.Generate(context.Background(), req)
	_, serr := p.Stream(context.Background(), req)
	if !errors.Is(gerr, ErrUnsupportedParam) || !errors.Is(serr, ErrUnsupportedParam) || hits.Load() != 0 {
		t.Fatalf("grammar: %v / %v, %d requests sent", gerr, serr, hits.Load())
	}
	req.Guided = nil
	if _, err := p.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	json.Unmarshal(*body, &sent)
	if sent["seed"] != float64(4) || sent["model"] != "qwen2.5-coder:7b" || sent["grammar"] != nil || sent["cache_prompt"] != nil {
		t.Errorf("sent %v", sent)
	}
}

func TestOllamaPresetDefaultsAndRequiresAModel(t *testing.T) {
	if _, err := NewOllamaProvider(OllamaConfig{}); err == nil {
		t.Errorf("no model: no error")
	}
	p, err := NewOllamaProvider(OllamaConfig{Model: "m"})
	if err != nil || p.cfg.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("default base %v %v", p, err)
	}
}

// plainProvider implements Provider and nothing else.
type plainProvider struct{ *StubProvider }

func TestCapabilitiesOfEachProvider(t *testing.T) {
	llama, _ := NewLlamaCppProvider(LlamaCppConfig{})
	ollama, _ := NewOllamaProvider(OllamaConfig{Model: "m"})
	vllm := openaiFor(t, OpenAIConfig{GuidedDialect: GuidedDialectVLLM})
	anth, _ := NewAnthropicProvider(AnthropicConfig{Model: "m", APIKey: "k"})
	plain := plainProvider{NewStubProvider()}
	fo := func(a, b Provider) Provider {
		f, err := NewFailoverProvider(FailoverConfig{Primary: a, Backup: b})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	for _, c := range []struct {
		name   string
		p      Provider
		guided []string
		seed   bool
	}{
		{"llama.cpp", llama, []string{GuidedGrammar, GuidedJSONSchema}, true},
		{"vllm", vllm, []string{GuidedGrammar, GuidedRegex, GuidedJSONSchema}, true},
		{"ollama", ollama, nil, true},
		{"anthropic", anth, nil, false},
		{"thinking over llama.cpp", NewThinkingProvider(llama, "<think>", "</think>"), []string{GuidedGrammar, GuidedJSONSchema}, true},
		{"failover llama.cpp then vllm", fo(llama, vllm), []string{GuidedGrammar, GuidedJSONSchema}, true},
		{"failover vllm then anthropic", fo(vllm, anth), nil, false},
		{"failover over a provider that can't say", fo(llama, plain), nil, false},
		{"control: a provider that can't say", plain, nil, false},
	} {
		got := CapabilitiesOf(c.p)
		if !slices.Equal(got.Guided, c.guided) || got.Seed != c.seed {
			t.Errorf("%s: %+v, want guided %v seed %v", c.name, got, c.guided, c.seed)
		}
	}
}

func TestCanGuide(t *testing.T) {
	llama, _ := NewLlamaCppProvider(LlamaCppConfig{})
	c := CapabilitiesOf(llama)
	for _, tc := range []struct {
		name string
		g    *GuidedDecoding
		want bool
	}{
		{"no constraint", nil, true},
		{"grammar", &GuidedDecoding{Grammar: "g"}, true},
		{"json schema", &GuidedDecoding{JSONSchema: core.NewRawJSON([]byte(`{}`))}, true},
		{"regex", &GuidedDecoding{Regex: "r"}, false},
		{"none set", &GuidedDecoding{}, false},
		{"two set", &GuidedDecoding{Grammar: "g", Regex: "r"}, false},
	} {
		if got := c.CanGuide(tc.g); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	if (Capabilities{}).CanGuide(&GuidedDecoding{Grammar: "g"}) {
		t.Errorf("control: nothing reported, yet a grammar fits")
	}
}
