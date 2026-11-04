// Command e2eserver serves agentweb's page and API for the Playwright checks in
// web/e2e. Chat has two connections, each over a StubProvider, so the header's
// model picker has a choice to show. A "coding" profile is served at /code/
// over a stub as well. Its first turn starts a fake tty process, which prints
// colours and a redrawn progress line, for the terminal view (#218). Nothing
// needs a key or a network.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	web "github.com/panyam/chakra/surfaces/web"
	"github.com/panyam/mcpkit/core"
)

func chatApp() (*host.App, error) {
	cfg := &host.Config{Connections: &host.ConnectionsConfig{
		Active: "local",
		Connections: map[string]host.ConnectionConfig{
			"local":    {Type: "lmstudio", Model: "qwen"},
			"deepseek": {Type: "openai", Model: "deepseek-chat"},
		},
	}}
	build := func(host.ConnectionConfig) (agent.Provider, error) { return agent.NewStubProvider(), nil }
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProviderBuilder(build))
}

// ttyText is what the fake process prints: a green word, then a progress line
// redrawn in place with a carriage return.
const ttyText = "\x1b[32mgreen-ok\x1b[0m\r\nprogress 10%\rprogress 100%\r\n"

// fakeProcs offers run_demo, which "starts" process p1 and reports its output
// the way ext/exec reports a tty process. p1 keeps running, and it echoes
// what the terminal view types and how it resizes the terminal.
type fakeProcs struct {
	host.BaseExtension
	mu     sync.Mutex
	report func(host.ProcessOutput)
}

func (f *fakeProcs) Name() string { return "e2e-procs" }

func (f *fakeProcs) SetProcessOutput(report func(host.ProcessOutput)) {
	f.mu.Lock()
	f.report = report
	f.mu.Unlock()
}

func (f *fakeProcs) out(text string) {
	f.mu.Lock()
	report := f.report
	f.mu.Unlock()
	if report != nil {
		report(host.ProcessOutput{Source: "e2e-procs", ID: "p1", Command: "demo", Text: text, TTY: true, Cols: 80, Rows: 6})
	}
}

// ResizeProcess implements host.ProcessController.
func (f *fakeProcs) ResizeProcess(id string, cols, rows int) error {
	f.out(fmt.Sprintf("\r\nresized to %dx%d\r\n", cols, rows))
	return nil
}

// WriteProcess implements host.ProcessController.
func (f *fakeProcs) WriteProcess(id, data string) error {
	f.out("\r\ntyped: " + data + "\r\n")
	return nil
}

func (f *fakeProcs) Tools() (agent.ToolSource, error) {
	src := agent.NewFuncSource()
	def := core.ToolDef{Name: "run_demo", Description: "starts a demo process", InputSchema: map[string]any{"type": "object"}}
	err := src.AddToolFunc(def, func(context.Context, map[string]any) (*core.ToolResult, error) {
		f.out(ttyText)
		return &core.ToolResult{
			Content:           []core.Content{{Type: "text", Text: "$ demo"}},
			StructuredContent: map[string]any{"session_id": "p1", "running": true},
		}, nil
	})
	return src, err
}

func codingApp(context.Context, string) (*host.App, error) {
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "coding", Model: "stub-coder"}}
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "run_demo", Args: core.NewRawJSON([]byte(`{}`))}}},
		agent.StubTurn{Text: "demo done"},
	)
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(stub), host.WithExtension(&fakeProcs{}))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "address to listen on")
	flag.Parse()

	mgr := web.NewSessionManager(func(context.Context) (*host.App, error) { return chatApp() })
	def, err := chatApp()
	if err != nil {
		log.Fatal(err)
	}
	mgr.SetDefault(def)
	defer mgr.CloseAll()
	h := web.HandlerWithSessions(mgr, web.WithProfile(web.Profile{Name: "coding", Path: "/code/", Build: codingApp}))
	log.Printf("e2eserver: listening on http://%s/", *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}
