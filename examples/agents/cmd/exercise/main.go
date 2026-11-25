// Command exercise runs each agent's ladder and prints its table (#350).
// With no flags it runs every agent on its scripted stand-in model, as CI
// does. -live names a connection in the agentchat config at -profile, and
// -agent picks one agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/panyam/chakra/examples/agents"
)

func main() {
	only := flag.String("agent", "", "run only this agent")
	live := flag.String("live", "", "run on this connection from -profile, rather than the scripted model")
	profile := flag.String("profile", "", "the agentchat config holding -live's connection")
	flag.Parse()

	list := agents.All
	if *only != "" {
		a, err := agents.Find(*only)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		list = []agents.Agent{a}
	}
	if *live != "" && *profile == "" {
		fmt.Fprintln(os.Stderr, "exercise: -live needs -profile, the agentchat config that defines it")
		os.Exit(2)
	}

	for i, a := range list {
		m := agents.Model{Provider: a.Scripted(), Name: "scripted"}
		if *live != "" {
			var err error
			if m, err = agents.Live(*profile, *live); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("on %s\n", m.Name)
		fmt.Print(agents.Climb(context.Background(), a, m).Table())
	}
}
