package web

import (
	"context"
	"io"
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

// The header's model picker reads the connections from GetStatus and switches
// through Dispatch("/provider <name>"), so both have to agree over the wire.
func TestGetStatusListsProvidersAndFollowsASwitch(t *testing.T) {
	cfg := &host.Config{Connections: &host.ConnectionsConfig{
		Active: "local",
		Connections: map[string]host.ConnectionConfig{
			"local": {Type: "lmstudio", Model: "qwen"},
			"cloud": {Type: "openai", Model: "gpt-5"},
		},
	}}
	build := func(host.ConnectionConfig) (agent.Provider, error) { return agent.NewStubProvider(), nil }
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProviderBuilder(build))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(Handler(app))
	defer srv.Close()
	client := webv1connect.NewHostServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()

	st, err := client.GetStatus(ctx, connect.NewRequest(&webv1.GetStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Msg.GetProviders(); !slices.Equal(got, []string{"cloud", "local"}) || st.Msg.GetActiveProvider() != "local" {
		t.Fatalf("GetStatus providers = %v active %q, want [cloud local] and local", got, st.Msg.GetActiveProvider())
	}

	if _, err := client.Dispatch(ctx, connect.NewRequest(&webv1.DispatchRequest{Line: "/provider cloud"})); err != nil {
		t.Fatal(err)
	}
	st, err = client.GetStatus(ctx, connect.NewRequest(&webv1.GetStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if st.Msg.GetActiveProvider() != "cloud" || st.Msg.GetModelLabel() != "cloud (gpt-5)" {
		t.Errorf("after /provider cloud: active %q label %q, want cloud and \"cloud (gpt-5)\"", st.Msg.GetActiveProvider(), st.Msg.GetModelLabel())
	}
}

func TestGetStatusWithoutConnectionsListsNone(t *testing.T) {
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "stub-model"}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(Handler(app))
	defer srv.Close()
	client := webv1connect.NewHostServiceClient(srv.Client(), srv.URL)
	st, err := client.GetStatus(context.Background(), connect.NewRequest(&webv1.GetStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Msg.GetProviders()) != 0 || st.Msg.GetActiveProvider() != "" {
		t.Errorf("single-model session listed providers %v active %q", st.Msg.GetProviders(), st.Msg.GetActiveProvider())
	}
}
