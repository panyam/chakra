// Command chat_element_rooms_e2e serves the two origins the <chakra-chat>
// room checks need (#324), for web/e2e/chat-rooms.e2e.ts in surfaces/web.
//
// The app origin plays a web app of its own. It serves a page, a massrelay
// relay, a rooms join endpoint and a rooms bridge at /mcp, the way
// mcpkit's examples/rooms/lanternfall does. The agentweb origin serves the
// HostService API and the element's bundle. Its "rooms" profile builds an
// agent whose server is the app's bridge, attached to the page's room with
// onConnect. The agent's stub turn calls the page's highlight tool, then
// asks the user through the element's ask_yes_no, then answers.
//
// It lives in its own module because the relay pulls in massrelay, which no
// chakra module needs (CONSTRAINTS A10).
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net/http"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	web "github.com/panyam/chakra/surfaces/web"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

// room is the one room the page joins and the agent attaches to. The room
// comes from config on both sides, so one room serves every session.
const room = "demo:page"

// subject is the single local user. The page joins as it, and the bridge's
// local mode makes the agent act for it, so the page's switch is the only
// step between them.
const subject = "me"

func appHandler(static string) (http.Handler, func(), error) {
	local, err := massrelay.NewLocal(massrelay.LocalOptions{Origins: []string{"localhost", "127.0.0.1"}})
	if err != nil {
		return nil, nil, err
	}
	bridge := rooms.New(local.Transport(), rooms.WithLocalSubject(subject))
	srv := server.NewServer(core.ServerInfo{Name: "rooms-e2e", Version: "0.1.0"})
	bridge.Register(srv)

	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(server.WithStreamableHTTP(true)))
	mux.Handle("/relay/", http.StripPrefix("/relay", local.Relay()))
	mux.Handle("POST /rooms/join", local.JoinHandler(rooms.JoinOptions{
		Subject:  func(*http.Request) (string, error) { return subject, nil },
		RoomOf:   func(*http.Request) (string, error) { return room, nil },
		RelayURL: "/relay",
	}))
	mux.Handle("/", http.FileServer(http.Dir(static)))
	return mux, func() { bridge.Close(); local.Close() }, nil
}

// awaitSurface never answers an ask, so the browser does, as in agentweb.
func awaitSurface(ctx context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
	<-ctx.Done()
	return core.ElicitationResult{}, ctx.Err()
}

func roomsApp(appURL string) func(context.Context, string) (*host.App, error) {
	return func(context.Context, string) (*host.App, error) {
		cfg := &host.Config{
			Model:    host.ModelConfig{BaseURL: "rooms", Model: "stub-rooms"},
			Approval: &host.ApprovalConfig{Mode: "allow"},
			Servers: []host.ServerConfig{{
				ID:        "app",
				URL:       appURL + "/mcp",
				Required:  true,
				OnConnect: []host.ToolCallConfig{{Name: "attach", Arguments: map[string]any{"resource": room}}},
			}},
		}
		call := func(id, name, args string) agent.StubTurn {
			return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: id, Name: name, Args: core.NewRawJSON([]byte(args))}}}
		}
		stub := agent.NewStubProvider(
			call("h1", "highlight", `{"text":"hello from the agent"}`),
			call("a1", "ask_yes_no", `{"question":"Keep the highlight?"}`),
			agent.StubTurn{Text: "Kept it."},
		)
		return host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(stub), host.WithElicitationUI(awaitSurface))
	}
}

func main() {
	appAddr := flag.String("app-addr", "127.0.0.1:8097", "the web app's address: page, relay, join endpoint and bridge")
	agentAddr := flag.String("agent-addr", "127.0.0.1:8096", "agentweb's address: HostService and the element bundle")
	static := flag.String("static", "static", "the app's page directory")
	flag.Parse()

	app, closeApp, err := appHandler(*static)
	if err != nil {
		log.Fatal(err)
	}
	defer closeApp()

	mgr := web.NewSessionManager(func(context.Context) (*host.App, error) {
		return nil, context.Canceled
	})
	defer mgr.CloseAll()
	h := web.HandlerWithSessions(mgr, web.WithProfile(web.Profile{Name: "rooms", Build: roomsApp("http://" + *appAddr)}))
	h = middleware.CORS(middleware.NewOriginChecker([]string{"localhost"}),
		middleware.CORSAllowHeaders("Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms"))(h)

	go func() {
		log.Printf("rooms e2e: agentweb on http://%s/", *agentAddr)
		log.Fatal(http.ListenAndServe(*agentAddr, h))
	}()
	log.Printf("rooms e2e: app on http://%s/", *appAddr)
	log.Fatal(http.ListenAndServe(*appAddr, app))
}
