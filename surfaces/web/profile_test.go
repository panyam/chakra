package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
	"github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1/webv1connect"
)

func modelApp(t *testing.T, model string) *host.App {
	t.Helper()
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: model}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func profileServer(t *testing.T, opts ...Option) (*httptest.Server, webv1connect.HostServiceClient) {
	t.Helper()
	mgr := NewSessionManager(func(context.Context) (*host.App, error) { return modelApp(t, "chat-model"), nil })
	mgr.SetDefault(modelApp(t, "chat-model"))
	t.Cleanup(mgr.CloseAll)
	srv := httptest.NewServer(HandlerWithSessions(mgr, opts...))
	t.Cleanup(srv.Close)
	return srv, webv1connect.NewHostServiceClient(srv.Client(), srv.URL)
}

var codingProfile = func(t *testing.T) Profile {
	return Profile{Name: "coding", Path: "/code/", Build: func(context.Context, string) (*host.App, error) { return modelApp(t, "coding-model"), nil }}
}

// A CreateSession naming a profile gets that profile's App, and the session
// joins the same roster as chat sessions (#178).
func TestCreateSessionBuildsTheNamedProfile(t *testing.T) {
	_, client := profileServer(t, WithProfile(codingProfile(t)))
	ctx := context.Background()

	created, err := client.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Profile: "coding"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.GetSessionId()
	st, err := client.GetStatus(ctx, connect.NewRequest(&webv1.GetStatusRequest{SessionId: id}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Msg.GetModelLabel(), "coding-model") {
		t.Errorf("coding session's model label = %q", st.Msg.GetModelLabel())
	}
	chat, err := client.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Profile: "chat"}))
	if err != nil {
		t.Fatal(err)
	}
	st, _ = client.GetStatus(ctx, connect.NewRequest(&webv1.GetStatusRequest{SessionId: chat.Msg.GetSessionId()}))
	if !strings.Contains(st.Msg.GetModelLabel(), "chat-model") {
		t.Errorf("profile chat built %q, want the default factory's", st.Msg.GetModelLabel())
	}
	list, err := client.ListWebSessions(ctx, connect.NewRequest(&webv1.ListWebSessionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if ids := list.Msg.GetSessionIds(); !slices.Contains(ids, id) || !slices.Contains(ids, chat.Msg.GetSessionId()) {
		t.Errorf("ListWebSessions = %v, want both the coding and the chat session", ids)
	}
}

func TestCreateSessionRefusesAnUnknownOrFailingProfile(t *testing.T) {
	failing := Profile{Name: "broken", Build: func(context.Context, string) (*host.App, error) { return nil, errors.New("needs OPENAI_API_KEY") }}
	_, client := profileServer(t, WithProfile(codingProfile(t)), WithProfile(failing))
	ctx := context.Background()

	_, err := client.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Profile: "review"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "have chat, coding, broken") {
		t.Errorf("unknown profile: err = %v, want InvalidArgument listing the profiles", err)
	}
	_, err = client.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Profile: "broken"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "needs OPENAI_API_KEY") {
		t.Errorf("failing profile: err = %v, want FailedPrecondition with the build's reason", err)
	}
}

func TestProfilePageIsTheShellMarkedWithItsProfile(t *testing.T) {
	srv, _ := profileServer(t, WithProfile(codingProfile(t)))
	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/code/"); code != 200 || !strings.Contains(body, `data-profile="coding"`) || !strings.Contains(body, `id="page-spec"`) {
		t.Errorf("/code/ = %d, want the shell marked coding", code)
	}
	if _, body := get("/"); strings.Contains(body, "data-profile") {
		t.Error("the chat page is marked with a profile")
	}
	if code, _ := get("/code/elsewhere"); code != 404 {
		t.Errorf("/code/elsewhere = %d, want 404", code)
	}

	plain, _ := profileServer(t)
	resp, err := http.Get(plain.URL + "/code/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("/code/ without a coding profile = %d, want 404", resp.StatusCode)
	}
}
