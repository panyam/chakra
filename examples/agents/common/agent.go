// Package common is what every agent in examples/agents builds on (mission
// #349): the Agent type, running a ladder on a model (Climb), reading
// connections from an agentchat profile, an agentchat config for an agent,
// and a demokit walk through an agent's ladder (Walk). An agent package
// exports Agent() Agent and fills in only what is its own.
package common

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/connection"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/demokit"
)

// Agent is one agent's ladder and what it runs on.
type Agent struct {
	// Name is the agent's directory under examples/agents.
	Name string
	// Ladder builds the agent's steps and eval. It can start what the steps
	// share, such as a tool server, and stop releases it once the ladder has
	// run. stop is never nil when err is nil.
	Ladder func(ctx context.Context) (l eval.Ladder, stop func(), err error)
	// Instructions is the system prompt every step starts from.
	Instructions string
	// Scripted is the stand-in model CI runs the ladder on.
	Scripted func() agent.Provider
	// MaxSteps caps each turn. Zero means 8.
	MaxSteps int
	// Chat sets the agent up for agentchat, as its last step, for make
	// chat-agent. Nil means the agent can't run there yet.
	Chat func() (ChatSetup, error)

	// About is the walk's opening section: the job, the eval, and anything
	// the steps share, such as a tool server. Nil gives a generic one.
	About []string
	// StepNotes says why each step exists, by step name, as a note on that
	// step of the walk. A step with no note gets none.
	StepNotes map[string]string
	// Walk lays out the walk. It gets the demo and a Walker with the
	// default pieces, and can put its own sections and steps between them.
	// Nil means DefaultWalk.
	Walk func(d *demokit.Demo, w *Walker)
}

// Static adapts a ladder that starts nothing to Agent.Ladder.
func Static(l func() eval.Ladder) func(context.Context) (eval.Ladder, func(), error) {
	return func(context.Context) (eval.Ladder, func(), error) { return l(), func() {}, nil }
}

// ChatSetup is what an agentchat config needs to run an agent: its prompt
// and the MCP servers it calls. Steps that are code, such as a stuck
// detector or a TurnEnd hook, have no config field, so agentchat runs
// without them.
type ChatSetup struct {
	Instructions string
	Servers      []ChatServer
}

// ChatServer is one stdio MCP server in an agentchat config.
type ChatServer struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Dir     string   `json:"dir,omitempty"`
}

// Model is what a ladder runs on: a provider, the name its usage is counted
// under, and prices for that name, or nil when the price is unknown.
type Model struct {
	Provider agent.Provider
	Name     string
	Prices   eval.Prices
}

// Climb runs a's ladder on m. It fails only when the ladder can't start.
// When progress isn't nil, Climb writes each step to it as the step starts,
// and each case as it finishes: PASS or FAIL, time, cost when priced, the
// question, and why a failed case failed.
func Climb(ctx context.Context, a Agent, m Model, progress io.Writer) (eval.LadderReport, error) {
	l, stop, err := a.Ladder(ctx)
	if err != nil {
		return eval.LadderReport{}, err
	}
	defer stop()
	steps := a.MaxSteps
	if steps == 0 {
		steps = 8
	}
	base := agent.RunnerConfig{Provider: m.Provider, Instructions: a.Instructions, MaxSteps: steps}
	suite := eval.Suite{Config: base, ModelName: m.Name, Prices: m.Prices}
	if progress != nil {
		fmt.Fprintf(progress, "%s on %s: %d steps, %d cases each\n", a.Name, m.Name, len(l.Steps), len(l.Cases))
		for i := range l.Steps {
			l.Steps[i] = announce(l.Steps[i], i, len(l.Steps), progress)
		}
		suite.OnCase = func(_ int, r eval.CaseReport) { fmt.Fprintln(progress, caseLine(r)) }
	}
	return l.Run(ctx, suite), nil
}

// announce wraps s so it prints its header when the ladder reaches it.
func announce(s eval.Step, i, n int, w io.Writer) eval.Step {
	configure := s.Configure
	s.Configure = func(c agent.RunnerConfig) (agent.RunnerConfig, error) {
		fmt.Fprintf(w, "[%d/%d %s]\n", i+1, n, s.Name)
		if configure == nil {
			return c, nil
		}
		return configure(c)
	}
	return s
}

// caseLine is one finished case: verdict, time, cost and question, then
// the first reason it failed.
func caseLine(r eval.CaseReport) string {
	verdict := "PASS"
	if !r.Pass {
		verdict = "FAIL"
	}
	line := fmt.Sprintf("  %s %5.1fs", verdict, r.Elapsed.Seconds())
	if r.Cost != nil {
		line += fmt.Sprintf("  $%.5f", *r.Cost)
	}
	line += "  " + r.Case
	if r.Pass {
		return line
	}
	why := r.RunErr
	for _, sc := range r.Scores {
		if why == "" && !sc.Pass {
			why = sc.Name + ": " + sc.Detail
		}
	}
	if len(why) > 160 {
		why = why[:160] + "..."
	}
	return line + "\n         " + why
}

// Connection is one connection in an agentchat config, as Connections
// lists it.
type Connection struct {
	Name string
	connection.Config
}

// readProfile reads the connections of the agentchat config at path.
func readProfile(path string) (map[string]connection.Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agents: reading the profile: %w", err)
	}
	var cfg struct {
		Connections struct {
			Connections map[string]connection.Config `json:"connections"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("agents: %s: %w", path, err)
	}
	return cfg.Connections.Connections, nil
}

// Connections lists the connections in the agentchat config at path, by
// name.
func Connections(path string) ([]Connection, error) {
	m, err := readProfile(path)
	if err != nil {
		return nil, err
	}
	var out []Connection
	for _, name := range slices.Sorted(maps.Keys(m)) {
		out = append(out, Connection{Name: name, Config: m[name]})
	}
	return out, nil
}

// Live builds the model for the connection called name in an agentchat
// config at path, with connection.Build. The connection's Price, when it
// has one, prices the run. eval.Price has no cached-input rate, so cached
// tokens are priced at the input rate. A name the config lacks is an error
// that lists the names it has.
func Live(path, name string) (Model, error) {
	m, err := readProfile(path)
	if err != nil {
		return Model{}, err
	}
	c, ok := m[name]
	if !ok {
		return Model{}, fmt.Errorf("agents: %s has no connection %q. It has %s", path, name, strings.Join(slices.Sorted(maps.Keys(m)), ", "))
	}
	p, err := connection.Build(c)
	if err != nil {
		return Model{}, fmt.Errorf("agents: connection %q: %w", name, err)
	}
	model := Model{Provider: p, Name: name}
	if c.Price != nil {
		model.Prices = eval.Prices{name: {InputPerM: c.Price.Input, OutputPerM: c.Price.Output}}
	}
	return model, nil
}

// ChatConfig writes an agentchat config that runs a as its last step. It
// copies the connections block of the config at profile, and adds a's
// instructions and MCP servers.
func ChatConfig(a Agent, profile string) ([]byte, error) {
	if a.Chat == nil {
		return nil, fmt.Errorf("agents: %s can't run in agentchat yet", a.Name)
	}
	setup, err := a.Chat()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(profile)
	if err != nil {
		return nil, fmt.Errorf("agents: reading the profile: %w", err)
	}
	var cfg struct {
		Connections json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("agents: %s: %w", profile, err)
	}
	steps := a.MaxSteps
	if steps == 0 {
		steps = 8
	}
	return json.MarshalIndent(map[string]any{
		"connections":  cfg.Connections,
		"instructions": setup.Instructions,
		"maxSteps":     steps,
		"servers":      setup.Servers,
	}, "", "  ")
}
