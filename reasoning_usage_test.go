package agent

import (
	"context"
	"reflect"
	"testing"
)

func TestOpenAIGenerateReportsReasoningTokens(t *testing.T) {
	ts := jsonServer(t, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":40,"completion_tokens_details":{"reasoning_tokens":32}}}`)
	defer ts.Close()
	res, err := newTestProvider(t, ts.URL).Generate(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{InputTokens: 100, OutputTokens: 40, ReasoningTokens: 32}
	if res.Usage == nil || *res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
}

// llama.cpp streams reasoning as reasoning_content, one token per chunk, and
// sends no reasoning count. A server that does send one is believed over the
// chunk count.
func TestOpenAIStreamCountsReasoningChunks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage string
		want  int
	}{
		{"no count sent", `{"prompt_tokens":100,"completion_tokens":5}`, 3},
		{"count sent", `{"prompt_tokens":100,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}`, 2},
		{"never above the output", `{"prompt_tokens":100,"completion_tokens":2}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := sseServer(t, nil,
				`{"choices":[{"delta":{"reasoning_content":"Let"}}]}`,
				`{"choices":[{"delta":{"reasoning_content":" me"}}]}`,
				`{"choices":[{"delta":{"reasoning_content":" see"}}]}`,
				`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":`+tc.usage+`}`,
				`[DONE]`,
			)
			defer ts.Close()
			s, err := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			res := drain(t, s)
			if res.Usage == nil || res.Usage.ReasoningTokens != tc.want {
				t.Fatalf("usage = %+v, want %d reasoning tokens", res.Usage, tc.want)
			}
		})
	}
}

func TestOpenAIStreamWithoutReasoningReportsNone(t *testing.T) {
	ts := sseServer(t, nil,
		`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5}}`,
		`[DONE]`,
	)
	defer ts.Close()
	s, err := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if res := drain(t, s); res.Usage == nil || res.Usage.ReasoningTokens != 0 {
		t.Fatalf("usage = %+v, want no reasoning tokens", res.Usage)
	}
}

func TestResponsesStreamReportsReasoningTokens(t *testing.T) {
	ts := jsonServer(t, rsData(
		`{"type":"response.output_text.delta","output_index":0,"delta":"ok"}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":100,"output_tokens":40,"output_tokens_details":{"reasoning_tokens":32}}}}`,
	))
	defer ts.Close()
	res := drainGenerate(t, newTestResponsesProvider(t, ts.URL, ResponsesConfig{Model: "m"}), []Message{{Role: RoleUser, Text: "hi"}})
	want := Usage{InputTokens: 100, OutputTokens: 40, ReasoningTokens: 32}
	if res.Usage == nil || *res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
}

func TestRunnerSumsReasoningTokens(t *testing.T) {
	turn := func(text string) StubTurn {
		return StubTurn{Deltas: []Delta{
			{Kind: DeltaText, Text: text},
			{Kind: DeltaFinish, FinishReason: "stop"},
			{Kind: DeltaUsage, Usage: &Usage{InputTokens: 100, OutputTokens: 50, ReasoningTokens: 30}},
		}}
	}
	r, err := NewRunner(RunnerConfig{Provider: NewStubProvider(turn("done"))})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.ReasoningTokens != 30 {
		t.Fatalf("turn usage = %+v, want 30 reasoning tokens", res.Usage)
	}
}

// Add and Sub must cover every count, or a total drops the new one silently,
// as eval's sum once dropped CachedInputTokens.
func TestUsageAddAndSubCoverEveryCount(t *testing.T) {
	var a, b Usage
	av, bv := reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem()
	for i := range av.NumField() {
		av.Field(i).SetInt(int64(10 * (i + 1)))
		bv.Field(i).SetInt(int64(i + 1))
	}
	sum, diff := reflect.ValueOf(a.Add(b)), reflect.ValueOf(a.Sub(b))
	for i := range av.NumField() {
		name := av.Type().Field(i).Name
		if got, want := sum.Field(i).Int(), int64(11*(i+1)); got != want {
			t.Errorf("Add: %s = %d, want %d", name, got, want)
		}
		if got, want := diff.Field(i).Int(), int64(9*(i+1)); got != want {
			t.Errorf("Sub: %s = %d, want %d", name, got, want)
		}
	}
}
