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

// gatedProvider holds its first model call until release closes.
type gatedProvider struct {
	*agent.StubProvider
	started chan struct{}
	release chan struct{}
	calls   int
}

func (p *gatedProvider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	p.calls++
	if p.calls == 1 {
		close(p.started)
		<-p.release
	}
	return p.StubProvider.Stream(ctx, req)
}

// TestSubmitDuringATurnSteers checks the wiring: a second Submit while a turn
// runs returns at once with steered set, instead of waiting for the turn.
func TestSubmitDuringATurnSteers(t *testing.T) {
	prov := &gatedProvider{
		StubProvider: agent.NewStubProvider(agent.StubTurn{Text: "first"}, agent.StubTurn{Text: "second"}),
		started:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "stub-model"}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(prov))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(Handler(app))
	defer srv.Close()
	client := webv1connect.NewHostServiceClient(srv.Client(), srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first := make(chan *webv1.SubmitResponse, 1)
	go func() {
		resp, err := client.Submit(ctx, connect.NewRequest(&webv1.SubmitRequest{Input: "fix Sum"}))
		if err != nil {
			t.Error(err)
			close(first)
			return
		}
		first <- resp.Msg
	}()
	<-prov.started

	resp, err := client.Submit(ctx, connect.NewRequest(&webv1.SubmitRequest{Input: "also check Mean"}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Msg.GetSteered() {
		t.Error("a Submit during a turn wasn't steered")
	}
	close(prov.release)
	if r := <-first; r == nil || r.GetSteered() {
		t.Errorf("the first Submit = %+v, want a turn that ran", r)
	}
}
