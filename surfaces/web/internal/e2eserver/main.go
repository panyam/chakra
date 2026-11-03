// Command e2eserver serves agentweb's page and API for the Playwright checks in
// web/e2e. Chat has two connections, each over a StubProvider, so the header's
// model picker has a choice to show. A "coding" profile is served at /code/
// over a stub as well. Nothing needs a key or a network.
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net/http"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	web "github.com/panyam/chakra/surfaces/web"
)

func chatApp() (*host.App, error) {
	cfg := &host.Config{Connections: &host.ConnectionsConfig{
		Active: "local",
		Connections: map[string]host.ConnectionConfig{
			"local":    {Type: "lmstudio", Model: "qwen"},
			"deepseek": {Type: "openai", Model: "deepseek-chat"},
		},
	}}
	build := func(host.ConnectionConfig) (agent.Provider, error) { return agent.NewStubProvider(), nil }
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProviderBuilder(build))
}

func codingApp(context.Context) (*host.App, error) {
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "coding", Model: "stub-coder"}}
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(agent.NewStubProvider()))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "address to listen on")
	flag.Parse()

	mgr := web.NewSessionManager(func(context.Context) (*host.App, error) { return chatApp() })
	def, err := chatApp()
	if err != nil {
		log.Fatal(err)
	}
	mgr.SetDefault(def)
	defer mgr.CloseAll()
	h := web.HandlerWithSessions(mgr, web.WithProfile(web.Profile{Name: "coding", Path: "/code/", Build: codingApp}))
	log.Printf("e2eserver: listening on http://%s/", *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}
