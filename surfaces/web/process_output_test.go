package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/host"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
	"github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1/webv1connect"
	"github.com/panyam/mcpkit/core"
)

// TestWatchStreamsProcessOutputWithoutPolling is #214's acceptance. A process
// started with exec_command shows its output on Watch as it prints. That
// happens after the starting turn has ended, with nobody calling write_stdin.
// The model's tool result holds only what it collected.
func TestWatchStreamsProcessOutputWithoutPolling(t *testing.T) {
	ext, err := exec.New(exec.Config{
		Roots:   []string{t.TempDir()},
		Sandbox: exec.Unconfined(),
		Shell: &exec.ShellConfig{
			Rules:     []exec.PrefixRule{{Prefix: [][]string{{"bash", "sh"}}, Decision: exec.Allow}},
			Processes: &exec.ProcessConfig{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := core.NewRawJSON(json.RawMessage(`{"cmd":"echo first; sleep 2; echo second; sleep 30","yield_ms":1000}`))
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: exec.ExecCommandTool, Args: args}}},
		agent.StubTurn{Text: "started it", FinishReason: "stop"},
	)
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "stub-model"}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(stub), host.WithExtension(ext), host.WithObserver(&recObserver{}))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	defer app.Close()

	srv := httptest.NewServer(Handler(app))
	defer srv.Close()
	client := webv1connect.NewHostServiceClient(srv.Client(), srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := startWatch(t, ctx, client)

	if err := app.RunTurn(ctx, "start the server"); err != nil {
		t.Fatal(err)
	}
	reqs := stub.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model requests, want 2", len(reqs))
	}
	var result string
	for _, m := range reqs[1].Messages {
		if m.Role == agent.RoleTool {
			result += m.Text
		}
	}
	if !strings.Contains(result, "still running") || strings.Contains(result, "second\n") {
		t.Fatalf("tool result %q, want a running process without second", result)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		var streamed strings.Builder
		w.mu.Lock()
		for _, f := range w.frames {
			if f.GetKind() != string(host.HostProcessOutput) {
				continue
			}
			var ev host.HostEvent
			if err := json.Unmarshal(f.GetPayload(), &ev); err != nil || ev.ProcessOutput == nil {
				t.Fatalf("frame payload %s: %v", f.GetPayload(), err)
			}
			if ev.ProcessOutput.ID != "p1" || ev.ProcessOutput.Source != "exec" {
				t.Fatalf("output not keyed to p1: %+v", ev.ProcessOutput)
			}
			streamed.WriteString(ev.ProcessOutput.Text)
		}
		w.mu.Unlock()
		if strings.Contains(streamed.String(), "second\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second line never streamed; got %q, frames %v", streamed.String(), w.kinds())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProcessControlRPCsReportFailuresAsPreconditions(t *testing.T) {
	app := modelAppForControl(t)
	srv := httptest.NewServer(Handler(app))
	defer srv.Close()
	client := webv1connect.NewHostServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()
	_, err := client.ResizeProcess(ctx, connect.NewRequest(&webv1.ResizeProcessRequest{Source: "exec", ProcessId: "p1", Cols: 80, Rows: 24}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "no process control") {
		t.Errorf("resize with no controller: err = %v", err)
	}
	_, err = client.WriteProcess(ctx, connect.NewRequest(&webv1.WriteProcessRequest{Source: "exec", ProcessId: "p1", Data: "q"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("write with no controller: err = %v", err)
	}
}

func modelAppForControl(t *testing.T) *host.App {
	t.Helper()
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "stub"}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}
