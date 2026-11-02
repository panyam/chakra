package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func intPtr(i int) *int { return &i }

func openaiFor(t *testing.T, cfg OpenAIConfig) *OpenAIProvider {
	t.Helper()
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://x"
	}
	if cfg.Model == "" {
		cfg.Model = "m"
	}
	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func wireJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOpenAIZeroParamsLeaveBodyUnchanged(t *testing.T) {
	p := openaiFor(t, OpenAIConfig{GuidedDialect: GuidedDialectVLLM})
	body := p.buildBody(ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, false)
	if len(body) != 2 || body["model"] == nil || body["messages"] == nil {
		t.Fatalf("zero-value request body keys = %v, want only model and messages", keysOf(body))
	}
}

func TestOpenAISeedOnTheWire(t *testing.T) {
	p := openaiFor(t, OpenAIConfig{})
	body := p.buildBody(ProviderRequest{Seed: intPtr(0)}, false)
	if got, ok := body["seed"]; !ok || got != 0 {
		t.Fatalf("seed = %v (present %v), want an explicit 0", got, ok)
	}
}

func TestOpenAIGuidedDialects(t *testing.T) {
	schema := core.NewRawJSON([]byte(`{"type":"object"}`))
	cases := []struct {
		name    string
		dialect GuidedDialect
		guided  GuidedDecoding
		key     string
		want    string
	}{
		{"llamacpp grammar", GuidedDialectLlamaCpp, GuidedDecoding{Grammar: `root ::= "a"`}, "grammar", `"root ::= \"a\""`},
		{"llamacpp schema", GuidedDialectLlamaCpp, GuidedDecoding{JSONSchema: schema}, "json_schema", `{"type":"object"}`},
		{"vllm grammar", GuidedDialectVLLM, GuidedDecoding{Grammar: `root ::= "a"`}, "structured_outputs", `{"grammar":"root ::= \"a\""}`},
		{"vllm regex", GuidedDialectVLLM, GuidedDecoding{Regex: `[0-9]+`}, "structured_outputs", `{"regex":"[0-9]+"}`},
		{"vllm schema", GuidedDialectVLLM, GuidedDecoding{JSONSchema: schema}, "structured_outputs", `{"json":{"type":"object"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := openaiFor(t, OpenAIConfig{GuidedDialect: tc.dialect})
			g := tc.guided
			req := ProviderRequest{Guided: &g}
			if err := p.check(req); err != nil {
				t.Fatal(err)
			}
			body := p.buildBody(req, false)
			if got := wireJSON(t, body[tc.key]); got != tc.want {
				t.Fatalf("%s = %s, want %s", tc.key, got, tc.want)
			}
		})
	}
}

func TestOpenAIGuidedRefusals(t *testing.T) {
	cases := []struct {
		name    string
		dialect GuidedDialect
		guided  GuidedDecoding
	}{
		{"no dialect configured", "", GuidedDecoding{Grammar: `root ::= "a"`}},
		{"llamacpp has no regex", GuidedDialectLlamaCpp, GuidedDecoding{Regex: `[0-9]+`}},
		{"empty constraint", GuidedDialectVLLM, GuidedDecoding{}},
		{"two constraints", GuidedDialectVLLM, GuidedDecoding{Grammar: "g", Regex: "r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer ts.Close()
			p := openaiFor(t, OpenAIConfig{BaseURL: ts.URL, GuidedDialect: tc.dialect})
			g := tc.guided
			_, gerr := p.Generate(context.Background(), ProviderRequest{Guided: &g})
			_, serr := p.Stream(context.Background(), ProviderRequest{Guided: &g})
			for _, err := range []error{gerr, serr} {
				if !errors.Is(err, ErrUnsupportedParam) {
					t.Errorf("err = %v, want ErrUnsupportedParam", err)
				}
			}
			if hits.Load() != 0 {
				t.Errorf("refused request reached the server %d times", hits.Load())
			}
		})
	}
}

func TestOpenAIUnknownDialectRejectedAtConstruction(t *testing.T) {
	if _, err := NewOpenAIProvider(OpenAIConfig{BaseURL: "http://x", Model: "m", GuidedDialect: "sglang"}); err == nil {
		t.Fatal("unknown GuidedDialect must fail construction")
	}
}

func TestOpenAIExtraBody(t *testing.T) {
	extra := map[string]any{"cache_prompt": true, "model": "hijack", "messages": "hijack", "temperature": 0.9}
	p := openaiFor(t, OpenAIConfig{ExtraBody: extra})
	extra["late"] = true

	body := p.buildBody(ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "hi"}}, Temperature: floatPtr(0.1)}, false)
	if body["cache_prompt"] != true {
		t.Errorf("cache_prompt = %v, want true", body["cache_prompt"])
	}
	if body["model"] != "m" {
		t.Errorf("model = %v: ExtraBody must not override a field the provider sets", body["model"])
	}
	if _, ok := body["messages"].([]map[string]any); !ok {
		t.Errorf("messages = %v: ExtraBody must not override a field the provider sets", body["messages"])
	}
	if body["temperature"] != 0.1 {
		t.Errorf("temperature = %v, want the request's 0.1", body["temperature"])
	}
	if _, ok := body["late"]; ok {
		t.Error("mutating the caller's map after construction changed the request")
	}

	unset := p.buildBody(ProviderRequest{}, false)
	if unset["temperature"] != 0.9 {
		t.Errorf("temperature = %v: ExtraBody should fill a field the request leaves unset", unset["temperature"])
	}
}

// TestOpenAILlamaCppRequestGolden reproduces what jaala's nlmodels/llamacpp
// client sends, so jaala's translator can move onto OpenAIProvider.
func TestOpenAILlamaCppRequestGolden(t *testing.T) {
	var captured json.RawMessage
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer ts.Close()
	p := openaiFor(t, OpenAIConfig{
		BaseURL:       ts.URL,
		Model:         "qwen",
		GuidedDialect: GuidedDialectLlamaCpp,
		ExtraBody:     map[string]any{"cache_prompt": true},
	})
	_, err := p.Generate(context.Background(), ProviderRequest{
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
	if got, exp := canonJSON(t, captured), canonJSON(t, want); got != exp {
		t.Fatalf("request body\n got %s\nwant %s", got, exp)
	}
}

func TestAnthropicRefusesSeedAndGuided(t *testing.T) {
	cases := map[string]ProviderRequest{
		"seed":   {Seed: intPtr(1)},
		"guided": {Guided: &GuidedDecoding{Grammar: "g"}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer ts.Close()
			p, err := NewAnthropicProvider(AnthropicConfig{BaseURL: ts.URL, Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			_, gerr := p.Generate(context.Background(), req)
			_, serr := p.Stream(context.Background(), req)
			for _, err := range []error{gerr, serr} {
				if !errors.Is(err, ErrUnsupportedParam) {
					t.Errorf("err = %v, want ErrUnsupportedParam", err)
				}
			}
			if hits.Load() != 0 {
				t.Errorf("refused request reached the server %d times", hits.Load())
			}
		})
	}
}

func TestGenerationSeedReachesStepAndFinalize(t *testing.T) {
	p := NewStubProvider(StubTurn{Text: "done"}, StubTurn{Text: `{"ok":true}`})
	r, err := NewRunner(RunnerConfig{
		Provider:       p,
		ResponseSchema: core.NewRawJSON([]byte(`{"type":"object"}`)),
		Generation:     GenerationParams{Seed: intPtr(42)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests (step + finalize), got %d", len(reqs))
	}
	for i, req := range reqs {
		if req.Seed == nil || *req.Seed != 42 {
			t.Errorf("request %d Seed = %v, want 42", i, req.Seed)
		}
	}
}

func TestGenerationSeedMerge(t *testing.T) {
	base := GenerationParams{Seed: intPtr(1)}
	if got := base.merge(GenerationParams{}); got.Seed == nil || *got.Seed != 1 {
		t.Errorf("zero override: Seed = %v, want inherited 1", got.Seed)
	}
	if got := base.merge(GenerationParams{Seed: intPtr(0)}); got.Seed == nil || *got.Seed != 0 {
		t.Errorf("explicit 0 override: Seed = %v, want 0", got.Seed)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func canonJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("invalid JSON %s: %v", raw, err)
	}
	return wireJSON(t, v)
}
