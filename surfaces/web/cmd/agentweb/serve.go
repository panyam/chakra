package main

import (
	"context"
	"crypto/subtle"
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
// servicekit's graceful shutdown cancels every request context before it
// drains, so an open Watch (every browser tab has one) or a running Submit
// returns at once instead of holding the drain.
func serve(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{Handler: handler}
	middleware.ApplyDefaults(srv)
	// Watch streams for as long as a tab is open and Submit lasts a whole turn,
	// so a write deadline would cut both off mid-response.
	srv.WriteTimeout = 0

	err := skhttp.ListenAndServeGraceful(srv,
		skhttp.WithListener(ln),
		skhttp.WithContext(ctx),
		skhttp.WithDrainTimeout(drainTimeout),
	)
	if errors.Is(err, skhttp.ErrDrainTimeout) {
		// servicekit has already closed what was left; a slow handler is not a
		// reason to exit non-zero.
		log.Printf("agentweb: handlers still running after %s, their connections were closed", drainTimeout)
		return nil
	}
	return err
}

// tokenHeader carries the access token on every HostService call.
const tokenHeader = "X-Agentweb-Token"

// tokenGuard requires token in tokenHeader on every HostService call, when a
// token is set. The origin guard only stops other sites' pages; a script can
// send any Origin it likes, so a server anyone can reach needs this too. The
// shell and static assets stay public: the page has to load before it can ask
// for the token. An empty token disables the guard.
func tokenGuard(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, connectPrefix) &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), want) != 1 {
			// Connect clients read the code from this JSON body.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"missing or wrong access token"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originGuard rejects HostService calls from a browser page on another site.
// Without it, any page the user visits could drive the agent through
// localhost, and so run its tools. servicekit's CheckRequest allows a call
// when its Origin is on the allowlist, or, with no Origin (curl, a script),
// when its Host is, which is what stops DNS rebinding as well. Only the
// Connect API is guarded: the shell and its static assets are public and
// change nothing, so a phone can load the page and only needs --allow-origin
// for the calls.
func originGuard(checker *middleware.OriginChecker, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, connectPrefix) && !checker.CheckRequest(r) {
			log.Printf("agentweb: rejected %s from origin %q host %q", r.URL.Path, r.Header.Get("Origin"), r.Host)
			http.Error(w, "origin not allowed; start agentweb with --allow-origin to permit it", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
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
