package web

import (
	"net/http"

	"github.com/panyam/chakra/host"
	"github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1/webv1connect"
)

// Handler builds the one mux the web surface serves. It follows the Agni /
// diffpp serve pattern. The HostService Connect handlers sit under their proto
// service path, the frontend bundle under /static/, and a server-rendered
// shell at / catches the rest. The shell loads the DockView + Solid frontend
// (E4, issue 1197) from /static, and that frontend consumes the Connect
// surface here.
//
// Connect server-streaming (Watch) uses the Connect protocol, which supports
// server streams over HTTP/1.1. So local dev needs no h2c, and a production
// deployment behind HTTP/2 works unchanged.
func Handler(app *host.App) http.Handler {
	return handlerFor(NewHostService(app))
}

// HandlerWithSessions is Handler for a multi-session server. It serves the same
// mux over a SessionManager, so one server hosts many concurrent conversations.
// An empty session_id routes to the manager's default session, and
// CreateSession creates more.
func HandlerWithSessions(mgr *SessionManager, opts ...Option) http.Handler {
	return handlerFor(NewHostServiceWithSessions(mgr, opts...))
}

func handlerFor(svc *HostService) http.Handler {
	mux := http.NewServeMux()

	path, h := webv1connect.NewHostServiceHandler(svc)
	mux.Handle(path, h)

	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticAssets()))))
	mux.HandleFunc("/", shellHandler)
	return mux
}
