package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func jsonServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
}

// The three shapes an OpenAI-wire server reports its prompt cache in: OpenAI
// and current llama.cpp put it in prompt_tokens_details, older llama.cpp only
// in timings.cache_n, and plenty of servers say nothing.
var openaiCacheCases = []struct {
	name   string
	extra  string // fields after "usage":{...}, placed in the response or the final chunk
	usage  string
	cached int
}{
	{"prompt_tokens_details", ``, `{"prompt_tokens":100,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":99}}`, 99},
	{"timings.cache_n fallback", `,"timings":{"cache_n":64,"prompt_n":36}`, `{"prompt_tokens":100,"completion_tokens":8}`, 64},
	{"details win over timings", `,"timings":{"cache_n":50}`, `{"prompt_tokens":100,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":99}}`, 99},
	{"nothing reported", ``, `{"prompt_tokens":100,"completion_tokens":8}`, 0},
}

func TestOpenAIGenerateReportsCachedTokens(t *testing.T) {
	for _, tc := range openaiCacheCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := jsonServer(t, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":`+tc.usage+tc.extra+`}`)
			defer ts.Close()
			res, err := newTestProvider(t, ts.URL).Generate(context.Background(), ProviderRequest{})
			if err != nil {
				t.Fatal(err)
			}
			want := Usage{InputTokens: 100, OutputTokens: 8, CachedInputTokens: tc.cached}
			if res.Usage == nil || *res.Usage != want {
				t.Fatalf("usage = %+v, want %+v", res.Usage, want)
			}
		})
	}
}

func TestOpenAIStreamReportsCachedTokens(t *testing.T) {
	for _, tc := range openaiCacheCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := sseServer(t, nil,
				`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":`+tc.usage+tc.extra+`}`,
				`[DONE]`,
			)
			defer ts.Close()
			s, err := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			res := drain(t, s)
			want := Usage{InputTokens: 100, OutputTokens: 8, CachedInputTokens: tc.cached}
			if res.Usage == nil || *res.Usage != want {
				t.Fatalf("usage = %+v, want %+v", res.Usage, want)
			}
		})
	}
}

// Anthropic's input_tokens leaves out cache reads and writes, where OpenAI's
// prompt_tokens includes them. Both providers report InputTokens as the whole
// prompt so CachedInputTokens is always a part of it.
func TestAnthropicGenerateNormalizesCachedTokens(t *testing.T) {
	ts := jsonServer(t, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":70,"output_tokens":4}}`)
	defer ts.Close()
	res, err := newTestAnthropic(t, ts.URL).Generate(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{InputTokens: 100, OutputTokens: 4, CachedInputTokens: 70}
	if res.Usage == nil || *res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
}

func TestAnthropicStreamNormalizesCachedTokens(t *testing.T) {
	ts := sseServer(t, nil,
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":70,"output_tokens":1}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	)
	defer ts.Close()
	s, err := newTestAnthropic(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res := drain(t, s)
	want := Usage{InputTokens: 100, OutputTokens: 9, CachedInputTokens: 70}
	if res.Usage == nil || *res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
}

func TestRunnerSumsCachedTokensAcrossStepAndFinalize(t *testing.T) {
	turn := func(text string) StubTurn {
		return StubTurn{Deltas: []Delta{
			{Kind: DeltaText, Text: text},
			{Kind: DeltaFinish, FinishReason: "stop"},
			{Kind: DeltaUsage, Usage: &Usage{InputTokens: 100, OutputTokens: 5, CachedInputTokens: 80}},
		}}
	}
	r, err := NewRunner(RunnerConfig{
		Provider:       NewStubProvider(turn("done"), turn(`{"ok":true}`)),
		ResponseSchema: core.NewRawJSON([]byte(`{"type":"object"}`)),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{InputTokens: 200, OutputTokens: 10, CachedInputTokens: 160}
	if res.Usage != want {
		t.Fatalf("turn usage = %+v, want %+v (one step plus the finalizing call)", res.Usage, want)
	}
}
