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
// only matters for a handler that ignores its context. After the timeout,
// the remaining connections are closed outright.
const drainTimeout = 3 * time.Second

// connectPrefix is the path every HostService RPC is served under.
const connectPrefix = "/chakra.web.v1.HostService/"

// serve runs handler on ln until ctx ends or the process gets SIGINT/SIGTERM.
// servicekit's graceful shutdown cancels every request context before it
// drains. So an open Watch returns at once instead of holding up the drain,
// and so does a running Submit. Every browser tab has an open Watch.
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
// shell and static assets are still public, because the page has to load
// before it can ask for the token. An empty token disables the guard.
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
// when its Origin is on the allowlist. A call with no Origin, such as one
// from curl or a script, passes when its Host is on the allowlist. The Host
// check stops DNS rebinding as well. The guard covers only the Connect API.
// The shell and its static assets are public and change nothing. So a phone
// can load the page, and it needs --allow-origin only for the calls.
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

// corsFor lets a page on an allowed origin call the API and load the static
// bundle across origins, as the <chakra-chat> element does from a page that
// agentweb didn't serve (#314). It uses the origin guard's own allowlist, so
// it widens nothing the guard refuses. It answers a preflight itself, before
// the token guard, because a browser sends no custom headers on a preflight.
// A call from an origin off the list gets no CORS headers, so the browser
// blocks the response, and the origin guard still refuses the call.
func corsFor(checker *middleware.OriginChecker, next http.Handler) http.Handler {
	return middleware.CORS(checker,
		middleware.CORSAllowMethods("GET", "POST", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms", tokenHeader),
		middleware.CORSExposeHeaders("Grpc-Status", "Grpc-Message"),
	)(next)
}

// newOriginChecker builds the allowlist from --allow-origin. localhost is
// always on it, because the main client is agentweb's own page loaded from
// localhost.
func newOriginChecker(flagValue string) *middleware.OriginChecker {
	origins := []string{"localhost"}
	for _, o := range strings.Split(flagValue, ",") {
		if o = strings.TrimSpace(o); o != "" && o != "*" {
			origins = append(origins, o)
		}
	}
	return middleware.NewOriginChecker(origins)
}
