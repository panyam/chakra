package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// samplingServer has one tool, "summarize", that asks the client to sample
// and returns what came back, or the error.
func samplingServer(t *testing.T, maxTokens int) *httptest.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "sampling-srv", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{Name: "summarize", Description: "summarizes", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			res, err := core.Sample(ctx, core.CreateMessageRequest{
				SystemPrompt: "You summarize release notes.",
				Messages:     []core.SamplingMessage{{Role: "user", Content: core.Content{Type: "text", Text: "Summarize: fixed three bugs."}}},
				MaxTokens:    maxTokens,
			})
			if err != nil {
				return core.TextResult("sampling failed: " + err.Error()), nil
			}
			return core.TextResult("summary[" + res.Model + "," + res.StopReason + "]: " + res.Content.Text), nil
		},
	)
	hts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(hts.Close)
	return hts
}

// askLog answers asks with the next of answers and records each question.
type askLog struct {
	mu        sync.Mutex
	answers   []bool
	questions []string
}

func (l *askLog) ui(_ context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.questions = append(l.questions, req.Message)
	yes := false
	if len(l.answers) > 0 {
		yes, l.answers = l.answers[0], l.answers[1:]
	}
	return core.ElicitationResult{Action: "accept", Content: map[string]any{"confirm": yes}}, nil
}

// runSampling runs one turn that calls summarize, with the stub answering
// the sampling request in between, and returns the tool's result text and
// the stub so a test can read what the model was sent.
func runSampling(t *testing.T, cfg *Config, asks *askLog, reply string) (string, *agent.StubProvider) {
	t.Helper()
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "summarize", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: reply, FinishReason: "stop"},
		agent.StubTurn{Text: "done"},
	)
	app, err := NewApp(cfg, io.Discard, strings.NewReader(""), WithProvider(stub), WithElicitationUI(asks.ui))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "summarize the notes"); err != nil {
		t.Fatal(err)
	}
	for _, m := range stub.Requests()[len(stub.Requests())-1].Messages {
		if m.Role == agent.RoleTool {
			return m.Text, stub
		}
	}
	t.Fatal("no tool result in the last request")
	return "", nil
}

func samplingConfig(url string, s *SamplingConfig) *Config {
	cfg := testConfig(url)
	cfg.Approval = &ApprovalConfig{Mode: "read-only-auto", Rules: map[string]string{"summarize": "allow"}}
	cfg.Sampling = s
	return cfg
}

func TestSamplingAsksThenAnswersWithTheModel(t *testing.T) {
	skipOnStateless(t, "sampling/createMessage is a server-to-client request mid-call")
	hts := samplingServer(t, 500)
	asks := &askLog{answers: []bool{true}}
	got, stub := runSampling(t, samplingConfig(hts.URL, &SamplingConfig{Enabled: true}), asks, "Three bugs fixed.")
	if !strings.Contains(got, "summary[stub,endTurn]: Three bugs fixed.") {
		t.Fatalf("tool result = %q", got)
	}
	if len(asks.questions) != 1 || !strings.Contains(asks.questions[0], `MCP server "test" asks to use your model`) ||
		!strings.Contains(asks.questions[0], "You summarize release notes.") || !strings.Contains(asks.questions[0], "Summarize: fixed three bugs.") {
		t.Errorf("questions = %q", asks.questions)
	}
	req := stub.Requests()[1]
	if req.Instructions != "You summarize release notes." || len(req.Tools) != 0 || req.MaxTokens != 500 {
		t.Errorf("sampling request = instructions %q, %d tools, max %d", req.Instructions, len(req.Tools), req.MaxTokens)
	}
}

func TestSamplingDeclinedReachesTheServerAsAnError(t *testing.T) {
	skipOnStateless(t, "sampling/createMessage is a server-to-client request mid-call")
	hts := samplingServer(t, 500)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "summarize", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: "done"},
	)
	asks := &askLog{answers: []bool{false}}
	app, err := NewApp(samplingConfig(hts.URL, &SamplingConfig{Enabled: true}), io.Discard, strings.NewReader(""), WithProvider(stub), WithElicitationUI(asks.ui))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "summarize"); err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model calls; a declined sample must not reach the model", len(reqs))
	}
	var got string
	for _, m := range reqs[1].Messages {
		if m.Role == agent.RoleTool {
			got = m.Text
		}
	}
	if !strings.Contains(got, "sampling failed") || !strings.Contains(got, "declined") {
		t.Errorf("tool result = %q, want the server to see the refusal", got)
	}
}

func TestSamplingReviewCapAndAllowMode(t *testing.T) {
	skipOnStateless(t, "sampling/createMessage is a server-to-client request mid-call")
	hts := samplingServer(t, 5000)

	// Review: the second ask shows the reply, and declining it keeps it from the server.
	asks := &askLog{answers: []bool{true, false}}
	got, _ := runSampling(t, samplingConfig(hts.URL, &SamplingConfig{Enabled: true, ReviewResponse: true}), asks, "A secret summary.")
	if len(asks.questions) != 2 || !strings.Contains(asks.questions[1], "A secret summary.") {
		t.Errorf("review questions = %q", asks.questions)
	}
	if strings.Contains(got, "A secret summary.") || !strings.Contains(got, "declined to send") {
		t.Errorf("a reply the user declined reached the server: %q", got)
	}

	// Cap: the configured MaxTokens wins over the request's 5000.
	asks = &askLog{answers: []bool{true}}
	_, stub := runSampling(t, samplingConfig(hts.URL, &SamplingConfig{Enabled: true, MaxTokens: 64}), asks, "short")
	if stub.Requests()[1].MaxTokens != 64 {
		t.Errorf("max tokens = %d, want the cap 64", stub.Requests()[1].MaxTokens)
	}

	// Allow mode runs it without asking.
	asks = &askLog{}
	cfg := samplingConfig(hts.URL, &SamplingConfig{Enabled: true})
	cfg.Approval = &ApprovalConfig{Mode: "allow"}
	got, _ = runSampling(t, cfg, asks, "Unasked.")
	if len(asks.questions) != 0 || !strings.Contains(got, "Unasked.") {
		t.Errorf("allow mode: questions %q, result %q", asks.questions, got)
	}
}

func TestSamplingOffByDefault(t *testing.T) {
	skipOnStateless(t, "sampling/createMessage is a server-to-client request mid-call")
	hts := samplingServer(t, 500)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "summarize", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: "done"},
	)
	app, err := NewApp(samplingConfig(hts.URL, nil), io.Discard, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "summarize"); err != nil {
		t.Fatal(err)
	}
	var got string
	for _, m := range stub.Requests()[1].Messages {
		if m.Role == agent.RoleTool {
			got = m.Text
		}
	}
	if !strings.Contains(got, "sampling failed") || len(stub.Requests()) != 2 {
		t.Errorf("without config the server sampled: %q (%d model calls)", got, len(stub.Requests()))
	}
}

func TestSamplingRefusesNonTextContent(t *testing.T) {
	a := &App{cfg: &Config{Sampling: &SamplingConfig{Enabled: true}}}
	_, err := a.sample(context.Background(), "s", core.CreateMessageRequest{
		Messages: []core.SamplingMessage{{Role: "user", Content: core.Content{Type: "image", Data: "AAAA", MimeType: "image/png"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "only text") {
		t.Errorf("image content: err = %v", err)
	}
}
