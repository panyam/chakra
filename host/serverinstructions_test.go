package host

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// TestServerInstructionsReachSystemPrompt pins issue #76: an MCP server may
// declare instructions for the model in its initialize result
// (client.Client.Instructions). The host must fold them into the per-turn
// system prompt, labelled with the server's host-assigned id so the model can
// tell which origin they came from, and framed as untrusted server-provided
// data rather than host instructions (the same rule SEP-2640 already sets for
// skill blocks — see originHeader in skills.go).
//
// It is end-to-end on purpose: the instructions only exist on a real
// initialize result, so a unit test over a hand-built App would not show the
// gap.
func TestServerInstructionsReachSystemPrompt(t *testing.T) {
	const wantInstr = "Always call echo before answering."

	srv := server.NewServer(core.ServerInfo{Name: "instr-srv", Version: "0.0.1"},
		server.WithInstructions(wantInstr))
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	cfg := testConfig(ts.URL)
	cfg.Instructions = "base"

	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	app, err := NewApp(cfg, &syncWriter{}, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	reqs := stub.Requests()
	if len(reqs) == 0 {
		t.Fatal("no provider request recorded")
	}
	// Capture the real value rather than only asserting on it, so a failing run
	// shows what the prompt actually became.
	prompt := reqs[0].Instructions

	if !strings.Contains(prompt, wantInstr) {
		t.Errorf("server instructions missing from the system prompt.\nserver sent: %q\ngot prompt:\n%s", wantInstr, prompt)
	}
	// The origin label: the host-assigned server id, not the server's
	// self-reported name ("instr-srv"), per the same rule as skill blocks.
	if !strings.Contains(prompt, `"test"`) {
		t.Errorf("prompt does not label the instructions with their origin \"test\".\ngot prompt:\n%s", prompt)
	}
	// Framing: server text must not read as higher-authority host instructions.
	if !strings.Contains(prompt, "untrusted") {
		t.Errorf("prompt does not frame server instructions as untrusted server-provided data.\ngot prompt:\n%s", prompt)
	}
	// The base host instructions stay the first thing the model reads.
	if !strings.HasPrefix(prompt, "base") {
		t.Errorf("host base instructions must remain first.\ngot prompt:\n%s", prompt)
	}
}

// TestServerWithoutInstructionsLeavesPromptUnchanged is the other half of #76:
// a connected server that sends no instructions adds no header and no gap.
func TestServerWithoutInstructionsLeavesPromptUnchanged(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "plain-srv", Version: "0.0.1"})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	cfg := testConfig(ts.URL)
	cfg.Instructions = "base"

	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	app, err := NewApp(cfg, &syncWriter{}, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if got := stub.Requests()[0].Instructions; got != "base" {
		t.Fatalf("prompt = %q, want exactly %q", got, "base")
	}
}
