package web

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
	"github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1/webv1connect"
)

type resultObserver struct{ results []*agent.TurnResult }

func (r *resultObserver) On(ev host.HostEvent) {
	if ev.Kind == host.HostTurnDone {
		r.results = append(r.results, ev.Result)
	}
}

// TestSubmitCarriesTheResponseSchema checks the wiring: a Submit with a
// response_schema gets the structured answer on turn-done, and one that
// isn't JSON is refused before the turn runs.
func TestSubmitCarriesTheResponseSchema(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "done"}, agent.StubTurn{Text: `{"ok":true}`})
	rec := &resultObserver{}
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "stub-model"}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(stub), host.WithObserver(rec))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(Handler(app))
	defer srv.Close()
	client := webv1connect.NewHostServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()

	if _, err := client.Submit(ctx, connect.NewRequest(&webv1.SubmitRequest{Input: "go", ResponseSchema: `{"type":"object","required":["ok"]}`})); err != nil {
		t.Fatal(err)
	}
	if len(rec.results) != 1 || string(rec.results[0].Structured.Raw()) != `{"ok":true}` {
		t.Fatalf("turn-done results = %+v", rec.results)
	}
	_, err = client.Submit(ctx, connect.NewRequest(&webv1.SubmitRequest{Input: "go", ResponseSchema: `{not json`}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a non-JSON schema: err = %v, want InvalidArgument", err)
	}
}
