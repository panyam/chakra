package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// capture serves every request with reply and keeps the last body.
func capture(t *testing.T, contentType, reply string) (*httptest.Server, *map[string]any, *atomic.Int32) {
	t.Helper()
	body := map[string]any{}
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		clear(body)
		json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", contentType)
		io.WriteString(w, reply)
	}))
	t.Cleanup(ts.Close)
	return ts, &body, &hits
}

func client(url string) *sdk.Client {
	c := sdk.NewClient(option.WithBaseURL(url), option.WithAPIKey("k"), option.WithMaxRetries(0))
	return &c
}

// okMessage is the no-SDK provider's JSON reply, and okStream is the same
// reply streamed, which is how this provider's Generate asks for it.
const okMessage = `{"id":"m1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`

func streamed(text, usage string) string {
	quoted, _ := json.Marshal(text)
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"usage":` + usage + `}}` + "\n\n" +
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(quoted) + `}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
}

var okStream = streamed("ok", `{"input_tokens":3,"output_tokens":0}`)

func conversation() agent.ProviderRequest {
	return agent.ProviderRequest{
		Instructions: "card",
		Messages: []agent.Message{
			{Role: agent.RoleUser, Text: "fix the test"},
			{Role: agent.RoleAssistant, Text: "looking", ToolCalls: []agent.ToolCall{{ID: "t1", Name: "files.read", Args: core.NewRawJSON([]byte(`{"path":"a.go"}`))}}},
			{Role: agent.RoleTool, ToolCallID: "t1", Text: "package a"},
			{Role: agent.RoleSystem, Text: "Step 3 of 8."},
		},
		Tools: []core.ToolDef{{Name: "files.read", Description: "reads", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
	}
}

// Imagine an agent's eighth step. It resends the instructions, the tools and
// seven steps of history, and today every token is billed at full price.
// The request now asks the API to cache it, so the next step reads that
// prefix at a tenth of the price.
func TestRequestAsksForCaching(t *testing.T) {
	ts, body, _ := capture(t, "text/event-stream", okStream)
	p, err := New(client(ts.URL), Config{Model: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), conversation()); err != nil {
		t.Fatal(err)
	}
	cc, _ := (*body)["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" {
		t.Fatalf("cache_control = %v, want top-level ephemeral", (*body)["cache_control"])
	}
}

// The SDK provider sends the same conversation the no-SDK one does. A
// mapping that drifted would change what the model reads.
func TestSameConversationAsTheNoSDKProvider(t *testing.T) {
	ts, sdkBody, _ := capture(t, "text/event-stream", okStream)
	p, _ := New(client(ts.URL), Config{Model: "claude-sonnet-5-5"})
	if _, err := p.Generate(context.Background(), conversation()); err != nil {
		t.Fatal(err)
	}
	ref, refBody, _ := capture(t, "application/json", okMessage)
	q, _ := agent.NewAnthropicProvider(agent.AnthropicConfig{BaseURL: ref.URL, Model: "claude-sonnet-5-5"})
	if _, err := q.Generate(context.Background(), conversation()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"messages", "tools", "model"} {
		if !reflect.DeepEqual(normalize((*sdkBody)[k]), normalize((*refBody)[k])) {
			got, _ := json.Marshal((*sdkBody)[k])
			want, _ := json.Marshal((*refBody)[k])
			t.Errorf("%s differs\n sdk %s\n ref %s", k, got, want)
		}
	}
	sys, _ := (*sdkBody)["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["text"] != (*refBody)["system"] {
		t.Errorf("system = %v, want one text block %q", (*sdkBody)["system"], (*refBody)["system"])
	}
}

// normalize folds the one encoding the two providers write differently. The
// SDK sends a tool result's content as text blocks with "is_error": false,
// and the no-SDK provider sends a plain string with no is_error. The API
// reads both the same way.
func normalize(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = normalize(e)
		}
		if out["type"] == "tool_result" {
			if out["is_error"] == false {
				delete(out, "is_error")
			}
			if blocks, ok := out["content"].([]any); ok && len(blocks) == 1 {
				if b, ok := blocks[0].(map[string]any); ok && b["type"] == "text" {
					out["content"] = b["text"]
				}
			}
		}
		return out
	}
	return v
}

func TestMaxTokensAndEffort(t *testing.T) {
	ts, body, _ := capture(t, "text/event-stream", okStream)
	p, _ := New(client(ts.URL), Config{Model: "m", MaxTokens: 64000, Effort: "medium"})
	if _, err := p.Generate(context.Background(), agent.ProviderRequest{Messages: conversation().Messages[:1]}); err != nil {
		t.Fatal(err)
	}
	oc, _ := (*body)["output_config"].(map[string]any)
	if (*body)["max_tokens"] != 64000.0 || oc["effort"] != "medium" {
		t.Errorf("max_tokens %v, output_config %v", (*body)["max_tokens"], (*body)["output_config"])
	}
	if _, err := p.Generate(context.Background(), agent.ProviderRequest{Messages: conversation().Messages[:1], MaxTokens: 100}); err != nil {
		t.Fatal(err)
	}
	if (*body)["max_tokens"] != 100.0 {
		t.Errorf("a request's MaxTokens didn't win: %v", (*body)["max_tokens"])
	}
	p, _ = New(client(ts.URL), Config{Model: "m"})
	p.Generate(context.Background(), agent.ProviderRequest{Messages: conversation().Messages[:1]})
	if (*body)["max_tokens"] != float64(defaultMaxTokens) || (*body)["output_config"] != nil {
		t.Errorf("defaults: max_tokens %v, output_config %v", (*body)["max_tokens"], (*body)["output_config"])
	}
}

// Current models answer 400 to a forced tool_choice, so a ResponseSchema
// goes out as structured output rather than as a forced tool.
func TestStructuredOutputUsesOutputFormat(t *testing.T) {
	ts, body, _ := capture(t, "text/event-stream", streamed(`{"ok":true}`, `{"input_tokens":3,"output_tokens":0}`))
	p, _ := New(client(ts.URL), Config{Model: "m"})
	req := agent.ProviderRequest{Messages: conversation().Messages[:1], ResponseSchema: core.NewRawJSON([]byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`))}
	resp, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	oc, _ := (*body)["output_config"].(map[string]any)
	format, _ := oc["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil || (*body)["tool_choice"] != nil || resp.Text != `{"ok":true}` {
		t.Errorf("output_config %v, tool_choice %v, text %q", (*body)["output_config"], (*body)["tool_choice"], resp.Text)
	}
}

func TestRefusesWhatTheAPIRejects(t *testing.T) {
	ts, _, hits := capture(t, "text/event-stream", okStream)
	p, _ := New(client(ts.URL), Config{Model: "m"})
	one := 1.0
	seed := 1
	for name, req := range map[string]agent.ProviderRequest{
		"temperature": {Temperature: &one},
		"seed":        {Seed: &seed},
		"guided":      {Guided: &agent.GuidedDecoding{Grammar: "g"}},
		"penalties":   {Penalties: &agent.SamplingPenalties{Frequency: &one}},
	} {
		_, gerr := p.Generate(context.Background(), req)
		_, serr := p.Stream(context.Background(), req)
		if !errors.Is(gerr, agent.ErrUnsupportedParam) || !errors.Is(serr, agent.ErrUnsupportedParam) {
			t.Errorf("%s: %v / %v, want ErrUnsupportedParam", name, gerr, serr)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("refused requests reached the server %d times", hits.Load())
	}
	if c := agent.CapabilitiesOf(p); c.Seed || c.Temperature || len(c.Guided) > 0 || len(c.Penalties) > 0 {
		t.Errorf("capabilities = %+v, want none", c)
	}
}

const sse = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":5,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}` + "\n\n" +
	"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"reading"}}` + "\n\n" +
	"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t2","name":"files_read","input":{}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"b.go\"}"}}` + "\n\n" +
	"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":2}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}` + "\n\n" +
	"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

// A streamed reply maps onto chakra's deltas, with the tool's real name
// restored and the cache reads reported.
func TestStreamDeltas(t *testing.T) {
	ts, body, _ := capture(t, "text/event-stream", sse)
	p, _ := New(client(ts.URL), Config{Model: "m"})
	s, err := p.Stream(context.Background(), conversation())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got []agent.Delta
	for {
		d, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, d)
	}
	want := []agent.Delta{
		{Kind: agent.DeltaReasoning, Text: "hmm"},
		{Kind: agent.DeltaText, Text: "reading"},
		{Kind: agent.DeltaToolCallStart, Index: 2, ToolCallID: "t2", ToolName: "files.read"},
		{Kind: agent.DeltaToolCallArgs, Index: 2, Text: `{"path":"b.go"}`},
		{Kind: agent.DeltaFinish, FinishReason: "tool_use"},
		{Kind: agent.DeltaUsage, Usage: &agent.Usage{InputTokens: 105, OutputTokens: 7, CachedInputTokens: 90}},
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Fatalf("deltas\n got %s\nwant %s", g, w)
	}
	if (*body)["stream"] != true {
		t.Errorf("stream = %v", (*body)["stream"])
	}
}

func TestGenerateUsage(t *testing.T) {
	ts, _, _ := capture(t, "text/event-stream", streamed("ok", `{"input_tokens":3,"cache_read_input_tokens":40,"cache_creation_input_tokens":2,"output_tokens":0}`))
	p, _ := New(client(ts.URL), Config{Model: "m"})
	resp, err := p.Generate(context.Background(), conversation())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ok" || resp.Usage == nil || *resp.Usage != (agent.Usage{InputTokens: 45, OutputTokens: 1, CachedInputTokens: 40}) {
		t.Errorf("text %q usage %+v", resp.Text, resp.Usage)
	}
}

func TestNewNeedsClientAndModel(t *testing.T) {
	if _, err := New(nil, Config{Model: "m"}); err == nil {
		t.Error("a nil client was accepted")
	}
	if _, err := New(client("http://x"), Config{}); err == nil {
		t.Error("an empty model was accepted")
	}
}

// The Runner's structured finalize sends the history ending on the model's
// own answer. Current models refuse a conversation that ends on the
// assistant, since they take no prefill. So the request gets one user turn
// asking for that answer as the JSON document.
func TestStructuredFinalizeEndsOnAUserTurn(t *testing.T) {
	ts, body, _ := capture(t, "text/event-stream", streamed(`{"ok":true}`, `{"input_tokens":3,"output_tokens":0}`))
	p, _ := New(client(ts.URL), Config{Model: "m"})
	schema := core.NewRawJSON([]byte(`{"type":"object"}`))
	msgs := []agent.Message{{Role: agent.RoleUser, Text: "fix it"}, {Role: agent.RoleAssistant, Text: "Fixed Sum's loop."}}
	if _, err := p.Generate(context.Background(), agent.ProviderRequest{Messages: msgs, ResponseSchema: schema}); err != nil {
		t.Fatal(err)
	}
	sent, _ := (*body)["messages"].([]any)
	if len(sent) != 3 {
		t.Fatalf("sent %d messages, want the history plus one user turn", len(sent))
	}
	last := sent[2].(map[string]any)
	if last["role"] != "user" || !strings.Contains(fmt.Sprint(last["content"]), "JSON") {
		t.Errorf("last message = %v", last)
	}
	if _, err := p.Generate(context.Background(), agent.ProviderRequest{Messages: msgs[:1], ResponseSchema: schema}); err != nil {
		t.Fatal(err)
	}
	if sent, _ := (*body)["messages"].([]any); len(sent) != 1 {
		t.Errorf("control: a request already ending on the user got %d messages", len(sent))
	}
	if _, err := p.Generate(context.Background(), agent.ProviderRequest{Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	if sent, _ := (*body)["messages"].([]any); len(sent) != 2 {
		t.Errorf("control: with no schema, the history went out as %d messages", len(sent))
	}
}

// Structured outputs take a subset of JSON Schema. Picture ext/git's review
// schema, with a priority from 0 to 3. The API answers 400 to "minimum", so
// the provider drops the keywords it can't send and closes every object, as
// the Python and TypeScript SDKs do. The Runner still validates the reply
// against the full schema and asks again on a mismatch.
func TestStructuredSchemaSentAsTheSupportedSubset(t *testing.T) {
	ts, body, _ := capture(t, "text/event-stream", streamed(`{}`, `{"input_tokens":3,"output_tokens":0}`))
	p, _ := New(client(ts.URL), Config{Model: "m"})
	full := `{"type":"object","properties":{
		"findings":{"type":"array","minItems":1,"maxItems":9,"items":{"type":"object","properties":{
			"priority":{"type":"integer","minimum":0,"maximum":3},
			"confidence":{"type":"number","exclusiveMinimum":0,"multipleOf":0.1},
			"title":{"type":"string","minLength":1,"maxLength":80,"format":"uri"}}}},
		"verdict":{"anyOf":[{"$ref":"#/$defs/v"},{"type":"null"}]}},
		"additionalProperties":true,
		"$defs":{"v":{"type":"object","properties":{"ok":{"type":"boolean"}}}}}`
	if _, err := p.Generate(context.Background(), agent.ProviderRequest{Messages: []agent.Message{{Role: agent.RoleUser, Text: "review"}}, ResponseSchema: core.NewRawJSON([]byte(full))}); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal((*body)["output_config"].(map[string]any)["format"].(map[string]any)["schema"])
	want := `{"$defs":{"v":{"additionalProperties":false,"properties":{"ok":{"type":"boolean"}},"type":"object"}},"additionalProperties":false,"properties":{"findings":{"items":{"additionalProperties":false,"properties":{"confidence":{"type":"number"},"priority":{"type":"integer"},"title":{"format":"uri","type":"string"}},"type":"object"},"type":"array"},"verdict":{"anyOf":[{"$ref":"#/$defs/v"},{"type":"null"}]}},"type":"object"}`
	if string(got) != want {
		t.Errorf("schema sent\n got %s\nwant %s", got, want)
	}
}
