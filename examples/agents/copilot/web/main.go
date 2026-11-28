package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/examples/agents/common"
)

func main() {
	appAddr := flag.String("app-addr", "localhost:8781", "the kanban app's address: page, relay, join endpoint and bridge")
	agentAddr := flag.String("agent-addr", "localhost:8782", "agentweb's address: HostService and the <chakra-chat> bundle")
	live := flag.String("live", "", "the connection in -profile the agent runs on, such as haiku")
	profile := flag.String("profile", "", "an agentchat config with connections, such as ~/.agentchat/exercise-agents.json")
	stub := flag.Bool("stub", false, "run on a scripted model that only answers the done-check trigger, for the browser test")
	flag.Parse()
	name, provider := "a scripted model", agent.Provider(stubModel{})
	if !*stub {
		if *live == "" || *profile == "" {
			fmt.Fprintln(os.Stderr, "copilot web needs a real model: run make web LIVE=haiku, which passes -live and -profile")
			os.Exit(2)
		}
		model, err := common.Live(*profile, *live)
		if err != nil {
			log.Fatal(err)
		}
		name, provider = model.Name, model.Provider
	}

	appURL, agentURL := "http://"+*appAddr, "http://"+*agentAddr
	app, err := newApp(agentURL, []string{"localhost", "127.0.0.1"})
	if err != nil {
		log.Fatal(err)
	}
	defer app.close()
	agentH, closeAgent := newAgent(appURL, provider)
	defer closeAgent()

	go func() { log.Fatal(http.ListenAndServe(*agentAddr, agentH)) }()
	log.Printf("copilot on %s: open %s/", name, appURL)
	log.Fatal(http.ListenAndServe(*appAddr, app))
}
