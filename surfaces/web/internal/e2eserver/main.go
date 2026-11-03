// Command e2eserver serves agentweb's page and API for the Playwright checks in
// web/e2e. It has two connections, each over a StubProvider, so the header's
// model picker has a choice to show and nothing needs a key or a network.
package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	web "github.com/panyam/chakra/surfaces/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "address to listen on")
	flag.Parse()

	cfg := &host.Config{Connections: &host.ConnectionsConfig{
		Active: "local",
		Connections: map[string]host.ConnectionConfig{
			"local":    {Type: "lmstudio", Model: "qwen"},
			"deepseek": {Type: "openai", Model: "deepseek-chat"},
		},
	}}
	build := func(host.ConnectionConfig) (agent.Provider, error) { return agent.NewStubProvider(), nil }
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProviderBuilder(build))
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()
	log.Printf("e2eserver: listening on http://%s/", *addr)
	log.Fatal(http.ListenAndServe(*addr, web.Handler(app)))
}
