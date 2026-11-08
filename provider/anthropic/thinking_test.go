package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// thinkingStep streams a reply that thinks, says a word, and calls a tool.
// sig is the thinking block's signature. dropped, when set, is the
// input_transformations entry message_start carries.
func thinkingStep(sig, dropped string) string {
	transforms := "[]"
	if dropped != "" {
		transforms = `[{"type":"thinking_dropped","path":"` + dropped + `","reason":"prefix_binding_mismatch"}]`
	}
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"output_tokens":0},"input_transformations":` + transforms + `}}` + "\n\n" +
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}` + "\n\n" +
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + sig + `"}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"looking"}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n" +
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t1","name":"files_read","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\"}"}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":2}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
}

// recorder serves each reply in turn and keeps every request's body and
// anthropic-beta header.
type recorder struct {
	mu      sync.Mutex
	replies []string
	bodies  []map[string]any
	betas   []string
}

func (r *recorder) serve(t *testing.T) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		body := map[string]any{}
		json.Unmarshal(raw, &body)
		r.mu.Lock()
		n := len(r.bodies)
		r.bodies = append(r.bodies, body)
		r.betas = append(r.betas, req.Header.Get("anthropic-beta"))
		reply := r.replies[min(n, len(r.replies)-1)]
		r.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, reply)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func firstStep() agent.ProviderRequest {
	return agent.ProviderRequest{
		Instructions: "card",
		Messages:     []agent.Message{{Role: agent.RoleUser, Text: "fix the test"}},
		Tools:        []core.ToolDef{{Name: "files.read", Description: "reads", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
	}
}

// nextStep is the request the Runner sends after resp: the same history,
// the assistant message it built from resp, and the tool's result.
func nextStep(prev agent.ProviderRequest, resp *agent.ProviderResponse) agent.ProviderRequest {
	next := prev
	next.Messages = append(append([]agent.Message(nil), prev.Messages...),
		agent.Message{Role: agent.RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls},
		agent.Message{Role: agent.RoleTool, ToolCallID: "t1", Text: "package a"})
	return next
}

// assistantContent is the content of the request's first assistant message.
func assistantContent(t *testing.T, body map[string]any) []any {
	t.Helper()
	for _, m := range body["messages"].([]any) {
		msg := m.(map[string]any)
		if msg["role"] == "assistant" {
			return msg["content"].([]any)
		}
	}
	t.Fatal("no assistant message in the request")
	return nil
}

func blockTypes(content []any) []string {
	var out []string
	for _, b := range content {
		out = append(out, b.(map[string]any)["type"].(string))
	}
	return out
}

// Imagine Claude on step 2 of a coding turn. It reasoned before calling
// files.read on step 1. The provider keeps that signed block and sends it
// back with the assistant message, so the model can read why it made the
// call.
func TestThinkingIsResentOnTheNextStep(t *testing.T) {
	rec := &recorder{replies: []string{thinkingStep("sig1", ""), okStream}}
	p, err := New(client(rec.serve(t).URL), Config{Model: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req := firstStep()
	resp, err := p.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(ctx, nextStep(req, resp)); err != nil {
		t.Fatal(err)
	}

	content := assistantContent(t, rec.bodies[1])
	if got := blockTypes(content); len(got) != 3 || got[0] != "thinking" || got[1] != "text" || got[2] != "tool_use" {
		t.Fatalf("step 2 sent the assistant message as %v, want [thinking text tool_use]", got)
	}
	if sig := content[0].(map[string]any)["signature"]; sig != "sig1" {
		t.Fatalf("resent signature = %v, want sig1", sig)
	}
	// A resent block goes with drop_block, so a block the API no longer
	// accepts is dropped rather than failing the turn.
	if rec.betas[1] != thinkingBindingBeta {
		t.Fatalf("anthropic-beta = %q, want %q", rec.betas[1], thinkingBindingBeta)
	}
	thinking, _ := rec.bodies[1]["thinking"].(map[string]any)
	binding, _ := thinking["block_binding"].(map[string]any)
	if thinking["type"] != "adaptive" || binding["prefix_mismatch_behavior"] != "drop_block" {
		t.Fatalf("thinking = %v, want adaptive with drop_block", rec.bodies[1]["thinking"])
	}
	if st := p.ThinkingStats(); st.Resent != 1 || st.Dropped != 0 {
		t.Fatalf("stats = %+v, want 1 resent, 0 dropped", st)
	}
}

// The first request has nothing to resend, so it carries no beta header and
// no thinking field. The model's own default applies.
func TestFirstRequestSendsNoBinding(t *testing.T) {
	rec := &recorder{replies: []string{thinkingStep("sig1", "")}}
	p, _ := New(client(rec.serve(t).URL), Config{Model: "claude-sonnet-5-5"})
	if _, err := p.Generate(context.Background(), firstStep()); err != nil {
		t.Fatal(err)
	}
	if rec.betas[0] != "" {
		t.Fatalf("anthropic-beta = %q on a request that resends nothing", rec.betas[0])
	}
	if _, ok := rec.bodies[0]["thinking"]; ok {
		t.Fatalf("thinking = %v, want it left to the model", rec.bodies[0]["thinking"])
	}
}

// Imagine a compactor rewrote the head of the turn. The block was signed
// over the old head, so sending it would get it dropped or refused. The
// provider sends the assistant message without it.
func TestThinkingIsNotResentAfterTheHistoryChanged(t *testing.T) {
	rec := &recorder{replies: []string{thinkingStep("sig1", ""), okStream}}
	p, _ := New(client(rec.serve(t).URL), Config{Model: "claude-sonnet-5-5"})
	ctx := context.Background()
	req := firstStep()
	resp, err := p.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	next := nextStep(req, resp)
	next.Messages[0] = agent.Message{Role: agent.RoleUser, Text: "Summary: the user asked to fix a test."}
	if _, err := p.Generate(ctx, next); err != nil {
		t.Fatal(err)
	}
	if got := blockTypes(assistantContent(t, rec.bodies[1])); len(got) != 2 || got[0] != "text" {
		t.Fatalf("after a rewrite the assistant message went as %v, want [text tool_use]", got)
	}
	if rec.betas[1] != "" {
		t.Fatalf("anthropic-beta = %q on a request that resends nothing", rec.betas[1])
	}
}

// The system prompt and the tools are part of what a block is bound to, so a
// change to either one stops the resend too.
func TestThinkingIsNotResentAfterTheToolsChanged(t *testing.T) {
	rec := &recorder{replies: []string{thinkingStep("sig1", ""), okStream}}
	p, _ := New(client(rec.serve(t).URL), Config{Model: "claude-sonnet-5-5"})
	ctx := context.Background()
	req := firstStep()
	resp, err := p.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	next := nextStep(req, resp)
	next.Tools = append(next.Tools, core.ToolDef{Name: "files.write", InputSchema: map[string]any{"type": "object"}})
	if _, err := p.Generate(ctx, next); err != nil {
		t.Fatal(err)
	}
	if got := blockTypes(assistantContent(t, rec.bodies[1])); got[0] == "thinking" {
		t.Fatalf("after a tool change the assistant message went as %v, want no thinking", got)
	}
}

// A block the model produced while streaming is kept too.
func TestStreamedThinkingIsResent(t *testing.T) {
	rec := &recorder{replies: []string{thinkingStep("sig2", ""), okStream}}
	p, _ := New(client(rec.serve(t).URL), Config{Model: "claude-sonnet-5-5"})
	ctx := context.Background()
	req := firstStep()
	s, err := p.Stream(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := collect(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(ctx, nextStep(req, resp)); err != nil {
		t.Fatal(err)
	}
	content := assistantContent(t, rec.bodies[1])
	if got := blockTypes(content); got[0] != "thinking" || content[0].(map[string]any)["signature"] != "sig2" {
		t.Fatalf("step 2 after a streamed step sent %v, want the sig2 thinking block first", got)
	}
}

// The API reports each block it dropped in input_transformations, and the
// provider counts them.
func TestDroppedBlocksAreCounted(t *testing.T) {
	rec := &recorder{replies: []string{thinkingStep("sig1", ""), thinkingStep("sig3", "messages.1.content.0")}}
	p, _ := New(client(rec.serve(t).URL), Config{Model: "claude-sonnet-5-5"})
	ctx := context.Background()
	req := firstStep()
	resp, err := p.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(ctx, nextStep(req, resp)); err != nil {
		t.Fatal(err)
	}
	if st := p.ThinkingStats(); st.Resent != 1 || st.Dropped != 1 {
		t.Fatalf("stats = %+v, want 1 resent, 1 dropped", st)
	}
}

func TestNoThinkingReplay(t *testing.T) {
	rec := &recorder{replies: []string{thinkingStep("sig1", ""), okStream}}
	p, _ := New(client(rec.serve(t).URL), Config{Model: "claude-sonnet-5-5", NoThinkingReplay: true})
	ctx := context.Background()
	req := firstStep()
	resp, err := p.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(ctx, nextStep(req, resp)); err != nil {
		t.Fatal(err)
	}
	if got := blockTypes(assistantContent(t, rec.bodies[1])); got[0] == "thinking" {
		t.Fatalf("NoThinkingReplay still sent %v", got)
	}
}

func TestThinkingConfig(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want map[string]any
	}{
		{Config{Thinking: "adaptive"}, map[string]any{"type": "adaptive"}},
		{Config{Thinking: "adaptive", ThinkingDisplay: "summarized"}, map[string]any{"type": "adaptive", "display": "summarized"}},
		{Config{Thinking: "between_tools"}, map[string]any{"type": "between_tools"}},
	} {
		ts, body, _ := capture(t, "text/event-stream", okStream)
		c.cfg.Model = "claude-sonnet-5-5"
		p, err := New(client(ts.URL), c.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Generate(context.Background(), firstStep()); err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal((*body)["thinking"])
		want, _ := json.Marshal(c.want)
		if string(got) != string(want) {
			t.Errorf("%+v: thinking = %s, want %s", c.cfg, got, want)
		}
	}
	for _, bad := range []Config{
		{Model: "m", Thinking: "enabled"},
		{Model: "m", Thinking: "between_tools", ThinkingDisplay: "summarized"},
	} {
		if _, err := New(client("http://x"), bad); err == nil {
			t.Errorf("New(%+v) succeeded, want an error", bad)
		}
	}
}

// between_tools takes no block_binding, so a resend there goes without
// drop_block or the beta header.
func TestBetweenToolsResendsWithoutBinding(t *testing.T) {
	rec := &recorder{replies: []string{thinkingStep("sig1", ""), okStream}}
	p, _ := New(client(rec.serve(t).URL), Config{Model: "claude-sonnet-5-5", Thinking: "between_tools"})
	ctx := context.Background()
	req := firstStep()
	resp, err := p.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(ctx, nextStep(req, resp)); err != nil {
		t.Fatal(err)
	}
	if got := blockTypes(assistantContent(t, rec.bodies[1])); got[0] != "thinking" {
		t.Fatalf("between_tools step 2 sent %v, want thinking first", got)
	}
	if rec.betas[1] != "" {
		t.Fatalf("anthropic-beta = %q, want none with between_tools", rec.betas[1])
	}
	if got, _ := json.Marshal(rec.bodies[1]["thinking"]); string(got) != `{"type":"between_tools"}` {
		t.Fatalf("thinking = %s", got)
	}
}

// The store is bounded, so a long-lived provider shared across sessions
// doesn't grow without limit. The oldest entries go first.
func TestThinkingStoreIsBounded(t *testing.T) {
	s := newThinkingStore(2)
	for i := byte(0); i < 3; i++ {
		s.put(key{i}, storedTurn{})
	}
	if _, ok := s.get(key{0}); ok {
		t.Fatal("the oldest entry survived past the cap")
	}
	if _, ok := s.get(key{2}); !ok {
		t.Fatal("the newest entry was evicted")
	}
}

// collect folds a stream's deltas into the response the Runner builds.
func collect(s agent.Stream) (*agent.ProviderResponse, error) {
	defer s.Close()
	out := &agent.ProviderResponse{}
	args := map[int]*strings.Builder{}
	var order []int
	for {
		d, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch d.Kind {
		case agent.DeltaText:
			out.Text += d.Text
		case agent.DeltaToolCallStart:
			out.ToolCalls = append(out.ToolCalls, agent.ToolCall{ID: d.ToolCallID, Name: d.ToolName})
			args[d.Index] = &strings.Builder{}
			order = append(order, d.Index)
		case agent.DeltaToolCallArgs:
			args[d.Index].WriteString(d.Text)
		}
	}
	for i, idx := range order {
		out.ToolCalls[i].Args = core.NewRawJSON([]byte(args[idx].String()))
	}
	return out, nil
}
