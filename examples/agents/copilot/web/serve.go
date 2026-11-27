// Command web serves copilot's kanban board as a real page (#397). A person
// drags cards in a browser while the co-pilot works on the same board.
//
// It serves two origins, the way tests/chat_element_rooms_e2e does. The app
// origin plays the kanban app: the board page, a massrelay relay, a rooms
// join endpoint, and a rooms bridge at /mcp with roomsevents on it. The page
// holds the board, offers the agent its tools through the room, and emits
// card.moved and card.renamed for each user action. The agentweb origin
// serves the HostService API and the <chakra-chat> bundle, with a "copilot"
// profile. That profile's agent attaches to the page's room, hears the
// events, and runs the done-check trigger on a card moved to Done.
//
// It lives in its own module because the relay pulls in massrelay, which no
// other chakra module needs (CONSTRAINTS A10).
package main

import (
	"context"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/examples/agents/copilot"
	"github.com/panyam/chakra/host"
	web "github.com/panyam/chakra/surfaces/web"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay"
	"github.com/panyam/mcpkit/experimental/integrations/roomsevents"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

//go:embed static
var static embed.FS

// room is the one room the page joins and the agent attaches to. Every
// browser tab and every agent session share it, so the demo has one board.
const room = "kanban:board"

// subject is the single local user. The page joins as it, and the bridge's
// local mode makes the agent act for it.
const subject = "me"

// critical is the hint the app gives both event types. The host passes a
// critical event to the agent before its next model call, mid-turn
// included, so a card the user moves while the agent works reaches it
// before its next edit. The templates match copilot's board.go.
func critical(template string) map[string]any {
	return map[string]any{"priority": "critical", "template": template}
}

func str() map[string]any { return map[string]any{"type": "string"} }

// eventCatalog fixes the two event types the page emits. The app declares
// them up front, so the agent's host can subscribe as soon as it connects,
// before any page has joined.
var eventCatalog = []rooms.PageEventDecl{
	{
		Name:        "card.moved",
		Description: "The user moved a card to another column.",
		PayloadSchema: map[string]any{"type": "object", "required": []any{"card", "title", "from", "to"},
			"properties": map[string]any{"card": str(), "title": str(), "from": str(), "to": str()}},
		ContextHint: critical("The user moved {{card}} ({{title}}) from {{from}} to {{to}}."),
	},
	{
		Name:        "card.renamed",
		Description: "The user renamed a card.",
		PayloadSchema: map[string]any{"type": "object", "required": []any{"card", "from", "to"},
			"properties": map[string]any{"card": str(), "from": str(), "to": str()}},
		ContextHint: critical(`The user renamed {{card}} from "{{from}}" to "{{to}}".`),
	},
}

// appServer is the kanban app's origin.
type appServer struct {
	http.Handler
	local *massrelay.Local
	close func()
}

// newApp builds the app origin. agentURL is where the page loads
// <chakra-chat> from and points it at. origins are the hosts whose pages may
// open the relay's WebSocket, and empty allows any.
func newApp(agentURL string, origins []string) (*appServer, error) {
	local, err := massrelay.NewLocal(massrelay.LocalOptions{Origins: origins})
	if err != nil {
		return nil, err
	}
	ev := roomsevents.New()
	bridge := rooms.New(local.Transport(), rooms.WithLocalSubject(subject),
		rooms.WithPageEventCatalog(eventCatalog), rooms.WithPageEvents(ev.Publish))
	srv := server.NewServer(core.ServerInfo{Name: "kanban", Version: "0.1.0"})
	bridge.Register(srv)
	// The bridge runs in local mode with no auth, and events/stream refuses
	// an unauthenticated caller, so the local user stands in.
	ev.Register(srv, bridge, events.Config{UnsafeAnonymousPrincipal: subject})

	pages, err := fs.Sub(static, "static")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(pages, "index.html")
	if err != nil {
		return nil, err
	}
	page := strings.ReplaceAll(string(index), "{{AGENT}}", agentURL)

	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(server.WithStreamableHTTP(true)))
	mux.Handle("/relay/", http.StripPrefix("/relay", local.Relay()))
	mux.Handle("POST /rooms/join", local.JoinHandler(rooms.JoinOptions{
		Subject:  func(*http.Request) (string, error) { return subject, nil },
		RoomOf:   func(*http.Request) (string, error) { return room, nil },
		RelayURL: "/relay",
	}))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
	mux.Handle("/", http.FileServerFS(pages))
	return &appServer{Handler: mux, local: local, close: func() { bridge.Close(); local.Close() }}, nil
}

// hostConfig is the copilot profile's config: copilot's instructions and
// page rules, the app's bridge as a server attached to the board's room,
// both event types subscribed, and the done-check trigger.
func hostConfig(appURL string) *host.Config {
	resource := map[string]any{"resource": room}
	return &host.Config{
		Instructions: copilot.Instructions + "\n" + copilot.PageRules,
		MaxSteps:     12,
		Approval:     &host.ApprovalConfig{Mode: "allow"},
		Servers: []host.ServerConfig{{
			ID:        "app",
			URL:       appURL + "/mcp",
			Required:  true,
			OnConnect: []host.ToolCallConfig{{Name: "attach", Arguments: resource}},
			Events: []host.EventConfig{
				{Name: "card.moved", Arguments: resource},
				{Name: "card.renamed", Arguments: resource},
			},
		}},
		Triggers: []host.TriggerConfig{{Server: "app", Event: "card.moved", Filter: map[string]string{"to": "Done"},
			Instructions: copilot.DoneCheck, Label: "done-check"}},
	}
}

// awaitSurface never answers an ask, so the browser does, as in agentweb.
func awaitSurface(ctx context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
	<-ctx.Done()
	return core.ElicitationResult{}, ctx.Err()
}

// buildApp is one copilot session: an App from hostConfig on provider p.
func buildApp(appURL string, p agent.Provider, opts ...host.AppOption) (*host.App, error) {
	all := append([]host.AppOption{host.WithProvider(p), host.WithElicitationUI(awaitSurface)}, opts...)
	return host.NewApp(hostConfig(appURL), io.Discard, strings.NewReader(""), all...)
}

// newAgent builds the agentweb origin. Each session the page creates is a
// fresh App from buildApp.
func newAgent(appURL string, p agent.Provider) (http.Handler, func()) {
	build := func(context.Context, string) (*host.App, error) { return buildApp(appURL, p) }
	mgr := web.NewSessionManager(func(context.Context) (*host.App, error) {
		return nil, context.Canceled
	})
	h := web.HandlerWithSessions(mgr, web.WithProfile(web.Profile{Name: "copilot", Build: build}))
	h = middleware.CORS(middleware.NewOriginChecker([]string{"localhost", "127.0.0.1"}),
		middleware.CORSAllowHeaders("Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms"))(h)
	return h, mgr.CloseAll
}
