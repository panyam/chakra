// Command walk steps through one agent's ladder with demokit (#369). Each
// ladder step is a demokit step: it shows what the step adds to the agent,
// then runs every case or lets you watch one. With no -live it runs on the
// agent's scripted stand-in model.
//
// demokit's own flags work too: --tui, --serve :8765, --non-interactive,
// --record run.json, and --doc md --from run.json to render a recorded walk
// with no model calls. --replay reuses a recording's inputs but runs the
// steps again, so on a live connection it calls the model again.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/panyam/chakra/examples/agents"
	"github.com/panyam/chakra/examples/agents/common"
	"github.com/panyam/demokit"
	"github.com/panyam/demokit/harness"
)

func main() {
	name := flag.String("agent", "", "the agent to walk")
	live := flag.String("live", "", "run on this connection from -profile, rather than the scripted model")
	profile := flag.String("profile", "", "the agentchat config holding -live's connection")
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:]))

	a, err := agents.Find(*name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	m := common.Model{Provider: a.Scripted(), Name: "scripted"}
	if *live != "" {
		if m, err = common.Live(*profile, *live); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	d, stop, err := common.Walk(context.Background(), a, m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer stop()
	harness.Run(d)
}
