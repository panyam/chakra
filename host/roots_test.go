package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// rootsServer serves workspace_roots. The tool asks the client for roots/list
// through SEP-2322 input, the one way to ask on both wires. It returns the
// URIs one per line, or "no roots capability" for a client that declared none.
// pushed records the result of the server's own list_changed fetch. Only the
// session wire can deliver that fetch.
type rootsServer struct {
	mu     sync.Mutex
	pushed [][]core.Root
	url    string
}

func newRootsServer(t *testing.T) *rootsServer {
	t.Helper()
	rs := &rootsServer{}
	srv := server.NewServer(core.ServerInfo{Name: "roots", Version: "0.0.1"},
		server.WithOnRootsChanged(func(r []core.Root) {
			rs.mu.Lock()
			rs.pushed = append(rs.pushed, r)
			rs.mu.Unlock()
		}))
	srv.RegisterTool(core.ToolDef{Name: "workspace_roots", Description: "lists the client's roots", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			if caps := ctx.ClientCaps(); caps == nil || caps.Roots == nil {
				return core.TextResult("no roots capability"), nil
			}
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{"roots": core.NewListRootsInputRequest()})
			}
			res, err := core.DecodeListRootsInputResponse(ctx.InputResponse("roots"))
			if err != nil {
				return nil, err
			}
			return core.TextResult(rootURIs(res.Roots)), nil
		})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	rs.url = ts.URL
	return rs
}

func (rs *rootsServer) lastPushed() []core.Root {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.pushed) == 0 {
		return nil
	}
	return rs.pushed[len(rs.pushed)-1]
}

func rootURIs(roots []core.Root) string {
	var uris []string
	for _, r := range roots {
		uris = append(uris, r.URI)
	}
	return strings.Join(uris, "\n")
}

func fileURI(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

func rootsApp(t *testing.T, rs *rootsServer, cfgRoots []string, opts ...AppOption) *App {
	t.Helper()
	cfg := testConfig(rs.url)
	cfg.Roots = cfgRoots
	app, err := NewApp(cfg, io.Discard, strings.NewReader(""), append([]AppOption{WithProvider(agent.NewStubProvider())}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if !eventually(func() bool {
		tools, err := app.sources.Tools(context.Background())
		return err == nil && hasTool(tools, "workspace_roots")
	}) {
		t.Fatal("workspace_roots never offered")
	}
	return app
}

func askRoots(t *testing.T, app *App) string {
	t.Helper()
	res, err := app.sources.Call(context.Background(), "workspace_roots", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// roots/list answers with Config.Roots and then WithRoots, as file:// URIs.
func TestServerSeesWorkspaceRoots(t *testing.T) {
	rs := newRootsServer(t)
	a, b := t.TempDir(), t.TempDir()
	app := rootsApp(t, rs, []string{a}, WithRoots(b))

	want := fileURI(t, a) + "\n" + fileURI(t, b)
	if got := askRoots(t, app); got != want {
		t.Fatalf("roots/list = %q, want %q", got, want)
	}
}

func TestNoRootsAdvertisesNoCapability(t *testing.T) {
	rs := newRootsServer(t)
	app := rootsApp(t, rs, nil)
	if got := askRoots(t, app); got != "no roots capability" {
		t.Fatalf("server saw %q, want a client with no roots capability", got)
	}
	if err := app.SetRoots(context.Background(), t.TempDir()); err == nil {
		t.Fatal("SetRoots on a rootless host succeeded; the capability can't be added after initialize")
	}
}

// After SetRoots the next roots/list answers the new set, on either wire.
func TestSetRootsChangesTheNextList(t *testing.T) {
	rs := newRootsServer(t)
	a, b := t.TempDir(), t.TempDir()
	app := rootsApp(t, rs, []string{a})
	if err := app.SetRoots(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got, want := askRoots(t, app), fileURI(t, b); got != want {
		t.Fatalf("roots/list after SetRoots = %q, want %q", got, want)
	}
}

// SetRoots sends notifications/roots/list_changed. The server then fetches
// roots/list itself and sees the new set. This runs on the session wire only,
// because the stateless wire has no server-to-client request outside a tool
// call.
func TestSetRootsNotifiesTheServer(t *testing.T) {
	skipOnStateless(t, "a server-initiated roots/list after list_changed")
	rs := newRootsServer(t)
	a, b := t.TempDir(), t.TempDir()
	app := rootsApp(t, rs, []string{a})
	if err := app.SetRoots(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	want := fileURI(t, b)
	if !eventually(func() bool { return rootURIs(rs.lastPushed()) == want }) {
		got, _ := json.Marshal(rs.lastPushed())
		t.Fatalf("server's fetch after list_changed saw %s, want %q", got, want)
	}
}
