package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func allPenalties() *SamplingPenalties {
	return &SamplingPenalties{
		Repeat:           floatPtr(1.1),
		RepeatLastN:      intPtr(64),
		Frequency:        floatPtr(0.2),
		Presence:         floatPtr(0.3),
		DRYMultiplier:    floatPtr(0.8),
		DRYBase:          floatPtr(1.75),
		DRYAllowedLength: intPtr(2),
	}
}

// Imagine jaala's translator on a llama.cpp role at temperature 0, looping
// under its grammar (#276). Every penalty and the DRY sampler reach
// llama-server under its own field names.
func TestLlamaCppSendsEveryPenalty(t *testing.T) {
	ts, body, _ := recordingServer(t)
	p, err := NewLlamaCppProvider(LlamaCppConfig{BaseURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req := ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "q"}}, Penalties: allPenalties()}
	want := map[string]string{
		"repeat_penalty":     "1.1",
		"repeat_last_n":      "64",
		"frequency_penalty":  "0.2",
		"presence_penalty":   "0.3",
		"dry_multiplier":     "0.8",
		"dry_base":           "1.75",
		"dry_allowed_length": "2",
	}
	for _, how := range []string{"Generate", "Stream"} {
		if how == "Generate" {
			_, err = p.Generate(ctx, req)
		} else {
			var s Stream
			if s, err = p.Stream(ctx, req); err == nil {
				s.Close()
			}
		}
		if err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		var sent map[string]json.RawMessage
		json.Unmarshal(*body, &sent)
		for k, v := range want {
			if string(sent[k]) != v {
				t.Errorf("%s: %s = %s, want %s", how, k, sent[k], v)
			}
		}
	}
	if _, err := p.Generate(ctx, ProviderRequest{Messages: req.Messages}); err != nil {
		t.Fatal(err)
	}
	var plain map[string]json.RawMessage
	json.Unmarshal(*body, &plain)
	for k := range want {
		if plain[k] != nil {
			t.Errorf("control: a request with no penalties sent %s", k)
		}
	}
}

// Only the fields that are set go out. A RepeatLastN without Repeat still
// counts as the repeat penalty, and an explicit 0 is a value.
func TestPenaltiesSendOnlyWhatIsSet(t *testing.T) {
	p, _ := NewLlamaCppProvider(LlamaCppConfig{})
	body := p.buildBody(ProviderRequest{Penalties: &SamplingPenalties{RepeatLastN: intPtr(0), DRYBase: floatPtr(2)}}, false)
	if body["repeat_last_n"] != 0 || body["dry_base"] != 2.0 {
		t.Errorf("sent %v", body)
	}
	for _, k := range []string{"repeat_penalty", "frequency_penalty", "presence_penalty", "dry_multiplier", "dry_allowed_length"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s sent though unset", k)
		}
	}
}

// Ollama's /v1 and OpenAI itself take frequency and presence only. A repeat
// penalty or DRY is refused before the request leaves, rather than dropped.
func TestOpenAIStandardPenaltiesAndRefusals(t *testing.T) {
	ts, body, hits := recordingServer(t)
	ollama, err := NewOllamaProvider(OllamaConfig{BaseURL: ts.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	openai := openaiFor(t, OpenAIConfig{BaseURL: ts.URL})
	ctx := context.Background()
	msgs := []Message{{Role: RoleUser, Text: "q"}}
	for name, p := range map[string]*OpenAIProvider{"ollama": ollama, "openai": openai} {
		before := hits.Load()
		for kind, pen := range map[string]*SamplingPenalties{
			"repeat":        {Repeat: floatPtr(1.1)},
			"repeat_last_n": {RepeatLastN: intPtr(64)},
			"dry":           {DRYMultiplier: floatPtr(0.8)},
			"dry_base":      {DRYBase: floatPtr(1.75)},
		} {
			req := ProviderRequest{Messages: msgs, Penalties: pen}
			_, gerr := p.Generate(ctx, req)
			_, serr := p.Stream(ctx, req)
			if !errors.Is(gerr, ErrUnsupportedParam) || !errors.Is(serr, ErrUnsupportedParam) {
				t.Errorf("%s %s: %v / %v, want ErrUnsupportedParam", name, kind, gerr, serr)
			}
		}
		if hits.Load() != before {
			t.Fatalf("%s: refused requests reached the server", name)
		}
		_, err := p.Generate(ctx, ProviderRequest{Messages: msgs, Penalties: &SamplingPenalties{Frequency: floatPtr(0.5), Presence: floatPtr(0)}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var sent map[string]json.RawMessage
		json.Unmarshal(*body, &sent)
		if string(sent["frequency_penalty"]) != "0.5" || string(sent["presence_penalty"]) != "0" {
			t.Errorf("%s sent frequency %s, presence %s", name, sent["frequency_penalty"], sent["presence_penalty"])
		}
	}
}

func TestAnthropicAndResponsesRefusePenalties(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer ts.Close()
	anth, _ := NewAnthropicProvider(AnthropicConfig{BaseURL: ts.URL, Model: "m"})
	resp, _ := NewResponsesProvider(ResponsesConfig{BaseURL: ts.URL, Model: "m"})
	req := ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "q"}}, Penalties: &SamplingPenalties{Frequency: floatPtr(0.1)}}
	for name, p := range map[string]Provider{"anthropic": anth, "responses": resp} {
		_, gerr := p.Generate(context.Background(), req)
		_, serr := p.Stream(context.Background(), req)
		if !errors.Is(gerr, ErrUnsupportedParam) || !errors.Is(serr, ErrUnsupportedParam) {
			t.Errorf("%s: %v / %v, want ErrUnsupportedParam", name, gerr, serr)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("refused requests reached the server %d times", hits.Load())
	}
	if _, err := anth.Generate(context.Background(), ProviderRequest{Messages: req.Messages, Penalties: &SamplingPenalties{}}); errors.Is(err, ErrUnsupportedParam) {
		t.Errorf("control: an empty SamplingPenalties asks for nothing, got %v", err)
	}
}

func TestPenaltyCapabilities(t *testing.T) {
	llama, _ := NewLlamaCppProvider(LlamaCppConfig{})
	ollama, _ := NewOllamaProvider(OllamaConfig{Model: "m"})
	openai := openaiFor(t, OpenAIConfig{})
	anth, _ := NewAnthropicProvider(AnthropicConfig{Model: "m", APIKey: "k"})
	resp, _ := NewResponsesProvider(ResponsesConfig{BaseURL: "http://unused", Model: "m"})
	fo, err := NewFailoverProvider(FailoverConfig{Primary: llama, Backup: ollama})
	if err != nil {
		t.Fatal(err)
	}
	all := []string{PenaltyRepeat, PenaltyFrequency, PenaltyPresence, PenaltyDRY}
	std := []string{PenaltyFrequency, PenaltyPresence}
	for _, c := range []struct {
		name string
		p    Provider
		want []string
	}{
		{"llama.cpp", llama, all},
		{"ollama", ollama, std},
		{"openai", openai, std},
		{"anthropic", anth, nil},
		{"responses", resp, nil},
		{"failover llama.cpp then ollama", fo, std},
		{"thinking over llama.cpp", NewThinkingProvider(llama, "<think>", "</think>"), all},
	} {
		if got := CapabilitiesOf(c.p).Penalties; !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	c := CapabilitiesOf(ollama)
	if !c.CanPenalize(nil) || !c.CanPenalize(&SamplingPenalties{Presence: floatPtr(1)}) || c.CanPenalize(&SamplingPenalties{DRYBase: floatPtr(1)}) {
		t.Errorf("CanPenalize on ollama: %+v", c)
	}
}

// A turn's penalties merge field by field over the config's, and reach both
// the step and the structured finalize.
func TestGenerationPenaltiesMergeAndReachEveryCall(t *testing.T) {
	base := GenerationParams{Penalties: &SamplingPenalties{Repeat: floatPtr(1.1), Frequency: floatPtr(0.2)}}
	got := base.merge(GenerationParams{Penalties: &SamplingPenalties{Repeat: floatPtr(1.3), DRYMultiplier: floatPtr(0.8)}})
	if *got.Penalties.Repeat != 1.3 || *got.Penalties.Frequency != 0.2 || *got.Penalties.DRYMultiplier != 0.8 {
		t.Errorf("merge = %+v", *got.Penalties)
	}
	if *base.Penalties.Repeat != 1.1 || base.Penalties.DRYMultiplier != nil {
		t.Errorf("merge mutated the receiver: %+v", *base.Penalties)
	}
	if got := base.merge(GenerationParams{}); got.Penalties == nil || *got.Penalties.Repeat != 1.1 {
		t.Errorf("zero override: %+v", got.Penalties)
	}
	if got := (GenerationParams{}).merge(base); got.Penalties == nil || *got.Penalties.Frequency != 0.2 {
		t.Errorf("override onto an empty config: %+v", got.Penalties)
	}

	p := NewStubProvider(StubTurn{Text: "done"}, StubTurn{Text: `{"ok":true}`})
	r, err := NewRunner(RunnerConfig{
		Provider:       p,
		ResponseSchema: core.NewRawJSON([]byte(`{"type":"object"}`)),
		Generation:     base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunTurn(context.Background(), TurnRequest{
		History:    []Message{{Role: RoleUser, Text: "hi"}},
		Generation: GenerationParams{Penalties: &SamplingPenalties{Presence: floatPtr(0.4)}},
	}); err != nil {
		t.Fatal(err)
	}
	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests (step + finalize), got %d", len(reqs))
	}
	for i, req := range reqs {
		if req.Penalties == nil || *req.Penalties.Repeat != 1.1 || *req.Penalties.Presence != 0.4 {
			t.Errorf("request %d Penalties = %+v", i, req.Penalties)
		}
	}
}

// Sendable keeps the kinds a provider takes, as whole kinds, and the result
// is always something the provider takes.
func TestSendableKeepsTheKindsAProviderTakes(t *testing.T) {
	llama, _ := NewLlamaCppProvider(LlamaCppConfig{})
	ollama, _ := NewOllamaProvider(OllamaConfig{Model: "m"})
	anth, _ := NewAnthropicProvider(AnthropicConfig{APIKey: "k", Model: "m"})
	every := &SamplingPenalties{
		Repeat: floatPtr(1.1), RepeatLastN: intPtr(64),
		Frequency: floatPtr(0.2), Presence: floatPtr(0.3),
		DRYMultiplier: floatPtr(0.8), DRYBase: floatPtr(1.75), DRYAllowedLength: intPtr(2),
	}
	before := *every
	for _, c := range []struct {
		name string
		p    Provider
		in   *SamplingPenalties
		want *SamplingPenalties
	}{
		{"llama.cpp keeps all", llama, every, every},
		{"ollama keeps frequency and presence", ollama, every, &SamplingPenalties{Frequency: floatPtr(0.2), Presence: floatPtr(0.3)}},
		{"ollama drops RepeatLastN with Repeat", ollama, &SamplingPenalties{RepeatLastN: intPtr(64), Frequency: floatPtr(0.2)}, &SamplingPenalties{Frequency: floatPtr(0.2)}},
		{"ollama with only DRY left is nil", ollama, &SamplingPenalties{DRYBase: floatPtr(1.75)}, nil},
		{"anthropic takes none", anth, every, nil},
		{"nil stays nil", llama, nil, nil},
	} {
		caps := CapabilitiesOf(c.p)
		got := caps.Sendable(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %s, want %s", c.name, penaltiesJSON(got), penaltiesJSON(c.want))
		}
		if !caps.CanPenalize(got) {
			t.Errorf("%s: the provider refuses what Sendable kept", c.name)
		}
	}
	if !reflect.DeepEqual(*every, before) {
		t.Errorf("Sendable changed its input")
	}
}

// Every field of SamplingPenalties belongs to a kind Sendable can drop. A new
// field it doesn't know would survive a provider that takes no penalties.
func TestSendableClearsEveryField(t *testing.T) {
	var s SamplingPenalties
	v := reflect.ValueOf(&s).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		f.Set(reflect.New(f.Type().Elem()))
	}
	if got := (Capabilities{}).Sendable(&s); got != nil {
		t.Errorf("Sendable with no kinds kept %s", penaltiesJSON(got))
	}
}

func penaltiesJSON(s *SamplingPenalties) string {
	b, _ := json.Marshal(s)
	return string(b)
}
