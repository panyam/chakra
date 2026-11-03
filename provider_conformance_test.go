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
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
)

// conformanceWire is one provider's wire for the shared conformance
// scenarios. Each scenario builds the same logical response in the
// provider's own encoding and asserts on the folded ProviderResponse, so a
// provider passes only by producing what OpenAIProvider produces.
type conformanceWire struct {
	name        string
	newProvider func(t *testing.T, url string) Provider
	// SSE bodies, each a complete stream.
	text      func(parts []string, in, out int) string
	parallel  func() string // a1 search {"q":"cats"} split in two, b2 zoom {}, interleaved
	reasoning func(reason, text string) string
	oneCall   func(id, name string) string
	// firstText is the opening of a stream that emits one text delta and
	// then hangs.
	firstText func(text string) string
	// Generate bodies.
	genText    func(text string, in, out int) string
	genCall    func(id, name, args string) string
	hasSchema  func(req map[string]any) bool
	toolsNamed func(req map[string]any) []string
}

func oaData(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "data: %s\n\n", l)
	}
	return b.String()
}

func rsData(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var probe struct{ Type string }
		json.Unmarshal([]byte(e), &probe)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", probe.Type, e)
	}
	return b.String()
}

func rsCompleted(id string, in, out int) string {
	return fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","usage":{"input_tokens":%d,"output_tokens":%d}}}`, id, in, out)
}

var conformanceWires = []conformanceWire{
	{
		name:        "openai",
		newProvider: func(t *testing.T, url string) Provider { return newTestProvider(t, url) },
		text: func(parts []string, in, out int) string {
			var lines []string
			for _, p := range parts {
				lines = append(lines, fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, p))
			}
			return oaData(append(lines,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				fmt.Sprintf(`{"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, in, out),
				`[DONE]`)...)
		},
		parallel: func() string {
			return oaData(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a1","function":{"name":"search","arguments":""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b2","function":{"name":"zoom","arguments":"{}"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"cats\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`[DONE]`)
		},
		reasoning: func(reason, text string) string {
			return oaData(
				fmt.Sprintf(`{"choices":[{"delta":{"reasoning_content":%q}}]}`, reason),
				fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, text),
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`[DONE]`)
		},
		oneCall: func(id, name string) string {
			return oaData(
				fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"function":{"name":%q,"arguments":"{}"}}]}}]}`, id, name),
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`[DONE]`)
		},
		firstText: func(text string) string {
			return oaData(fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, text))
		},
		genText: func(text string, in, out int) string {
			return fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, text, in, out)
		},
		genCall: func(id, name, args string) string {
			return fmt.Sprintf(`{"choices":[{"message":{"content":"","tool_calls":[{"id":%q,"function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`, id, name, args)
		},
		hasSchema: func(req map[string]any) bool {
			rf, ok := req["response_format"].(map[string]any)
			return ok && rf["type"] == "json_schema"
		},
		toolsNamed: func(req map[string]any) []string {
			var out []string
			tools, _ := req["tools"].([]any)
			for _, t := range tools {
				fn, _ := t.(map[string]any)["function"].(map[string]any)
				out = append(out, fmt.Sprint(fn["name"]))
			}
			return out
		},
	},
	{
		name:        "responses",
		newProvider: func(t *testing.T, url string) Provider { return newTestResponsesProvider(t, url, ResponsesConfig{}) },
		text: func(parts []string, in, out int) string {
			events := []string{`{"type":"response.created","response":{"id":"resp_1"}}`}
			for _, p := range parts {
				events = append(events, fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"delta":%q}`, p))
			}
			return rsData(append(events, rsCompleted("resp_1", in, out))...)
		},
		parallel: func() string {
			return rsData(
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
				`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"a1","name":"search","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"q\":"}`,
				`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_2","call_id":"b2","name":"zoom","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{}"}`,
				`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"cats\"}"}`,
				rsCompleted("resp_1", 0, 0))
		},
		reasoning: func(reason, text string) string {
			return rsData(
				fmt.Sprintf(`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":%q}`, reason),
				fmt.Sprintf(`{"type":"response.output_text.delta","output_index":1,"delta":%q}`, text),
				rsCompleted("resp_1", 0, 0))
		},
		oneCall: func(id, name string) string {
			return rsData(
				fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":%q,"name":%q,"arguments":""}}`, id, name),
				`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
				rsCompleted("resp_1", 0, 0))
		},
		firstText: func(text string) string {
			return rsData(fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"delta":%q}`, text))
		},
		genText: func(text string, in, out int) string {
			return fmt.Sprintf(`{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":%d,"output_tokens":%d}}`, text, in, out)
		},
		genCall: func(id, name, args string) string {
			return fmt.Sprintf(`{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":%q,"name":%q,"arguments":%q}]}`, id, name, args)
		},
		hasSchema: func(req map[string]any) bool {
			text, _ := req["text"].(map[string]any)
			f, ok := text["format"].(map[string]any)
			return ok && f["type"] == "json_schema" && f["schema"] != nil
		},
		toolsNamed: func(req map[string]any) []string {
			var out []string
			tools, _ := req["tools"].([]any)
			for _, t := range tools {
				out = append(out, fmt.Sprint(t.(map[string]any)["name"]))
			}
			return out
		},
	},
}

// rawServer answers every request with body, recording the last request.
func rawServer(t *testing.T, capture *[]byte, contentType, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if capture != nil {
			*capture = b
		}
		w.Header().Set("Content-Type", contentType)
		io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func streamFrom(t *testing.T, w conformanceWire, body string, req ProviderRequest) (*ProviderResponse, []byte) {
	t.Helper()
	var captured []byte
	ts := rawServer(t, &captured, "text/event-stream", body)
	s, err := w.newProvider(t, ts.URL).Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	return drain(t, s), captured
}

func TestProviderConformance(t *testing.T) {
	for _, w := range conformanceWires {
		t.Run(w.name, func(t *testing.T) {
			t.Run("TextFinishUsage", func(t *testing.T) {
				res, _ := streamFrom(t, w, w.text([]string{"Hel", "lo"}, 7, 3), ProviderRequest{})
				if res.Text != "Hello" || res.FinishReason != "stop" {
					t.Fatalf("res = %+v", res)
				}
				if res.Usage == nil || res.Usage.InputTokens != 7 || res.Usage.OutputTokens != 3 {
					t.Fatalf("usage = %+v", res.Usage)
				}
			})
			t.Run("ChunkedParallelToolCalls", func(t *testing.T) {
				res, _ := streamFrom(t, w, w.parallel(), ProviderRequest{})
				if len(res.ToolCalls) != 2 {
					t.Fatalf("tool calls = %+v", res.ToolCalls)
				}
				if res.ToolCalls[0].ID != "a1" || res.ToolCalls[0].Name != "search" || string(res.ToolCalls[0].Args.Raw()) != `{"q":"cats"}` {
					t.Fatalf("call 0 = %+v args=%s", res.ToolCalls[0], res.ToolCalls[0].Args.Raw())
				}
				if res.ToolCalls[1].ID != "b2" || res.ToolCalls[1].Name != "zoom" || string(res.ToolCalls[1].Args.Raw()) != `{}` {
					t.Fatalf("call 1 = %+v", res.ToolCalls[1])
				}
				if res.FinishReason != "tool_calls" {
					t.Fatalf("finish = %q", res.FinishReason)
				}
			})
			t.Run("ReasoningDeltas", func(t *testing.T) {
				res, _ := streamFrom(t, w, w.reasoning("thinking...", "answer"), ProviderRequest{})
				if res.Reasoning != "thinking..." || res.Text != "answer" {
					t.Fatalf("res = %+v", res)
				}
			})
			t.Run("ToolNameSanitizedAndReversed", func(t *testing.T) {
				res, body := streamFrom(t, w, w.oneCall("a1", "weather_current"), ProviderRequest{
					Tools: []core.ToolDef{{Name: "weather.current", InputSchema: map[string]any{"type": "object"}}},
				})
				var req map[string]any
				json.Unmarshal(body, &req)
				if names := w.toolsNamed(req); len(names) != 1 || names[0] != "weather_current" || strings.Contains(string(body), "weather.current") {
					t.Fatalf("tools-list name not sanitized: %v\n%s", names, body)
				}
				if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "weather.current" {
					t.Fatalf("tool call name not reversed: %+v", res.ToolCalls)
				}
			})
			t.Run("HTTPError", func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
					rw.WriteHeader(401)
					fmt.Fprint(rw, `{"error":"bad key"}`)
				}))
				defer ts.Close()
				_, err := w.newProvider(t, ts.URL).Stream(context.Background(), ProviderRequest{})
				var perr *ProviderError
				if !errors.As(err, &perr) || perr.StatusCode != 401 || !strings.Contains(perr.Body, "bad key") {
					t.Fatalf("want ProviderError 401, got %v", err)
				}
			})
			t.Run("CancelMidStream", func(t *testing.T) {
				release := make(chan struct{})
				ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
					rw.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(rw, w.firstText("first"))
					rw.(http.Flusher).Flush()
					<-release
				}))
				defer ts.Close()
				defer close(release)
				ctx, cancel := context.WithCancel(context.Background())
				s, err := w.newProvider(t, ts.URL).Stream(ctx, ProviderRequest{})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if d, err := s.Recv(); err != nil || d.Text != "first" {
					t.Fatalf("first delta: %v %v", d, err)
				}
				go func() {
					time.Sleep(20 * time.Millisecond)
					cancel()
				}()
				if _, err := s.Recv(); !errors.Is(err, context.Canceled) {
					t.Fatalf("want context.Canceled, got %v", err)
				}
			})
			t.Run("GenerateStructured", func(t *testing.T) {
				var captured []byte
				ts := rawServer(t, &captured, "application/json", w.genText(`{"name":"trip"}`, 5, 2))
				res, err := w.newProvider(t, ts.URL).Generate(context.Background(), ProviderRequest{
					Messages:       []Message{{Role: RoleUser, Text: "name it"}},
					ResponseSchema: core.NewRawJSON(json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)),
				})
				if err != nil {
					t.Fatal(err)
				}
				if res.Text != `{"name":"trip"}` || res.Usage == nil || res.Usage.InputTokens != 5 || res.FinishReason != "stop" {
					t.Fatalf("res = %+v", res)
				}
				var req map[string]any
				json.Unmarshal(captured, &req)
				if !w.hasSchema(req) {
					t.Fatalf("request carries no JSON schema: %s", captured)
				}
				if _, streaming := req["stream"]; streaming {
					t.Fatal("Generate must not stream")
				}
			})
			t.Run("GenerateToolCalls", func(t *testing.T) {
				ts := rawServer(t, nil, "application/json", w.genCall("t1", "go", `{"x":1}`))
				res, err := w.newProvider(t, ts.URL).Generate(context.Background(), ProviderRequest{})
				if err != nil {
					t.Fatal(err)
				}
				if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "t1" || res.ToolCalls[0].Name != "go" || string(res.ToolCalls[0].Args.Raw()) != `{"x":1}` || res.FinishReason != "tool_calls" {
					t.Fatalf("res = %+v", res)
				}
			})
		})
	}
}
