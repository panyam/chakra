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

// A tool a server adds after connect reaches the model's tool list on either
// wire. The session wire delivers tools/list_changed on the GET stream; the
// stateless wire only on a subscriptions/listen stream, which the host didn't
// open before #112, so a stateless server's new tool stayed invisible.
func TestToolAddedAfterConnectIsOffered(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "tools-lc", Version: "0.0.1"})
	registerSearch(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	app, err := NewApp(testConfig(ts.URL), &strings.Builder{}, strings.NewReader(""), WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	offers := func(name string) func() bool {
		return func() bool {
			tools, err := app.sources.Tools(context.Background())
			return err == nil && hasTool(tools, name)
		}
	}
	if !eventually(offers("search")) {
		t.Fatal("the server's first tool never appeared")
	}
	srv.RegisterTool(core.ToolDef{Name: "translate", Description: "translates", InputSchema: map[string]any{"type": "object"}},
		func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult("ok"), nil })
	if !eventually(offers("translate")) {
		t.Fatal("a tool added after connect never reached the tool list")
	}
}
