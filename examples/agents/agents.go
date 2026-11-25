// Package agents holds chakra's agents for a job, each built as a ladder
// (mission #349). Every agent lives in its own directory with a README that
// walks through its steps, and `make exercise-agents` runs each ladder and
// prints a table: a row per step with what it passed and what a correct
// answer cost.
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/connection"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/chakra/examples/agents/calc"
	"github.com/panyam/chakra/examples/agents/codeq"
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
}

// All lists the agents, in the order the exercise runs them.
var All = []Agent{
	{Name: "calc", Ladder: static(calc.Ladder), Instructions: calc.Instructions, Scripted: calc.Scripted},
	{Name: "codeq", Ladder: codeq.Ladder, Instructions: codeq.Instructions, Scripted: codeq.Scripted},
}

// static adapts a ladder that starts nothing.
func static(l func() eval.Ladder) func(context.Context) (eval.Ladder, func(), error) {
	return func(context.Context) (eval.Ladder, func(), error) { return l(), func() {}, nil }
}

// Find returns the agent called name.
func Find(name string) (Agent, error) {
	for _, a := range All {
		if a.Name == name {
			return a, nil
		}
	}
	return Agent{}, fmt.Errorf("agents: no agent %q", name)
}

// Model is what a ladder runs on: a provider, the name its usage is counted
// under, and prices for that name, or nil when the price is unknown.
type Model struct {
	Provider agent.Provider
	Name     string
	Prices   eval.Prices
}

// Climb runs a's ladder on m. It fails only when the ladder can't start.
func Climb(ctx context.Context, a Agent, m Model) (eval.LadderReport, error) {
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
	return l.Run(ctx, eval.Suite{Config: base, ModelName: m.Name, Prices: m.Prices}), nil
}

// Live builds the model for the connection called name in an agentchat
// config at path, with connection.Build. The connection's Price, when it
// has one, prices the run. eval.Price has no cached-input rate, so cached
// tokens are priced at the input rate.
func Live(path, name string) (Model, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Model{}, err
	}
	var cfg struct {
		Connections struct {
			Connections map[string]connection.Config `json:"connections"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Model{}, fmt.Errorf("agents: %s: %w", path, err)
	}
	c, ok := cfg.Connections.Connections[name]
	if !ok {
		return Model{}, fmt.Errorf("agents: %s has no connection %q", path, name)
	}
	p, err := connection.Build(c)
	if err != nil {
		return Model{}, fmt.Errorf("agents: connection %q: %w", name, err)
	}
	m := Model{Provider: p, Name: name}
	if c.Price != nil {
		m.Prices = eval.Prices{name: {InputPerM: c.Price.Input, OutputPerM: c.Price.Output}}
	}
	return m, nil
}
