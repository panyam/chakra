package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// startServe runs serve on a loopback port with handler and returns its base
// URL, a cancel that triggers shutdown, and a channel carrying serve's result.
func startServe(t *testing.T, handler http.Handler) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, handler) }()
	t.Cleanup(cancel)
	return "http://" + ln.Addr().String(), cancel, done
}

// openRequest starts a GET whose handler blocks, and returns once the handler
// has the request.
func openRequest(t *testing.T, url string, started <-chan struct{}) {
	t.Helper()
	go func() {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never got the request")
	}
}

func TestServe_ShutdownCancelsOpenStreams(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	url, stop, done := startServe(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	openRequest(t, url, started)

	t0 := time.Now()
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(drainTimeout + 5*time.Second):
		t.Fatal("serve did not return")
	}
	if took := time.Since(t0); took > time.Second {
		t.Errorf("shutdown took %s with a stream open; the stream's context should end at once", took)
	}
	select {
	case <-cancelled:
	default:
		t.Error("the open request's context was never cancelled")
	}
}

func TestServe_ClosesHandlersThatIgnoreTheirContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	url, stop, done := startServe(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	openRequest(t, url, started)

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v; past the drain it should close connections and return cleanly", err)
		}
	case <-time.After(drainTimeout + 5*time.Second):
		t.Fatal("serve did not return after the drain timeout")
	}
}

func TestOriginGuard(t *testing.T) {
	const api = connectPrefix + "Submit"
	guard := originGuard(newOriginChecker("192.168.1.5"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, tc := range []struct {
		name, path, origin, host string
		want                     int
	}{
		{"own page on localhost", api, "http://localhost:8090", "localhost:8090", http.StatusOK},
		{"own page on 127.0.0.1", api, "http://127.0.0.1:8090", "127.0.0.1:8090", http.StatusOK},
		{"another site", api, "https://evil.example", "localhost:8090", http.StatusForbidden},
		{"curl on localhost", api, "", "localhost:8090", http.StatusOK},
		{"DNS rebinding, no origin", api, "", "evil.example:8090", http.StatusForbidden},
		{"allowed LAN page", api, "http://192.168.1.5:8090", "192.168.1.5:8090", http.StatusOK},
		{"allowed LAN host, no origin", api, "", "192.168.1.5:8090", http.StatusOK},
		{"shell is not guarded", "/", "https://evil.example", "evil.example:8090", http.StatusOK},
		{"static is not guarded", "/static/app.js", "", "evil.example:8090", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.path, nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// A page on an allowed origin gets CORS headers and its preflight is
// answered before the token guard. Another site gets none (#314).
func TestCORSFollowsTheOriginGuard(t *testing.T) {
	checker := newOriginChecker("")
	h := corsFor(checker, originGuard(checker, tokenGuard("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))))
	for _, tc := range []struct {
		name, method, origin string
		wantCode             int
		wantAllow            string
	}{
		{"preflight from a localhost page", "OPTIONS", "http://localhost:5173", http.StatusNoContent, "http://localhost:5173"},
		{"preflight from another site", "OPTIONS", "https://evil.example", http.StatusNoContent, ""},
		{"call from another site", "POST", "https://evil.example", http.StatusForbidden, ""},
		{"call from a localhost page without the token", "POST", "http://127.0.0.1:5173", http.StatusUnauthorized, "http://127.0.0.1:5173"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, connectPrefix+"Submit", nil)
			req.Host = "localhost:8090"
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("status %d, want %d", rec.Code, tc.wantCode)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllow {
				t.Errorf("Access-Control-Allow-Origin %q, want %q", got, tc.wantAllow)
			}
			if tc.wantAllow != "" && !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), tokenHeader) {
				t.Errorf("allowed headers %q must include %s", rec.Header().Get("Access-Control-Allow-Headers"), tokenHeader)
			}
		})
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"localhost:8090": true, "127.0.0.1:8090": true, "[::1]:8090": true,
		":8090": false, "0.0.0.0:8090": false, "192.168.1.5:8090": false,
	} {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
