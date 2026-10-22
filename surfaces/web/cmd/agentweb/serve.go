package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	skhttp "github.com/panyam/servicekit/http"
	"github.com/panyam/servicekit/middleware"
)

// drainTimeout bounds how long shutdown waits for handlers once their contexts
// are cancelled. A Watch or Submit returns as soon as its context ends, so this
// only matters for a handler that ignores its context; past it, connections
// are closed outright.
const drainTimeout = 3 * time.Second

// connectPrefix is the path every HostService RPC is served under.
const connectPrefix = "/chakra.web.v1.HostService/"

// serve runs handler on ln until ctx ends or the process gets SIGINT/SIGTERM.
//
// http.Server.Shutdown waits for handlers but never cancels their contexts, so
// an open Watch stream (every browser tab has one) or a running Submit would
// hold shutdown for the whole drain. Every request context here derives from
// one base context, which shutdown cancels before draining.
func serve(ctx context.Context, ln net.Listener, handler http.Handler) error {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv := &http.Server{
		Handler:     handler,
		BaseContext: func(net.Listener) context.Context { return base },
	}
	middleware.ApplyDefaults(srv)
	// Watch streams for as long as a tab is open and Submit lasts a whole turn,
	// so a write deadline would cut both off mid-response.
	srv.WriteTimeout = 0

	err := skhttp.ListenAndServeGraceful(srv,
		skhttp.WithListener(ln),
		skhttp.WithContext(ctx),
		skhttp.WithOnShutdown(cancelBase),
		skhttp.WithDrainTimeout(drainTimeout),
	)
	if errors.Is(err, context.DeadlineExceeded) {
		log.Printf("agentweb: handlers still running after %s, closing their connections", drainTimeout)
		return srv.Close()
	}
	return err
}

// originGuard rejects HostService calls from a browser page on another site.
// Without it, any page the user visits could drive the agent through
// localhost, and so run its tools. Calls are allowed when the Origin header is
// in the allowlist, or, with no Origin (curl, a script), when the Host is.
// Checking the Host too is what stops DNS rebinding, where a hostile name
// resolves to 127.0.0.1 and the browser treats the page as same-origin.
//
// servicekit's CheckRequest does the Origin half, but with no Origin it only
// accepts a localhost Host, which would lock out a phone on the LAN even when
// its address is allowed. Only the Connect API is guarded: the shell and its
// static assets are public and change nothing.
func originGuard(checker *middleware.OriginChecker, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, connectPrefix) && !originAllowed(checker, r) {
			log.Printf("agentweb: rejected %s from origin %q host %q", r.URL.Path, r.Header.Get("Origin"), r.Host)
			http.Error(w, "origin not allowed; start agentweb with --allow-origin to permit it", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originAllowed(checker *middleware.OriginChecker, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return checker.Check(origin)
	}
	return r.Host == "" || checker.Check("http://"+r.Host)
}

// newOriginChecker builds the allowlist from --allow-origin. localhost is
// always on it, since the page agentweb serves to itself is the main client.
func newOriginChecker(flagValue string) *middleware.OriginChecker {
	origins := []string{"localhost"}
	for _, o := range strings.Split(flagValue, ",") {
		if o = strings.TrimSpace(o); o != "" && o != "*" {
			origins = append(origins, o)
		}
	}
	return middleware.NewOriginChecker(origins)
}
