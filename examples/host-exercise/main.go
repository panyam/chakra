// Command host-exercise is mission #89's exercise. It checks that agentchat's
// host honours what an MCP server declares (instructions, skills, event hints
// and agents) by running a host.App against a demo server, with a scripted
// model, on both client wires. It prints PASS or FAIL per check and wire, and
// exits non-zero only when the results differ from expectedFailures.
//
// --serve runs the demo server on its own instead, for pointing agentchat and a
// real model at it (make host-exercise-live).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"text/tabwriter"
	"time"
)

// wires are the client modes every check runs under. The host never picks one
// itself, so the env var is how to force each.
var wires = []string{"legacy", "stateless"}

const wireEnv = "MCPKIT_CLIENT_MODE"

func main() {
	serve := flag.String("serve", "", "serve the demo server on this address (e.g. :8790) instead of running the checks")
	verbose := flag.Bool("v", false, "keep mcpkit's server and client logging")
	flag.Parse()
	if !*verbose && *serve == "" {
		log.SetOutput(io.Discard)
	}

	if *serve != "" {
		d, err := newDemo()
		if err != nil {
			fmt.Fprintln(os.Stderr, "host-exercise:", err)
			os.Exit(1)
		}
		go d.tick(30 * time.Second)
		fmt.Printf("host-exercise demo server on %s/mcp (a %s event every 30s)\n", *serve, demoEvent)
		if err := http.ListenAndServe(*serve, d.handler); err != nil {
			fmt.Fprintln(os.Stderr, "host-exercise:", err)
			os.Exit(1)
		}
		return
	}

	results, err := runAll(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "host-exercise:", err)
		os.Exit(1)
	}
	if unexpected := report(os.Stdout, results); unexpected > 0 {
		os.Exit(1)
	}
}

type result struct {
	check  check
	wire   string
	pass   bool
	detail string
}

func runAll(ctx context.Context) ([]result, error) {
	prev, had := os.LookupEnv(wireEnv)
	defer func() {
		if had {
			os.Setenv(wireEnv, prev)
		} else {
			os.Unsetenv(wireEnv)
		}
	}()
	var out []result
	for _, c := range checks {
		for _, w := range wires {
			os.Setenv(wireEnv, w)
			pass, detail, err := runCheck(ctx, c, nil)
			if err != nil {
				pass, detail = false, "error: "+err.Error()
			}
			out = append(out, result{check: c, wire: w, pass: pass, detail: detail})
		}
	}
	return out, nil
}

// report prints the table and returns how many results differ from
// expectedFailures.
func report(w io.Writer, results []result) int {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	unexpected := 0
	for _, r := range results {
		ticket, expectFail := expectedFailures[outcomeKey{r.check.id, r.wire}]
		status := "PASS"
		if !r.pass {
			status = "FAIL"
		}
		note := ""
		switch {
		case !r.pass && expectFail:
			note = "expected, " + ticket
		case !r.pass:
			note = "UNEXPECTED"
			unexpected++
		case expectFail:
			note = "UNEXPECTED, remove " + ticket + "'s entry from expected.go"
			unexpected++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", status, r.wire, r.check.id, r.detail, note)
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%d check(s) x %d wire(s), %d unexpected\n", len(checks), len(wires), unexpected)
	return unexpected
}
