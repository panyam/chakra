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
	"strings"

	"github.com/panyam/chakra/eval"
	"github.com/panyam/chakra/examples/agents"
)

func main() {
	only := flag.String("agent", "", "run only this agent")
	live := flag.String("live", "", "run on this connection from -profile, rather than the scripted model")
	profile := flag.String("profile", "", "the agentchat config holding -live's connection")
	cases := flag.Bool("cases", false, "after each table, print every failing case with its scores")
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
		r, err := agents.Climb(context.Background(), a, m)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("on %s\n", m.Name)
		fmt.Print(r.Table())
		if *cases {
			printFailures(r)
		}
	}
}

// printFailures prints each step's failing cases, with every score's detail
// and the answer, so a row in the table can be traced to what the model said.
func printFailures(r eval.LadderReport) {
	for _, s := range r.Steps {
		for _, c := range s.Report.Cases {
			if c.Pass {
				continue
			}
			fmt.Printf("\n[%s] FAIL %s\n", s.Step, c.Case)
			if c.RunErr != "" {
				fmt.Printf("    run error: %s\n", c.RunErr)
			}
			for _, sc := range c.Scores {
				if !sc.Pass {
					fmt.Printf("    %s: %s\n", sc.Name, sc.Detail)
				}
			}
			if c.Answer != "" {
				fmt.Printf("    answer: %s\n", strings.ReplaceAll(c.Answer, "\n", " | "))
			}
		}
	}
}
