package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// maxTokensSent is the max_tokens a provider sent for req, or -1 for none.
func maxTokensSent(t *testing.T, build func(url string) (Provider, error), req ProviderRequest) int {
	t.Helper()
	ts, body, _ := recordingServer(t)
	p, err := build(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		MaxTokens *int `json:"max_tokens"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.MaxTokens == nil {
		return -1
	}
	return *sent.MaxTokens
}

// The local presets cap a reply at DefaultLocalMaxTokens when nothing sets a
// cap, since llama-server and Ollama otherwise generate until the context is
// full. The config's cap replaces the default, a request's replaces both,
// and a negative config sends none. control: the plain OpenAI provider
// sends no cap of its own.
func TestLocalPresetsCapEachReply(t *testing.T) {
	presets := map[string]func(n int) func(url string) (Provider, error){
		"llamacpp": func(n int) func(string) (Provider, error) {
			return func(url string) (Provider, error) {
				return NewLlamaCppProvider(LlamaCppConfig{BaseURL: url, MaxTokens: n})
			}
		},
		"ollama": func(n int) func(string) (Provider, error) {
			return func(url string) (Provider, error) {
				return NewOllamaProvider(OllamaConfig{BaseURL: url, Model: "qwen2.5-coder:14b", MaxTokens: n})
			}
		},
	}
	q := ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "q"}}}
	withCap := q
	withCap.MaxTokens = 512
	for name, preset := range presets {
		for _, tc := range []struct {
			config int
			req    ProviderRequest
			want   int
		}{
			{0, q, DefaultLocalMaxTokens},
			{9000, q, 9000},
			{9000, withCap, 512},
			{0, withCap, 512},
			{-1, q, -1},
			{-1, withCap, 512},
		} {
			if got := maxTokensSent(t, preset(tc.config), tc.req); got != tc.want {
				t.Errorf("%s with MaxTokens %d, request cap %d: sent %d, want %d", name, tc.config, tc.req.MaxTokens, got, tc.want)
			}
		}
	}
	if DefaultLocalMaxTokens != 4096 {
		t.Errorf("DefaultLocalMaxTokens = %d", DefaultLocalMaxTokens)
	}
	plain := func(url string) (Provider, error) { return NewOpenAIProvider(OpenAIConfig{BaseURL: url, Model: "m"}) }
	if got := maxTokensSent(t, plain, q); got != -1 {
		t.Errorf("control: the OpenAI provider sent max_tokens %d", got)
	}
	set := func(url string) (Provider, error) {
		return NewOpenAIProvider(OpenAIConfig{BaseURL: url, Model: "m", MaxTokens: 700})
	}
	if got := fmt.Sprint(maxTokensSent(t, set, q), maxTokensSent(t, set, withCap)); got != "700 512" {
		t.Errorf("OpenAIConfig.MaxTokens: sent %s, want 700 then the request's 512", got)
	}
}
