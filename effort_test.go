package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func effortRequest(e string) ProviderRequest {
	return ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "q"}}, Effort: e}
}

// A Gemini connection's default effort goes in ExtraBody. A request that
// sets Effort replaces it for that call, on Generate and Stream alike, and a
// request that sets none keeps the default (#375).
func TestOpenAISendsTheRequestsEffort(t *testing.T) {
	ts, body, _ := recordingServer(t)
	p, err := NewOpenAIProvider(OpenAIConfig{BaseURL: ts.URL, Model: "m", Efforts: []string{EffortNone, EffortHigh}, ExtraBody: map[string]any{"reasoning_effort": "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := CapabilitiesOf(p).Effort; !slices.Equal(got, []string{EffortNone, EffortHigh}) {
		t.Errorf("Capabilities.Effort = %v", got)
	}
	ctx := context.Background()
	for _, c := range []struct{ effort, want string }{{EffortHigh, `"high"`}, {EffortNone, `"none"`}, {"", `"medium"`}} {
		for _, how := range []string{"Generate", "Stream"} {
			if how == "Generate" {
				_, err = p.Generate(ctx, effortRequest(c.effort))
			} else {
				var s Stream
				if s, err = p.Stream(ctx, effortRequest(c.effort)); err == nil {
					s.Close()
				}
			}
			if err != nil {
				t.Fatalf("%s %q: %v", how, c.effort, err)
			}
			var sent map[string]json.RawMessage
			json.Unmarshal(*body, &sent)
			if got := string(sent["reasoning_effort"]); got != c.want {
				t.Errorf("%s with Effort %q: reasoning_effort = %s, want %s", how, c.effort, got, c.want)
			}
		}
	}
}

// An effort the provider can't send is refused before anything leaves. A
// llama.cpp preset and the SDK-free Anthropic provider send none, and a
// level outside a provider's list is refused too.
func TestAnEffortTheProviderCantSendIsRefused(t *testing.T) {
	ts, _, hits := recordingServer(t)
	llama, err := NewLlamaCppProvider(LlamaCppConfig{BaseURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	gemini, err := NewOpenAIProvider(OpenAIConfig{BaseURL: ts.URL, Model: "m", Efforts: []string{EffortLow, EffortHigh}})
	if err != nil {
		t.Fatal(err)
	}
	ant, err := NewAnthropicProvider(AnthropicConfig{BaseURL: ts.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	rs := newTestResponsesProvider(t, ts.URL, ResponsesConfig{})
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		p      Provider
		effort string
	}{{"llamacpp", llama, EffortHigh}, {"openai", gemini, "xhigh"}, {"anthropic", ant, EffortLow}, {"responses", rs, "max"}} {
		_, gerr := c.p.Generate(ctx, effortRequest(c.effort))
		_, serr := c.p.Stream(ctx, effortRequest(c.effort))
		if !errors.Is(gerr, ErrUnsupportedParam) || !errors.Is(serr, ErrUnsupportedParam) {
			t.Errorf("%s with Effort %q: %v / %v, want ErrUnsupportedParam", c.name, c.effort, gerr, serr)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests reached the server, want 0", n)
	}
}

// The Responses API sends a request's Effort as reasoning.effort, in place
// of ResponsesConfig.ReasoningEffort.
func TestResponsesSendsTheRequestsEffort(t *testing.T) {
	f := &rsFake{reply: func(n int, _ map[string]any) (int, string) { return 0, rsReplyText(n, "ok") }}
	p := newTestResponsesProvider(t, f.serve(t).URL, ResponsesConfig{ReasoningEffort: EffortLow})
	ctx := context.Background()
	for i, e := range []string{EffortHigh, ""} {
		s, err := p.Stream(ctx, effortRequest(e))
		if err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := s.Recv(); err != nil {
				break
			}
		}
		s.Close()
		r, _ := f.request(i)["reasoning"].(map[string]any)
		want := map[string]string{EffortHigh: EffortHigh, "": EffortLow}[e]
		if r["effort"] != want {
			t.Errorf("Effort %q: reasoning.effort = %v, want %s", e, r["effort"], want)
		}
	}
}

// A request stage picks the effort for each call. Here it asks for low on
// the first call, and the turn's default applies where it says nothing.
func TestAStepStageSetsEachCallsEffort(t *testing.T) {
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "no_such_tool"}}},
		StubTurn{Text: "done"},
	)
	stage := StepStage{Name: "effort", Scope: ScopeRequest, Run: func(_ context.Context, info StepInfo, v StepView) (StepView, error) {
		if info.Step == 1 {
			v.Generation.Effort = EffortLow
		}
		return v, nil
	}}
	r, err := NewRunner(RunnerConfig{Provider: stub, MaxSteps: 4, StepStages: []StepStage{stage}, Generation: GenerationParams{Effort: EffortHigh}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "q"}}, nil); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, req := range stub.Requests() {
		got = append(got, req.Effort)
	}
	if !slices.Equal(got, []string{EffortLow, EffortHigh}) {
		t.Errorf("efforts asked for = %v, want [low high]", got)
	}
}

// A failover pair reports the effort levels both sides send, since either
// may serve a call.
func TestAFailoverPairReportsTheEffortsBothSend(t *testing.T) {
	a, err := NewOpenAIProvider(OpenAIConfig{BaseURL: "http://127.0.0.1:1", Model: "m", Efforts: []string{EffortNone, EffortLow, EffortHigh}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewOpenAIProvider(OpenAIConfig{BaseURL: "http://127.0.0.1:1", Model: "m", Efforts: []string{EffortLow, EffortHigh, "xhigh"}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFailoverProvider(FailoverConfig{Primary: a, Backup: b})
	if err != nil {
		t.Fatal(err)
	}
	if got := CapabilitiesOf(f).Effort; !slices.Equal(got, []string{EffortLow, EffortHigh}) {
		t.Errorf("failover Capabilities.Effort = %v, want [low high]", got)
	}
}
