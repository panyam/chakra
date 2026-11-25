package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// qwenCall is the reply Qwen2.5-Coder 14B wrote on llama-server for
// Declaire's x01 (panyam/declaire#378), shortened.
const qwenCall = "```json\n{\"name\": \"query\", \"arguments\": {\"program\": \"node(?p, \\\"package\\\") => ?p\", \"repos\": [\"delve\"]}}\n```"

var offered = map[string]string{"query": "query", "weather_current": "weather.current"}

// A reply made only of calls to offered tools reads as those calls, in each
// way models write them. Anything else in the reply, or a call to a tool
// that wasn't offered, leaves it text.
func TestTextToolCallsReadsOnlyAWholeReplyOfCalls(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string // name and args of each call; nil for text
	}{
		{"Qwen's fenced JSON", qwenCall, []string{`query {"program": "node(?p, \"package\") => ?p", "repos": ["delve"]}`}},
		{"a bare object", `{"name": "query", "arguments": {"program": "p"}}`, []string{`query {"program": "p"}`}},
		{"Hermes tags", "<tool_call>\n{\"name\": \"query\", \"arguments\": {}}\n</tool_call>", []string{"query {}"}},
		{"two fenced calls", "```json\n{\"name\": \"query\", \"arguments\": {\"a\": 1}}\n```\n\n```json\n{\"name\": \"query\", \"arguments\": {\"a\": 2}}\n```", []string{`query {"a": 1}`, `query {"a": 2}`}},
		{"two objects in one fence", "```\n{\"name\": \"query\", \"arguments\": {}}\n{\"name\": \"query\", \"arguments\": {}}\n```", []string{"query {}", "query {}"}},
		{"arguments as a string", `{"name": "query", "arguments": "{\"a\": 1}"}`, []string{`query {"a": 1}`}},
		{"parameters for arguments", `{"name": "query", "parameters": {"a": 1}}`, []string{`query {"a": 1}`}},
		{"a sanitized name maps back", `{"name": "weather_current", "arguments": {}}`, []string{"weather.current {}"}},
		{"prose before the call", "Let me query.\n" + qwenCall, nil},
		{"prose after the call", qwenCall + "\nThat should work.", nil},
		{"a tool that wasn't offered", `{"name": "delete_repo", "arguments": {}}`, nil},
		{"a code block in another language", "```go\n{\"name\": \"query\", \"arguments\": {}}\n```", nil},
		{"JSON that isn't a call", "```json\n{\"rows\": [1, 2]}\n```", nil},
		{"broken JSON", "```json\n{\"name\": \"query\", \"arguments\": {\n```", nil},
		{"arguments that aren't an object", `{"name": "query", "arguments": [1]}`, nil},
		{"an answer", "The package is github.com/go-delve/delve/pkg/proc.", nil},
	} {
		calls, ok := textToolCalls(tc.text, offered)
		var got []string
		for _, c := range calls {
			got = append(got, c.Name+" "+string(c.Args.Raw()))
		}
		if ok != (tc.want != nil) || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: got %q ok=%v, want %q", tc.name, got, ok, tc.want)
		}
	}
	a, _ := textToolCalls(qwenCall, offered)
	b, _ := textToolCalls(qwenCall, offered)
	if a[0].ID == b[0].ID || !strings.HasPrefix(a[0].ID, "text-call-") {
		t.Errorf("recovered calls need ids of their own: %q, %q", a[0].ID, b[0].ID)
	}
}

// chunks streams text as llama-server does, a few characters a chunk, then
// stops.
func chunks(text string) []string {
	var lines []string
	for len(text) > 0 {
		n := min(7, len(text))
		piece, _ := json.Marshal(text[:n])
		lines = append(lines, fmt.Sprintf(`{"choices":[{"delta":{"content":%s}}]}`, piece))
		text = text[n:]
	}
	return append(lines, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4}}`, `[DONE]`)
}

func llamaCpp(t *testing.T, url string) Provider {
	t.Helper()
	p, err := NewLlamaCppProvider(LlamaCppConfig{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var queryTool = []core.ToolDef{{Name: "query", InputSchema: map[string]any{"type": "object"}}}

// The llama.cpp preset reads Qwen's fenced call as a tool call, streamed and
// not, with the finish reason a tool call has and the usage kept. control:
// the plain OpenAI provider leaves it text, and so does the preset when the
// request offered no tools.
func TestLlamaCppReadsAToolCallWrittenAsText(t *testing.T) {
	ts := sseServer(t, nil, chunks(qwenCall)...)
	defer ts.Close()
	s, err := llamaCpp(t, ts.URL).Stream(context.Background(), ProviderRequest{Tools: queryTool})
	if err != nil {
		t.Fatal(err)
	}
	res := drain(t, s)
	if res.Text != "" || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "query" || res.FinishReason != "tool_calls" {
		t.Fatalf("streamed: %+v", res)
	}
	if args := string(res.ToolCalls[0].Args.Raw()); !strings.Contains(args, `"repos": ["delve"]`) {
		t.Errorf("streamed args %s", args)
	}
	if res.Usage == nil || res.Usage.InputTokens != 9 {
		t.Errorf("streamed usage %+v", res.Usage)
	}

	gen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": qwenCall}, "finish_reason": "stop"}}})
		w.Write(body)
	}))
	defer gen.Close()
	out, err := llamaCpp(t, gen.URL).Generate(context.Background(), ProviderRequest{Tools: queryTool})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "" || len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "query" || out.FinishReason != "tool_calls" {
		t.Errorf("generated: %+v", out)
	}

	plain, err := newTestProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{Tools: queryTool})
	if err != nil {
		t.Fatal(err)
	}
	if res := drain(t, plain); res.Text != qwenCall || len(res.ToolCalls) != 0 {
		t.Errorf("control: the OpenAI provider read text as calls: %+v", res)
	}
	noTools, err := llamaCpp(t, ts.URL).Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res := drain(t, noTools); res.Text != qwenCall || len(res.ToolCalls) != 0 {
		t.Errorf("control: a request with no tools read text as calls: %+v", res)
	}
}

// An answer streams as it arrives: once its text can't be a call, the held
// text goes out and the rest follows a chunk at a time. A reply that starts
// like a call and then isn't one comes out whole, as text.
func TestLlamaCppStreamsAnAnswerAsItComes(t *testing.T) {
	answer := "The package is pkg/proc, which holds the target."
	ts := sseServer(t, nil, chunks(answer)...)
	defer ts.Close()
	s, err := llamaCpp(t, ts.URL).Stream(context.Background(), ProviderRequest{Tools: queryTool})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	var acc DeltaAccumulator
	for {
		d, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind == DeltaText {
			texts = append(texts, d.Text)
		}
		acc.Add(d)
	}
	if len(texts) < 3 || texts[0] != "The pac" || acc.Result().Text != answer || acc.Result().FinishReason != "stop" {
		t.Errorf("an answer came as %q, finish %q", texts, acc.Result().FinishReason)
	}

	code := "```go\nfunc main() {}\n```"
	ts2 := sseServer(t, nil, chunks(code)...)
	defer ts2.Close()
	s2, err := llamaCpp(t, ts2.URL).Stream(context.Background(), ProviderRequest{Tools: queryTool})
	if err != nil {
		t.Fatal(err)
	}
	if res := drain(t, s2); res.Text != code || len(res.ToolCalls) != 0 || res.FinishReason != "stop" {
		t.Errorf("a code answer: %+v", res)
	}
}

// A Runner on llama.cpp runs the tool Qwen asked for as text, and the
// model's next reply answers the turn: the turn panyam/declaire#378 lost.
func TestRunnerRunsAToolCallWrittenAsText(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		lines := chunks(qwenCall)
		if calls.Add(1) > 1 {
			lines = chunks("delve has 3 packages.")
		}
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
		}
	}))
	defer ts.Close()
	src := NewFuncSource()
	var ran []string
	AddFunc(src, "query", "runs a program", func(ctx context.Context, in struct {
		Program string   `json:"program"`
		Repos   []string `json:"repos"`
	}) (string, error) {
		ran = append(ran, in.Program+" in "+strings.Join(in.Repos, ","))
		return "3 rows", nil
	})
	r, err := NewRunner(RunnerConfig{Provider: llamaCpp(t, ts.URL), Tools: src})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "How many packages?"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != `node(?p, "package") => ?p in delve` {
		t.Errorf("the tool ran %q", ran)
	}
	if res.Text != "delve has 3 packages." || res.Steps != 2 {
		t.Errorf("turn: steps %d, text %q", res.Steps, res.Text)
	}
}
