// Command coding-harness is mission #162's exercise and its coding agent. The
// agent is agentweb's server and UI over a host.App wired for coding
// (profile.go); nothing here changes how a turn runs.
//
// With no flags it runs the exercise: it copies testdata/fixture (a Go module
// with a failing test and an AGENTS.md) to a temp directory, drives the agent
// over agentweb's Connect API with a scripted model, runs the fixture's tests,
// and prints PASS or FAIL per behaviour. It exits non-zero only when the
// results differ from expectedFailures.
//
// --live uses a real model from config.json instead and prints a metrics line.
// To use the agent by hand, run agentweb over the same config and workspace
// files: make coding-harness-web does that on a fresh copy of the fixture.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"text/tabwriter"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

type options struct {
	live      bool
	active    string
	model     string
	keep      bool
	jsonlPath string
	metrics   string
	timeout   time.Duration
	// instructions is the agentsmd preset; empty keeps workspace.json's.
	instructions string
	// routeTo, routeFrom and routeModel send later steps to another
	// connection (#185).
	routeTo    string
	routeFrom  int
	routeModel string
}

func main() {
	var o options
	flag.BoolVar(&o.live, "live", false, "use a real model from config.json instead of the scripted one")
	flag.StringVar(&o.active, "active", "", "the config.json connection to use (default: the config's active)")
	flag.StringVar(&o.model, "model", "", "override the connection's model")
	flag.BoolVar(&o.keep, "keep", false, "keep the temp workspace and print where it is")
	flag.StringVar(&o.jsonlPath, "jsonl", "", "write every Watch frame to this file as JSONL")
	flag.StringVar(&o.metrics, "metrics", "", "write the metrics line as JSON to this file")
	flag.DurationVar(&o.timeout, "timeout", 0, "give up after this long (default 2m scripted, 15m live)")
	flag.StringVar(&o.instructions, "instructions", "", "how AGENTS.md and the like load: chakra, codex, claude or cursor (default: workspace.json's, else chakra)")
	flag.StringVar(&o.routeTo, "route-to", "", "send steps from --route-from on to this config.json connection (live only)")
	flag.IntVar(&o.routeFrom, "route-from", 2, "the first step --route-to answers")
	flag.StringVar(&o.routeModel, "route-model", "", "override --route-to's model")
	verbose := flag.Bool("v", false, "keep the host's logging")
	flag.Parse()
	if !*verbose {
		log.SetOutput(io.Discard)
	}

	if err := runExercise(o); err != nil {
		fmt.Fprintln(os.Stderr, "coding-harness:", err)
		os.Exit(1)
	}
}

// exerciseRun is one run of the exercise and its outcomes.
type exerciseRun struct {
	result   *runResult
	outcomes map[string]outcome
	root     string
}

type outcome struct {
	pass   bool
	detail string
}

// exercise copies the fixture, drives one run, and evaluates every check.
func exercise(ctx context.Context, o options) (*exerciseRun, error) {
	fixture, err := filepath.Abs(filepath.Join("testdata", "fixture"))
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "coding-harness-")
	if err != nil {
		return nil, err
	}
	// The file tools compare against resolved paths, and macOS temp dirs sit
	// behind a /var -> /private/var symlink.
	if tmp, err = filepath.EvalSymlinks(tmp); err != nil {
		return nil, err
	}
	root := filepath.Join(tmp, "repo")
	if err := copyTree(fixture, root); err != nil {
		return nil, err
	}
	if err := commitFixture(root); err != nil {
		return nil, err
	}

	p := profile{
		dir:          ".",
		root:         root,
		active:       o.active,
		model:        o.model,
		approval:     "allow",
		checkpoints:  filepath.Join(tmp, "checkpoints"),
		recorder:     &recorder{},
		instructions: o.instructions,
		routeTo:      o.routeTo,
		routeFrom:    o.routeFrom,
		routeModel:   o.routeModel,
	}
	if !o.live {
		if p.provider, err = scriptedProvider(fixture); err != nil {
			return nil, err
		}
		// CI is Linux, and the exec sandbox backend is darwin-only.
		p.unconfined = runtime.GOOS != "darwin"
	}

	timeout := o.timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
		if o.live {
			timeout = 15 * time.Minute
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := drive(ctx, p)
	if err != nil {
		return nil, err
	}
	res.finish(root, fixture)

	run := &exerciseRun{result: res, outcomes: map[string]outcome{}, root: tmp}
	for _, c := range checks {
		pass, detail := c.run(res, fixture)
		run.outcomes[c.id] = outcome{pass, detail}
	}
	if !o.keep {
		_ = os.RemoveAll(tmp)
	}
	return run, nil
}

// unexpected lists the checks whose outcome differs from expectedFailures.
func (r *exerciseRun) unexpected() []string {
	var out []string
	for _, c := range checks {
		_, pinned := expectedFailures[c.id]
		if r.outcomes[c.id].pass == pinned {
			out = append(out, c.id)
		}
	}
	return out
}

func runExercise(o options) error {
	run, err := exercise(context.Background(), o)
	if err != nil {
		return err
	}
	res := run.result

	mode := "scripted"
	if o.live {
		mode = "live"
	}
	fmt.Printf("coding-harness (%s), agentweb wire\n\n", mode)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RESULT\tCHECK\tWIRING\tDETAIL")
	for _, c := range checks {
		oc := run.outcomes[c.id]
		status := "PASS"
		if !oc.pass {
			status = "FAIL"
			if t, ok := expectedFailures[c.id]; ok {
				status = "FAIL " + t
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", status, c.id, c.wiring, oc.detail)
	}
	tw.Flush()

	m := metricsOf(res)
	fmt.Printf("\n%s\n", m)
	var review host.CmdResult
	if json.Unmarshal([]byte(res.review), &review) == nil && review.Message != "" {
		fmt.Printf("\n/review after the run:\n%s", review.Message)
	}
	if o.keep {
		fmt.Printf("workspace kept at %s\n", run.root)
	}
	if o.jsonlPath != "" {
		f, err := os.Create(o.jsonlPath)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := res.writeJSONL(f); err != nil {
			return err
		}
	}
	if o.metrics != "" {
		data, _ := json.MarshalIndent(m, "", "  ")
		if err := os.WriteFile(o.metrics, data, 0o644); err != nil {
			return err
		}
	}

	if o.live {
		return nil
	}
	if bad := run.unexpected(); len(bad) > 0 {
		return fmt.Errorf("results differ from expected.go for %v: a check that now passes should lose its line there", bad)
	}
	fmt.Println("\nmatches expected.go")
	return nil
}

// metrics is the live variant's baseline line, and what the parity script
// compares against codex.
type metrics struct {
	Steps     int `json:"steps"`
	ToolCalls int `json:"tool_calls"`
	InputTok  int `json:"input_tokens"`
	// CachedTok is the part of InputTok the provider's prompt cache served.
	CachedTok   int  `json:"cached_input_tokens"`
	OutputTok   int  `json:"output_tokens"`
	WallSeconds int  `json:"wall_seconds"`
	TestsPassed bool `json:"tests_passed"`
	// TestFilesUntouched is AGENTS.md's "never edit *_test.go" rule.
	TestFilesUntouched bool `json:"test_files_untouched"`
	// SchemaValid is whether the run returned a result valid against
	// result.schema.json. The Runner validated it; this reads the outcome.
	SchemaValid bool   `json:"schema_valid"`
	TurnError   string `json:"turn_error,omitempty"`
	// ByModel splits the tokens by model when steps were routed (#185).
	ByModel map[string]agent.Usage `json:"by_model,omitempty"`
}

func metricsOf(r *runResult) metrics {
	m := metrics{ToolCalls: len(r.toolCalls()), WallSeconds: int(r.wall.Round(time.Second).Seconds()), TestsPassed: r.testsPassed, TestFilesUntouched: len(r.testFilesChanged) == 0, TurnError: r.turnErr}
	if r.turn != nil {
		m.Steps = r.turn.Steps
		m.InputTok = r.turn.Usage.InputTokens
		m.CachedTok = r.turn.Usage.CachedInputTokens
		m.OutputTok = r.turn.Usage.OutputTokens
		m.SchemaValid = r.turn.Structured.Len() > 0 && r.turn.StructuredError == ""
		m.ByModel = r.turn.UsageByModel
	}
	return m
}

func (m metrics) String() string {
	tests := "fail"
	if m.TestsPassed {
		tests = "pass"
	}
	s := fmt.Sprintf("steps=%d tool_calls=%d in_tok=%d cached_tok=%d out_tok=%d wall=%ds tests=%s",
		m.Steps, m.ToolCalls, m.InputTok, m.CachedTok, m.OutputTok, m.WallSeconds, tests)
	for _, name := range slices.Sorted(maps.Keys(m.ByModel)) {
		u := m.ByModel[name]
		s += fmt.Sprintf(" %s=%d/%d/%d", cmp.Or(name, "default"), u.InputTokens, u.CachedInputTokens, u.OutputTokens)
	}
	if m.TurnError != "" {
		s += " turn_error=" + m.TurnError
	}
	return s
}
