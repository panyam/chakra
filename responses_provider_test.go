package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func newTestResponsesProvider(t *testing.T, url string, cfg ResponsesConfig) *ResponsesProvider {
	t.Helper()
	cfg.BaseURL, cfg.Model = url, "test-model"
	p, err := NewResponsesProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// rsFake is a Responses endpoint that numbers its responses and records each
// request body. reply builds the SSE body for the nth request (from 1).
type rsFake struct {
	mu    sync.Mutex
	reqs  []map[string]any
	reply func(n int, req map[string]any) (status int, body string)
}

func (f *rsFake) serve(t *testing.T) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		n := len(f.reqs)
		f.mu.Unlock()
		status, body := f.reply(n, req)
		if status != 0 && status != 200 {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (f *rsFake) request(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[i]
}

// rsCallThenText replies with one function call on odd requests and text on
// even ones, each response numbered resp_<n>.
func rsReplyCall(n int, name string) string {
	id := fmt.Sprintf("resp_%d", n)
	return rsData(
		fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_%d","name":%q,"arguments":""}}`, n, name),
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
		rsCompleted(id, 10, 2))
}

func rsReplyText(n int, text string) string {
	id := fmt.Sprintf("resp_%d", n)
	return rsData(fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"delta":%q}`, text), rsCompleted(id, 10, 2))
}

func inputItems(req map[string]any) []map[string]any {
	var out []map[string]any
	items, _ := req["input"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any))
	}
	return out
}

func TestResponsesRequestWireShape(t *testing.T) {
	temp := 0.2
	p := newTestResponsesProvider(t, "http://unused", ResponsesConfig{ReasoningEffort: "low"})
	req := ProviderRequest{
		Instructions: "be brief",
		Messages: []Message{
			{Role: RoleUser, Text: "hi"},
			{Role: RoleAssistant, Text: "checking", ToolCalls: []ToolCall{{ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"x"}`))}}},
			{Role: RoleTool, ToolCallID: "c1", Text: "echo: x"},
		},
		Tools:       []core.ToolDef{{Name: "echo", Description: "echoes", InputSchema: map[string]any{"type": "object"}}},
		Temperature: &temp,
		MaxTokens:   64,
		ToolChoice:  ToolChoiceFunc("echo"),
	}
	got, err := json.Marshal(p.buildBody(req, req.Messages, "", true))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"input":[{"content":"hi","role":"user"},{"content":"checking","role":"assistant"},{"arguments":"{\"message\":\"x\"}","call_id":"c1","name":"echo","type":"function_call"},{"call_id":"c1","output":"echo: x","type":"function_call_output"}],"instructions":"be brief","max_output_tokens":64,"model":"test-model","reasoning":{"effort":"low","summary":"auto"},"store":true,"stream":true,"temperature":0.2,"tool_choice":{"name":"echo","type":"function"},"tools":[{"description":"echoes","name":"echo","parameters":{"type":"object"},"type":"function"}]}`
	if string(got) != want {
		t.Fatalf("wire shape drift:\n got: %s\nwant: %s", got, want)
	}
}

func TestResponsesRefusesSeedAndGuided(t *testing.T) {
	p := newTestResponsesProvider(t, "http://unused", ResponsesConfig{})
	seed := 1
	if _, err := p.Stream(context.Background(), ProviderRequest{Seed: &seed}); !errors.Is(err, ErrUnsupportedParam) {
		t.Fatalf("seed: %v", err)
	}
	if _, err := p.Generate(context.Background(), ProviderRequest{Guided: &GuidedDecoding{Regex: "a"}}); !errors.Is(err, ErrUnsupportedParam) {
		t.Fatalf("guided: %v", err)
	}
	if c := CapabilitiesOf(p); c.Seed || len(c.Guided) != 0 {
		t.Fatalf("capabilities = %+v", c)
	}
}

func TestResponsesIncompleteIsLength(t *testing.T) {
	ts := rawServer(t, nil, "text/event-stream", rsData(
		`{"type":"response.output_text.delta","output_index":0,"delta":"cut"}`,
		`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`))
	s, err := newTestResponsesProvider(t, ts.URL, ResponsesConfig{}).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if res := drain(t, s); res.Text != "cut" || res.FinishReason != "length" {
		t.Fatalf("res = %+v", res)
	}
}

func TestResponsesFailedEventIsAnError(t *testing.T) {
	ts := rawServer(t, nil, "text/event-stream", rsData(
		`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"boom"}}}`))
	s, err := newTestResponsesProvider(t, ts.URL, ResponsesConfig{}).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Recv(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want failure carrying the server message, got %v", err)
	}
}

// A three-step tool turn through a real Runner: the second and third model
// calls chain from the previous response and carry only the tool output.
func TestResponsesRunnerChainsToolSteps(t *testing.T) {
	f := &rsFake{reply: func(n int, _ map[string]any) (int, string) {
		if n < 3 {
			return 0, rsReplyCall(n, "look")
		}
		return 0, rsReplyText(n, "done")
	}}
	ts := f.serve(t)
	var notes []string
	p := newTestResponsesProvider(t, ts.URL, ResponsesConfig{OnFullHistory: func(r string) { notes = append(notes, r) }})
	src := NewFuncSource()
	AddFunc(src, "look", "looks", func(context.Context, struct{}) (string, error) { return "seen", nil })
	r, err := NewRunner(RunnerConfig{Provider: p, Tools: src, Instructions: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{{Role: RoleUser, Text: "go"}}
	res, err := r.Run(context.Background(), history, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" || len(f.reqs) != 3 {
		t.Fatalf("text=%q requests=%d", res.Text, len(f.reqs))
	}
	if _, ok := f.request(0)["previous_response_id"]; ok {
		t.Fatal("first request must not chain")
	}
	for i, want := range []string{"resp_1", "resp_2"} {
		req := f.request(i + 1)
		if req["previous_response_id"] != want {
			t.Fatalf("request %d previous_response_id = %v, want %s", i+2, req["previous_response_id"], want)
		}
		items := inputItems(req)
		if len(items) != 1 || items[0]["type"] != "function_call_output" || items[0]["call_id"] != fmt.Sprintf("call_%d", i+1) {
			t.Fatalf("request %d input = %v, want only the tool output", i+2, items)
		}
		if req["instructions"] != "sys" || req["tools"] == nil {
			t.Fatalf("request %d must resend instructions and tools: %v", i+2, req)
		}
	}
	if len(notes) != 0 {
		t.Fatalf("OnFullHistory fired on a chained turn: %v", notes)
	}

	// The next turn chains too: the user message follows the turn's last
	// assistant message.
	history = append(history, res.Messages...)
	history = append(history, Message{Role: RoleUser, Text: "again"})
	if _, err := r.Run(context.Background(), history, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	next := f.request(3)
	if next["previous_response_id"] != "resp_3" || len(inputItems(next)) != 1 {
		t.Fatalf("next turn = %v", next)
	}
}

// The ticket's acceptance case: once compaction replaces the history's head,
// no stored response matches it, so the request carries the full compacted
// history, chains from nothing, and OnFullHistory says why.
func TestResponsesCompactedHistoryDropsCursor(t *testing.T) {
	f := &rsFake{reply: func(n int, _ map[string]any) (int, string) { return 0, rsReplyText(n, "ok") }}
	ts := f.serve(t)
	var notes []string
	p := newTestResponsesProvider(t, ts.URL, ResponsesConfig{OnFullHistory: func(r string) { notes = append(notes, r) }})

	full := []Message{{Role: RoleUser, Text: "first"}}
	res := drainGenerate(t, p, full)
	full = append(full, Message{Role: RoleAssistant, Text: res.Text}, Message{Role: RoleUser, Text: "second"})
	drainGenerate(t, p, full)
	if f.request(1)["previous_response_id"] != "resp_1" {
		t.Fatalf("uncompacted history should chain: %v", f.request(1))
	}

	compacted := []Message{
		{Role: RoleSystem, Text: "summary of: first / ok"},
		{Role: RoleAssistant, Text: "ok"},
		{Role: RoleUser, Text: "third"},
	}
	drainGenerate(t, p, compacted)
	req := f.request(2)
	if _, ok := req["previous_response_id"]; ok {
		t.Fatalf("compacted history must not chain: %v", req)
	}
	if got := len(inputItems(req)); got != len(compacted) {
		t.Fatalf("compacted request sent %d items, want the full %d", got, len(compacted))
	}
	if len(notes) != 1 || notes[0] != FullHistoryNoCursor {
		t.Fatalf("notes = %v", notes)
	}
}

func drainGenerate(t *testing.T, p Provider, msgs []Message) *ProviderResponse {
	t.Helper()
	s, err := p.Stream(context.Background(), ProviderRequest{Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	return drain(t, s)
}

func TestResponsesStaleCursorRetriesWithFullHistory(t *testing.T) {
	f := &rsFake{reply: func(n int, req map[string]any) (int, string) {
		if _, chained := req["previous_response_id"]; chained {
			return 404, `{"error":{"message":"Previous response with id 'resp_1' not found.","code":"previous_response_not_found","param":"previous_response_id"}}`
		}
		return 0, rsReplyText(n, "ok")
	}}
	ts := f.serve(t)
	var notes []string
	p := newTestResponsesProvider(t, ts.URL, ResponsesConfig{OnFullHistory: func(r string) { notes = append(notes, r) }})
	msgs := []Message{{Role: RoleUser, Text: "a"}}
	drainGenerate(t, p, msgs)
	msgs = append(msgs, Message{Role: RoleAssistant, Text: "ok"}, Message{Role: RoleUser, Text: "b"})
	if res := drainGenerate(t, p, msgs); res.Text != "ok" {
		t.Fatalf("retry result = %+v", res)
	}
	if len(f.reqs) != 3 || len(inputItems(f.request(2))) != 3 {
		t.Fatalf("want a chained attempt then a full-history retry, got %d requests", len(f.reqs))
	}
	if len(notes) != 1 || notes[0] != FullHistoryStaleCursor {
		t.Fatalf("notes = %v", notes)
	}
}

func TestResponsesNoStoreNeverChains(t *testing.T) {
	f := &rsFake{reply: func(n int, _ map[string]any) (int, string) { return 0, rsReplyText(n, "ok") }}
	ts := f.serve(t)
	called := false
	p := newTestResponsesProvider(t, ts.URL, ResponsesConfig{NoStore: true, OnFullHistory: func(string) { called = true }})
	msgs := []Message{{Role: RoleUser, Text: "a"}}
	drainGenerate(t, p, msgs)
	drainGenerate(t, p, append(msgs, Message{Role: RoleAssistant, Text: "ok"}, Message{Role: RoleUser, Text: "b"}))
	for i := range 2 {
		req := f.request(i)
		if req["store"] != false || req["previous_response_id"] != nil {
			t.Fatalf("request %d = %v", i, req)
		}
	}
	if called {
		t.Fatal("OnFullHistory must stay quiet with NoStore")
	}
}

// Conversations sharing one provider chain from their own responses only.
func TestResponsesConcurrentConversationsKeepTheirCursors(t *testing.T) {
	f := &rsFake{reply: func(n int, req map[string]any) (int, string) {
		items := inputItems(req)
		last := fmt.Sprint(items[len(items)-1]["content"])
		return 0, rsData(rsCompleted("resp_"+last, 1, 1))
	}}
	ts := f.serve(t)
	p := newTestResponsesProvider(t, ts.URL, ResponsesConfig{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for c := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := fmt.Sprintf("conv%d", c)
			msgs := []Message{{Role: RoleUser, Text: first}}
			if _, err := collect(p, msgs); err != nil {
				errs <- err
				return
			}
			msgs = append(msgs, Message{Role: RoleAssistant}, Message{Role: RoleUser, Text: first + "-b"})
			if _, err := collect(p, msgs); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, req := range f.reqs {
		items := inputItems(req)
		last := fmt.Sprint(items[len(items)-1]["content"])
		if prev, ok := req["previous_response_id"]; ok {
			if want := "resp_" + strings.TrimSuffix(last, "-b"); prev != want {
				t.Fatalf("%s chained from %v, want %s", last, prev, want)
			}
		} else if strings.HasSuffix(last, "-b") {
			t.Fatalf("%s did not chain", last)
		}
	}
}

func collect(p Provider, msgs []Message) (*ProviderResponse, error) {
	s, err := p.Stream(context.Background(), ProviderRequest{Messages: msgs})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var acc DeltaAccumulator
	for {
		d, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return acc.Result(), nil
		}
		if err != nil {
			return nil, err
		}
		acc.Add(d)
	}
}

func TestResponsesGenerateChainsToo(t *testing.T) {
	var reqs []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		reqs = append(reqs, req)
		fmt.Fprintf(w, `{"id":"resp_%d","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, len(reqs))
	}))
	defer ts.Close()
	p := newTestResponsesProvider(t, ts.URL, ResponsesConfig{})
	msgs := []Message{{Role: RoleUser, Text: "a"}}
	if _, err := p.Generate(context.Background(), ProviderRequest{Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, Message{Role: RoleAssistant, Text: "ok"}, Message{Role: RoleUser, Text: "b"})
	if _, err := p.Generate(context.Background(), ProviderRequest{Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	if reqs[1]["previous_response_id"] != "resp_1" {
		t.Fatalf("Generate did not chain: %v", reqs[1])
	}
}

func TestCursorCacheEvictsLeastRecent(t *testing.T) {
	c := newCursorCache(2)
	k := func(s string) [32]byte { return cursorKey([]Message{{Role: RoleUser, Text: s}}) }
	c.put(k("a"), "1")
	c.put(k("b"), "2")
	c.get(k("a"))
	c.put(k("c"), "3")
	if _, ok := c.get(k("b")); ok {
		t.Fatal("b should be evicted as least recently used")
	}
	if id, ok := c.get(k("a")); !ok || id != "1" {
		t.Fatal("a was used last-but-one and should survive")
	}
	if c.len() != 2 {
		t.Fatalf("len = %d", c.len())
	}
}

func TestCursorKeyIgnoresTimestampOnly(t *testing.T) {
	a := []Message{{Role: RoleUser, Text: "x"}}
	b := []Message{{Role: RoleUser, Text: "x"}}
	b[0].Timestamp = b[0].Timestamp.AddDate(1, 0, 0)
	if cursorKey(a) != cursorKey(b) {
		t.Fatal("Timestamp must not change the key")
	}
	if cursorKey(a) == cursorKey([]Message{{Role: RoleUser, Text: "y"}}) {
		t.Fatal("text must change the key")
	}
	// Field boundaries are length-prefixed, so shifting text between fields
	// cannot collide.
	if cursorKey([]Message{{Role: RoleUser, Text: "ab", ToolCallID: ""}}) == cursorKey([]Message{{Role: RoleUser, Text: "a", ToolCallID: "b"}}) {
		t.Fatal("field boundaries collide")
	}
}
