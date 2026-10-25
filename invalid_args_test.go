package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// Issue 104: a model that emits tool arguments which are not valid JSON must
// not be able to put unterminated JSON into ToolCall.Args. The bad bytes would
// otherwise sit in the assistant message, and every reader of that message
// (the session store, the Watch stream, a provider re-encoding the history on
// the next step) would fail to encode it.
//
// The fix belongs where the provider builds Args, not in dispatch: by the time
// dispatch runs, the bad RawJSON is already in the history.
const badArgsJSON = "{\"text\":\"line one\nline two\"}"

// invalidArgTools is a ToolSource with one "echo" tool that records the args
// it was called with, so a test can tell "never dispatched" from "dispatched
// with empty args".
type invalidArgTools struct {
	called bool
	args   map[string]any
}

func (s *invalidArgTools) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return []core.ToolDef{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (s *invalidArgTools) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	s.called = true
	s.args = args
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "ok"}}}, nil
}

// runInvalidArgsTurn drives a full turn whose model emits badArgsJSON as the
// arguments of one call, and returns the result plus the events the turn
// emitted.
func runInvalidArgsTurn(t *testing.T) (*TurnResult, []Event, *invalidArgTools) {
	t.Helper()
	stub := NewStubProvider(
		StubTurn{Deltas: []Delta{
			{Kind: DeltaToolCallStart, Index: 0, ToolCallID: "c1", ToolName: "echo", Text: badArgsJSON},
			{Kind: DeltaFinish, FinishReason: "tool_calls"},
		}},
		StubTurn{Text: "done"},
	)
	tools := &invalidArgTools{}
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: tools})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	var events []Event
	result, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}},
		func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result, events, tools
}

// TestInvalidArgs_RunnerEventsAndRunMarshal is the wire-safety half of #104:
// with invalid model-supplied arguments, every event the runner emitted and
// the whole persisted Run must still encode as JSON.
func TestInvalidArgs_RunnerEventsAndRunMarshal(t *testing.T) {
	result, events, _ := runInvalidArgsTurn(t)

	if len(events) == 0 {
		t.Fatal("turn emitted no events")
	}
	for i, ev := range events {
		if _, err := json.Marshal(ev); err != nil {
			t.Errorf("event %d (%s) did not marshal: %v", i, ev.Kind, err)
		}
	}

	// A tool-begin event is where a surface first sees the raw arguments.
	var sawBegin bool
	for _, ev := range events {
		if ev.Kind == EventToolBegin {
			sawBegin = true
		}
	}
	if !sawBegin {
		t.Fatal("turn emitted no tool-begin event")
	}

	run := Run{
		ID:       "run-1",
		Messages: append([]Message{{Role: RoleUser, Text: "go"}}, result.Messages...),
		Events:   events,
	}
	if _, err := json.Marshal(run); err != nil {
		t.Fatalf("persisted run did not marshal: %v", err)
	}

	// And through the real store, which is what a durable backend encodes.
	store := NewInMemoryRunStore()
	ctx := context.Background()
	if _, err := store.CreateRun(ctx, CreateRunRequest{RunID: "run-1"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := store.AppendMessages(ctx, AppendMessagesRequest{RunID: "run-1", Messages: run.Messages}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	if _, err := store.AppendEvents(ctx, AppendEventsRequest{RunID: "run-1", Events: events}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	loaded, err := store.LoadRun(ctx, LoadRunRequest{RunID: "run-1"})
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if _, err := json.Marshal(loaded.Run); err != nil {
		t.Fatalf("loaded run did not marshal: %v", err)
	}
}

// TestInvalidArgs_StoredArgsAreValidJSON pins the shape of what is repaired:
// the assistant message keeps the call (so the conversation stays well-formed
// for the next step) but its Args are a valid JSON object, not the bad bytes.
func TestInvalidArgs_StoredArgsAreValidJSON(t *testing.T) {
	result, _, _ := runInvalidArgsTurn(t)

	var found bool
	for _, msg := range result.Messages {
		for _, tc := range msg.ToolCalls {
			found = true
			if !json.Valid(tc.Args.Raw()) {
				t.Errorf("tool call %q Args are not valid JSON: %q", tc.Name, tc.Args.Raw())
			}
			var obj map[string]any
			if err := tc.Args.Bind(&obj); err != nil {
				t.Errorf("tool call %q Args do not bind to an object: %v", tc.Name, err)
			}
			if string(tc.Args.Raw()) == badArgsJSON {
				t.Errorf("tool call %q kept the raw invalid bytes", tc.Name)
			}
		}
	}
	if !found {
		t.Fatal("no tool call in the result messages")
	}
}

// TestInvalidArgs_ModelSeesError is the model-facing half of #104: the call
// does not run, and the model is told its arguments were invalid.
func TestInvalidArgs_ModelSeesError(t *testing.T) {
	result, _, tools := runInvalidArgsTurn(t)

	if tools.called {
		t.Error("echo tool ran despite invalid arguments")
	}

	var sawToolResult bool
	for _, msg := range result.Messages {
		if msg.Role != RoleTool {
			continue
		}
		sawToolResult = true
		if !strings.Contains(msg.Text, "invalid arguments") {
			t.Errorf("tool result %q does not tell the model its arguments were invalid", msg.Text)
		}
	}
	if !sawToolResult {
		t.Fatal("no RoleTool message fed back to the model")
	}
}

// TestDeltaAccumulator_InvalidArgsNormalized covers the streaming path: the
// accumulator is where argument fragments are promoted into a RawJSON.
func TestDeltaAccumulator_InvalidArgsNormalized(t *testing.T) {
	var acc DeltaAccumulator
	acc.Add(Delta{Kind: DeltaToolCallStart, Index: 0, ToolCallID: "c1", ToolName: "echo"})
	acc.Add(Delta{Kind: DeltaToolCallArgs, Index: 0, Text: badArgsJSON})

	call := acc.Result().ToolCalls[0]
	if !json.Valid(call.Args.Raw()) {
		t.Fatalf("accumulator promoted invalid JSON: %q", call.Args.Raw())
	}
	var obj map[string]any
	if err := call.Args.Bind(&obj); err != nil {
		t.Fatalf("accumulator result does not bind: %v", err)
	}
	if raw, ok := obj[invalidArgsKey].(string); !ok || raw != badArgsJSON {
		t.Fatalf("accumulator did not mark the arguments invalid, got %v", obj)
	}
}

// TestProviderArgsFromJSON normalizes nothing that is already fine, and repairs
// whatever is not. Every provider path that builds ToolCall.Args goes through
// it (DeltaAccumulator.Result for streaming, and the Generate paths in
// openai_provider.go and anthropic_provider.go).
func TestProviderArgsFromJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"valid object passes through", `{"a":1}`, `{"a":1}`},
		{"empty becomes empty object", ``, `{}`},
		{"whitespace only becomes empty object", " \n\t ", `{}`},
		{"bad json is wrapped", badArgsJSON, ""},
		{"non-object json is wrapped", `[1,2,3]`, ""},
		{"bare string is wrapped", `"hi"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolCallArgsFromJSON(json.RawMessage(tc.in))
			if !json.Valid(got.Raw()) {
				t.Fatalf("result is not valid JSON: %q", got.Raw())
			}
			var obj map[string]any
			if err := got.Bind(&obj); err != nil {
				t.Fatalf("result does not bind to an object: %v", err)
			}
			if tc.want != "" && string(got.Raw()) != tc.want {
				t.Errorf("got %q, want %q", got.Raw(), tc.want)
			}
			if tc.want == "" {
				if raw, ok := obj[invalidArgsKey].(string); !ok || raw != tc.in {
					t.Errorf("repaired args did not preserve the original text: %v", obj)
				}
			}
		})
	}
}

// TestDecodeToolArgs_RepairedValueIsReported makes sure dispatch can tell the
// model what it got wrong from the repaired object alone.
func TestDecodeToolArgs_RepairedValueIsReported(t *testing.T) {
	_, err := decodeToolArgs(toolCallArgsFromJSON(json.RawMessage(badArgsJSON)))
	if err == nil {
		t.Fatal("decodeToolArgs accepted repaired invalid arguments")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("error %q does not explain the problem", err)
	}

	args, err := decodeToolArgs(toolCallArgsFromJSON(json.RawMessage(`{"a":1}`)))
	if err != nil {
		t.Fatalf("decodeToolArgs rejected valid arguments: %v", err)
	}
	if args["a"] != float64(1) {
		t.Errorf("valid arguments decoded wrong: %v", args)
	}
}
