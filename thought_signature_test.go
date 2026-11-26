package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// sentSignatures decodes a chat-completions request body and returns, for
// each tool call in its messages, the call ID and the thought signature sent
// with it ("" when none).
func sentSignatures(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var req struct {
		Messages []struct {
			ToolCalls []oaToolCall `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	out := map[string]string{}
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			out[tc.ID] = tc.signature()
		}
	}
	return out
}

// replayServer answers the first request with first and every later one
// with a plain reply, keeping the last request body.
func replayServer(t *testing.T, first string, last *[]byte) *httptest.Server {
	t.Helper()
	n := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last, _ = io.ReadAll(r.Body)
		n++
		if n == 1 {
			io.WriteString(w, first)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"5"},"finish_reason":"stop"}]}`)
	}))
}

func toolTurn(call ToolCall, result string) []Message {
	return []Message{
		{Role: RoleUser, Text: "What is 2 + 3?"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
		{Role: RoleTool, ToolCallID: call.ID, Text: result},
	}
}

// Gemini 3 signs each tool call it makes and refuses the next request
// unless the signature comes back with that call.
func TestOpenAIGenerateSendsBackAThoughtSignature(t *testing.T) {
	var last []byte
	ts := replayServer(t, `{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","extra_content":{"google":{"thought_signature":"SIG1"}},"function":{"name":"calc","arguments":"{\"expr\":\"2+3\"}"}}]},"finish_reason":"tool_calls"}]}`, &last)
	defer ts.Close()
	p := newTestProvider(t, ts.URL)
	res, err := p.Generate(context.Background(), ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "What is 2 + 3?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), ProviderRequest{Messages: toolTurn(res.ToolCalls[0], "5")}); err != nil {
		t.Fatal(err)
	}
	if got := sentSignatures(t, last)["c1"]; got != "SIG1" {
		t.Fatalf("signature sent for c1 = %q, want SIG1", got)
	}
}

func TestOpenAIStreamSendsBackAThoughtSignature(t *testing.T) {
	var last []byte
	ts := sseServer(t, &last,
		`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","extra_content":{"google":{"thought_signature":"SIG1"}},"function":{"name":"calc","arguments":"{\"expr\":\"2+3\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	defer ts.Close()
	p := newTestProvider(t, ts.URL)
	s, err := p.Stream(context.Background(), ProviderRequest{Messages: []Message{{Role: RoleUser, Text: "What is 2 + 3?"}}})
	if err != nil {
		t.Fatal(err)
	}
	res := drain(t, s)
	s.Close()
	s, err = p.Stream(context.Background(), ProviderRequest{Messages: toolTurn(res.ToolCalls[0], "5")})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, s)
	s.Close()
	if got := sentSignatures(t, last)["c1"]; got != "SIG1" {
		t.Fatalf("signature sent for c1 = %q, want SIG1", got)
	}
}

// A history the provider never saw, such as a resumed session's, has no
// signatures. With ThoughtSignatures set, the first call of each assistant
// message gets Google's placeholder. Without it, nothing is added, since
// other servers may refuse the field.
func TestUnknownToolCallsGetThePlaceholderOnlyWhenAsked(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Text: "What are 2 + 3 and 7 * 8?"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Name: "calc", Args: core.NewRawJSON(json.RawMessage(`{"expr":"2+3"}`))},
			{ID: "c2", Name: "calc", Args: core.NewRawJSON(json.RawMessage(`{"expr":"7*8"}`))},
		}},
		{Role: RoleTool, ToolCallID: "c1", Text: "5"},
		{Role: RoleTool, ToolCallID: "c2", Text: "56"},
	}
	for _, tc := range []struct {
		name string
		on   bool
		want map[string]string
	}{
		{"asked", true, map[string]string{"c1": PlaceholderThoughtSignature, "c2": ""}},
		{"not asked", false, map[string]string{"c1": "", "c2": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var last []byte
			ts := replayServer(t, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, &last)
			defer ts.Close()
			p, err := NewOpenAIProvider(OpenAIConfig{BaseURL: ts.URL, Model: "m", ThoughtSignatures: tc.on})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Generate(context.Background(), ProviderRequest{Messages: msgs}); err != nil {
				t.Fatal(err)
			}
			got := sentSignatures(t, last)
			for id, want := range tc.want {
				if got[id] != want {
					t.Errorf("signature sent for %s = %q, want %q", id, got[id], want)
				}
			}
		})
	}
}

// Gemini streams each parallel call whole in its own chunk, with no index.
// A missing index decodes as 0, so the second call must be told apart by
// its new ID.
func TestOpenAIStreamSeparatesParallelCallsWithoutAnIndex(t *testing.T) {
	ts := sseServer(t, nil,
		`{"choices":[{"delta":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"calc","arguments":"{\"expr\":\"2+3\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"c2","type":"function","function":{"name":"calc","arguments":"{\"expr\":\"7*8\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	defer ts.Close()
	s, err := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res := drain(t, s)
	if len(res.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2: %+v", len(res.ToolCalls), res.ToolCalls)
	}
	for i, want := range []struct{ id, args string }{{"c1", `{"expr":"2+3"}`}, {"c2", `{"expr":"7*8"}`}} {
		if c := res.ToolCalls[i]; c.ID != want.id || string(c.Args.Raw()) != want.args {
			t.Errorf("call %d = %s %s, want %s %s", i, c.ID, c.Args.Raw(), want.id, want.args)
		}
	}
}
