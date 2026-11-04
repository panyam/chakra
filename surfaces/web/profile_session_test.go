package web

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
)

// recordingProfile builds coding-model Apps over store and records the ids
// it built and closed.
type recordingProfile struct {
	mu            sync.Mutex
	built, closed []string
}

func (r *recordingProfile) profile(store agent.RunStore) Profile {
	return Profile{
		Name: "coding",
		Path: "/code/",
		Build: func(_ context.Context, id string) (*host.App, error) {
			r.mu.Lock()
			r.built = append(r.built, id)
			r.mu.Unlock()
			opts := []host.AppOption{host.WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "ok", FinishReason: "stop"}))}
			if store != nil {
				opts = append(opts, host.WithRunStore(store))
			}
			return host.NewApp(&host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "coding-model"}}, io.Discard, strings.NewReader(""), opts...)
		},
		Close: func(id string) {
			r.mu.Lock()
			r.closed = append(r.closed, id)
			r.mu.Unlock()
		},
	}
}

// A coding session on a server with a session store survives a restart as a
// coding session, rebuilt by its profile under the same id (#244).
func TestProfileSessionSurvivesRestartAsItsOwnKind(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	rec := &recordingProfile{}

	mgr1 := NewSessionManagerWithStore(persistedFactory(t, store), store)
	NewHostServiceWithSessions(mgr1, WithProfile(rec.profile(store)))
	id, err := mgr1.CreateProfile(ctx, "coding")
	if err != nil {
		t.Fatalf("CreateProfile with a store: %v", err)
	}
	if ProfileOf(id) != "coding" {
		t.Errorf("id %q doesn't name its profile", id)
	}
	app, _ := mgr1.Get(ctx, id)
	if err := app.RunTurn(ctx, "remember 42"); err != nil {
		t.Fatal(err)
	}
	mgr1.CloseAll()
	if len(rec.closed) != 0 {
		t.Errorf("closed %v on shutdown, though the store can resume them", rec.closed)
	}

	mgr2 := NewSessionManagerWithStore(persistedFactory(t, store), store)
	NewHostServiceWithSessions(mgr2, WithProfile(rec.profile(store)))
	t.Cleanup(mgr2.CloseAll)
	again, ok := mgr2.Get(ctx, id)
	if !ok {
		t.Fatal("the coding session didn't come back after a restart")
	}
	if again.ModelLabel() != "coding-model" {
		t.Errorf("rehydrated as %q, want the coding profile's App", again.ModelLabel())
	}
	if !slices.Equal(rec.built, []string{id, id}) {
		t.Errorf("built %v, want the same id at create and at resume", rec.built)
	}
	res, err := again.Dispatch(ctx, "/history")
	if err != nil || len(res.Messages) == 0 || res.Messages[0].Text != "remember 42" {
		t.Errorf("history after restart = %+v, %v", res, err)
	}
}

func TestListWebSessionsNamesEachSessionsKind(t *testing.T) {
	rec := &recordingProfile{}
	_, client := profileServer(t, WithProfile(rec.profile(nil)))
	ctx := context.Background()
	code, err := client.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{Profile: "coding"}))
	if err != nil {
		t.Fatal(err)
	}
	chat, err := client.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	list, err := client.ListWebSessions(ctx, connect.NewRequest(&webv1.ListWebSessionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range list.Msg.GetSessions() {
		got[s.GetSessionId()] = s.GetProfile() + " at " + s.GetPath()
	}
	want := map[string]string{
		code.Msg.GetSessionId(): "coding at /code/",
		chat.Msg.GetSessionId(): " at /",
		DefaultSessionID:        " at /",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("session %s: %q, want %q (all: %v)", id, got[id], w, got)
		}
	}
	if len(list.Msg.GetSessionIds()) != len(list.Msg.GetSessions()) {
		t.Errorf("session_ids %v and sessions %v disagree", list.Msg.GetSessionIds(), got)
	}
}

// Without a store a closed session can't come back, so its profile removes
// what it left on disk, on CloseSession and on shutdown.
func TestClosingAProfileSessionCleansUpWithoutAStore(t *testing.T) {
	ctx := context.Background()
	rec := &recordingProfile{}
	mgr := NewSessionManager(func(context.Context) (*host.App, error) { return modelApp(t, "chat-model"), nil })
	NewHostServiceWithSessions(mgr, WithProfile(rec.profile(nil)))
	a, err := mgr.CreateProfile(ctx, "coding")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.CreateProfile(ctx, "coding")
	if err != nil {
		t.Fatal(err)
	}
	chat, _, err := mgr.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mgr.Close(a)
	mgr.Close(chat)
	if !slices.Equal(rec.closed, []string{a}) {
		t.Errorf("after closing %s and a chat session, cleaned %v", a, rec.closed)
	}
	mgr.CloseAll()
	if !slices.Equal(rec.closed, []string{a, b}) {
		t.Errorf("after shutdown, cleaned %v, want %v", rec.closed, []string{a, b})
	}
}

func TestProfileNameCantHoldADash(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(error).Error(), "dash") {
			t.Errorf("recover() = %v, want a refusal naming the dash", r)
		}
	}()
	NewHostServiceWithSessions(NewSessionManager(nil), WithProfile(Profile{Name: "code-review"}))
}
