package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/panyam/chakra/host"
	web "github.com/panyam/chakra/surfaces/web"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
	"github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1/webv1connect"
)

// byokServer serves a --byok HostService behind the token guard, the way main
// wires it, and returns a client that sends token.
func byokServer(t *testing.T, serverToken, clientToken string) webv1connect.HostServiceClient {
	t.Helper()
	mgr := web.NewSessionManager(nil)
	t.Cleanup(mgr.CloseAll)
	opts := appOptions{}
	h := tokenGuard(serverToken, web.HandlerWithSessions(mgr,
		web.WithSessionBuilder(func(ctx context.Context, spec *webv1.ProviderSpec) (*host.App, error) {
			return buildSessionApp(opts, spec)
		}),
		web.WithPrivateSessions()))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return webv1connect.NewHostServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(tokenInterceptor(clientToken)))
}

func tokenInterceptor(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token != "" {
				req.Header().Set(tokenHeader, token)
			}
			return next(ctx, req)
		}
	}
}

func codeOf(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeUnknown
}

// Without the token, or with the wrong one, every API call is refused; the
// shell page still loads.
func TestTokenGuard(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, token string
		want        connect.Code
	}{
		{"missing", "", connect.CodeUnauthenticated},
		{"wrong", "nope", connect.CodeUnauthenticated},
	} {
		c := byokServer(t, "s3cret", tc.token)
		_, err := c.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Provider: &webv1.ProviderSpec{Type: "demo"}}))
		if codeOf(err) != tc.want {
			t.Errorf("%s token: CreateSession err = %v, want %v", tc.name, err, tc.want)
		}
	}
	c := byokServer(t, "s3cret", "s3cret")
	if _, err := c.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Provider: &webv1.ProviderSpec{Type: "demo"}})); err != nil {
		t.Fatalf("right token: CreateSession: %v", err)
	}

	rec := httptest.NewRecorder()
	tokenGuard("s3cret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("shell behind the guard: %d, want 200", rec.Code)
	}
}

// In --byok mode a session needs a provider, there is no default session, and
// the roster can't be listed: only the id reaches a session.
func TestBYOK_SessionsArePrivateAndNeedAProvider(t *testing.T) {
	ctx := context.Background()
	c := byokServer(t, "", "")

	if _, err := c.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{})); codeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateSession with no provider: %v, want InvalidArgument", err)
	}
	if _, err := c.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Provider: &webv1.ProviderSpec{Type: "openai"}})); codeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateSession with no model: %v, want InvalidArgument", err)
	}
	if _, err := c.GetStatus(ctx, connect.NewRequest(&webv1.GetStatusRequest{})); codeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetStatus with no session_id: %v, want NotFound", err)
	}
	if _, err := c.ListWebSessions(ctx, connect.NewRequest(&webv1.ListWebSessionsRequest{})); codeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListWebSessions: %v, want PermissionDenied", err)
	}

	resp, err := c.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Provider: &webv1.ProviderSpec{Type: "demo"}}))
	if err != nil {
		t.Fatalf("CreateSession demo: %v", err)
	}
	st, err := c.GetStatus(ctx, connect.NewRequest(&webv1.GetStatusRequest{SessionId: resp.Msg.GetSessionId()}))
	if err != nil || st.Msg.GetModelLabel() == "" {
		t.Fatalf("GetStatus on the new session: %v %v", st, err)
	}
}

// A session's key reaches its provider: a turn on it calls the endpoint with
// that key, and two sessions with different keys stay apart.
func TestBYOK_SessionUsesItsOwnKey(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var seen []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, "stop here", http.StatusTeapot)
	}))
	defer endpoint.Close()

	c := byokServer(t, "", "")
	for _, key := range []string{"key-one", "key-two"} {
		resp, err := c.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Provider: &webv1.ProviderSpec{
			Type: "openai", BaseUrl: endpoint.URL, Model: "m", ApiKey: key,
		}}))
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		// The endpoint refuses, so the turn fails; only the key it carried matters.
		_, _ = c.Submit(ctx, connect.NewRequest(&webv1.SubmitRequest{SessionId: resp.Msg.GetSessionId(), Input: "hi"}))
	}
	mu.Lock()
	defer mu.Unlock()
	got := strings.Join(seen, ",")
	if !strings.Contains(got, "Bearer key-one") || !strings.Contains(got, "Bearer key-two") {
		t.Fatalf("endpoint saw %q, want each session's own key", got)
	}
}
