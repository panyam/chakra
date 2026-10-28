package web

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
	"github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1/webv1connect"
)

// drainWatch runs one Watch to completion and returns the kinded frames it
// carried. It relies on the server's watch window to end the stream.
func drainWatch(t *testing.T, client webv1connect.HostServiceClient, resumeAfter uint64) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Watch(ctx, connect.NewRequest(&webv1.WatchRequest{ResumeAfter: resumeAfter}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	var kinds []string
	for stream.Receive() {
		if k := stream.Msg().GetKind(); k != "" {
			kinds = append(kinds, k)
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("Watch did not end within the window; got %v", kinds)
	}
	return kinds
}

// A watch window ends the stream on its own, and resume_after skips the events
// a reconnecting client has already applied, so a host that buffers whole
// responses can deliver the log as a series of polls without repeating any of
// it.
func TestWatchWindowAndResumeAfter(t *testing.T) {
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "first", FinishReason: "stop"},
		agent.StubTurn{Text: "second", FinishReason: "stop"},
	)
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "stub-model"}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(stub))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	defer app.Close()
	mgr := NewSessionManager(nil)
	mgr.SetDefault(app)
	srv := httptest.NewServer(HandlerWithSessions(mgr, WithWatchWindow(200*time.Millisecond)))
	defer srv.Close()
	client := webv1connect.NewHostServiceClient(srv.Client(), srv.URL)

	submit := func(in string) {
		if _, err := client.Submit(context.Background(), connect.NewRequest(&webv1.SubmitRequest{Input: in})); err != nil {
			t.Fatalf("Submit %q: %v", in, err)
		}
	}
	countDone := func(kinds []string) int {
		n := 0
		for _, k := range kinds {
			if k == string(host.HostTurnDone) {
				n++
			}
		}
		return n
	}

	submit("hi")
	first := drainWatch(t, client, 0)
	if countDone(first) != 1 {
		t.Fatalf("first poll: want 1 turn-done, got %v", first)
	}

	submit("again")
	second := drainWatch(t, client, uint64(len(first)))
	if countDone(second) != 1 {
		t.Fatalf("resumed poll: want only the new turn's turn-done, got %v", second)
	}

	if rest := drainWatch(t, client, uint64(len(first)+len(second))); len(rest) != 0 {
		t.Fatalf("caught-up poll: want no events, got %v", rest)
	}
}
