// Command e2eserver serves agentweb's page and API for the Playwright checks in
// web/e2e. Chat has two connections, each over a StubProvider, so the header's
// model picker has a choice to show. A "coding" profile is served at /code/
// over a stub as well. Its first turn starts a fake tty process, which prints
// colours and a redrawn progress line, for the terminal view (#218). Nothing
// needs a key or a network.
//
// An "element" profile backs the <chakra-chat> checks (#314). Its one turn
// asks to run a lookup tool, which needs approval, then thinks and answers.
// With -pages-addr, the server also serves web/e2e/pages on a second port.
// So those pages load the element and call the API across origins, the way a
// page from another app does.
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
	"github.com/panyam/servicekit/middleware"
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

// lookupTool offers the element profile's one tool.
type lookupTool struct{ host.BaseExtension }

func (lookupTool) Name() string { return "e2e-lookup" }

func (lookupTool) Tools() (agent.ToolSource, error) {
	src := agent.NewFuncSource()
	def := core.ToolDef{Name: "lookup", Description: "looks something up", InputSchema: map[string]any{"type": "object"}}
	err := src.AddToolFunc(def, func(context.Context, map[string]any) (*core.ToolResult, error) {
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "42"}}}, nil
	})
	return src, err
}

func elementApp(context.Context, string) (*host.App, error) {
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "element", Model: "stub-element"}, Approval: &host.ApprovalConfig{Mode: "ask"}}
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "l1", Name: "lookup", Args: core.NewRawJSON([]byte(`{}`))}}},
		agent.StubTurn{Deltas: []agent.Delta{
			{Kind: agent.DeltaReasoning, Text: "the tool said 42"},
			{Kind: agent.DeltaText, Text: "The answer is 42."},
		}},
	)
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(stub), host.WithExtension(lookupTool{}),
		host.WithElicitationUI(awaitSurface))
}

// awaitSurface never answers an ask, so the browser's RespondToAsk does, as
// in agentweb. The default terminal responder reads empty input and declines.
func awaitSurface(ctx context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
	<-ctx.Done()
	return core.ElicitationResult{}, ctx.Err()
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "address to listen on")
	pagesAddr := flag.String("pages-addr", "", "address to serve web/e2e/pages on, a second origin for the <chakra-chat> checks")
	flag.Parse()

	mgr := web.NewSessionManager(func(context.Context) (*host.App, error) { return chatApp() })
	def, err := chatApp()
	if err != nil {
		log.Fatal(err)
	}
	mgr.SetDefault(def)
	defer mgr.CloseAll()
	h := web.HandlerWithSessions(mgr,
		web.WithProfile(web.Profile{Name: "coding", Path: "/code/", Build: codingApp}),
		web.WithProfile(web.Profile{Name: "element", Build: elementApp}))
	// agentweb's CORS layer, so the pages on the second origin can call in.
	h = middleware.CORS(middleware.NewOriginChecker([]string{"localhost"}),
		middleware.CORSAllowHeaders("Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms"))(h)
	if *pagesAddr != "" {
		go func() {
			log.Printf("e2eserver: pages on http://%s/", *pagesAddr)
			log.Fatal(http.ListenAndServe(*pagesAddr, http.FileServer(http.Dir("web/e2e/pages"))))
		}()
	}
	log.Printf("e2eserver: listening on http://%s/", *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}
