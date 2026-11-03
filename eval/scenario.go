package eval

import (
	"context"
	"fmt"

	"github.com/panyam/chakra"
)

// Scenario is a multi-turn eval input. It is a sequence of user turns. They
// run in order and share one conversation, and optionally one working-memory
// scratchpad. Memory evals (LongMemEval and kin) build up facts across turns
// and sessions, then ask a question. They test whether earlier turns survive
// into a later answer, and the single-turn Case cannot express that.
//
// RunScenario threads history across turns and keeps one MemorySource for
// the whole run, so a fact remembered on turn 1 is recallable on turn 5.
// A Scorer grades the last turn, and earlier turns are setup.
type Scenario struct {
	// Name identifies the scenario in a report. Should be unique in a Suite.
	Name string

	// History seeds the conversation before the first turn runs. It is data,
	// not turns: nothing in it is executed, and the model is never called to
	// produce it.
	//
	// Imagine a memory benchmark that supplies a long prior conversation and
	// then asks one question about it. Replaying that conversation as Turns
	// would call the model once per historical message. It would also grade
	// the model's own invented replies, not the dataset's. History holds the
	// conversation as data instead, and Turns cannot do that. History is the
	// conversation the agent is told it had, and Turns are the turns the
	// agent takes.
	History []agent.Message

	// Turns are the user inputs, oldest first. Each runs as one agent turn
	// against the accumulated history; the final turn's Result is the graded
	// one.
	Turns []string

	// Memory gives the run a working-memory scratchpad when true. The
	// scratchpad is a MemorySource merged with any base Tools, so the model
	// can remember and recall across the turns. NewMemoryStore backs it when
	// set, else a fresh in-memory default. RunScenario builds the store once
	// per call, so scenarios never share memory.
	Memory bool

	// NewMemoryStore builds the MemoryStore behind this scenario's working
	// memory. Nil uses the in-memory default. Set it to grade a different
	// backend (redis, gorm, a future semantic store) through the same
	// scenarios, since an eval harness exists to compare impls. It is a
	// factory, not a value, so each RunScenario call gets its own store and
	// scenarios in a suite never cross-contaminate. To share state across
	// scenarios, a caller can close over one store and return it.
	// A non-nil NewMemoryStore implies Memory.
	NewMemoryStore func() (agent.MemoryStore, error)

	// Tools is the base tool surface, merged under the memory tools when
	// Memory is set. Nil offers only the memory tools (or none, when Memory
	// is false).
	Tools agent.ToolSource

	// Instructions overrides RunnerConfig.Instructions for this scenario
	// when non-empty.
	Instructions string

	// MaxSteps overrides RunnerConfig.MaxSteps per turn when positive.
	MaxSteps int
}

// RunScenario applies the scenario's overrides to a copy of cfg and runs
// each turn in order. It threads history and one shared working-memory
// source across the turns. It returns one Result per turn, and the last is
// the graded turn. The returned error means a harness-level failure, such as
// an invalid RunnerConfig. When a turn runs and fails, its error goes in that
// turn's Result.Err.
//
// RunScenario builds one Runner and reuses it for every turn. The Runner
// holds no state beyond the history it is handed. The shared MemorySource
// sits in cfg.Tools, so reusing the Runner lets it persist across turns.
func RunScenario(ctx context.Context, cfg agent.RunnerConfig, s Scenario) ([]Result, error) {
	if s.Instructions != "" {
		cfg.Instructions = s.Instructions
	}
	if s.MaxSteps > 0 {
		cfg.MaxSteps = s.MaxSteps
	}
	if s.Tools != nil {
		cfg.Tools = s.Tools
	}
	if s.Memory || s.NewMemoryStore != nil {
		store := agent.MemoryStore(agent.NewInMemoryMemoryStore())
		if s.NewMemoryStore != nil {
			var err error
			if store, err = s.NewMemoryStore(); err != nil {
				return nil, fmt.Errorf("eval: scenario %q memory store: %w", s.Name, err)
			}
		}
		mem, err := agent.NewMemorySource(store)
		if err != nil {
			return nil, fmt.Errorf("eval: scenario %q memory: %w", s.Name, err)
		}
		if cfg.Tools == nil {
			cfg.Tools = mem
		} else {
			multi := agent.NewMultiSource()
			if err := multi.Add("base", cfg.Tools); err != nil {
				return nil, fmt.Errorf("eval: scenario %q tools: %w", s.Name, err)
			}
			if err := multi.Add("memory", mem); err != nil {
				return nil, fmt.Errorf("eval: scenario %q memory: %w", s.Name, err)
			}
			cfg.Tools = multi
		}
	}

	runner, err := agent.NewRunner(cfg)
	if err != nil {
		return nil, fmt.Errorf("eval: build runner for scenario %q: %w", s.Name, err)
	}

	// Copy History rather than alias it. The loop appends to history, and a
	// caller may run one Scenario value more than once (across memory
	// backends, for instance) or share it across a suite.
	history := append([]agent.Message(nil), s.History...)
	results := make([]Result, 0, len(s.Turns))
	for i, input := range s.Turns {
		history = append(history, agent.Message{Role: agent.RoleUser, Text: input})

		var events []agent.Event
		emit := func(e agent.Event) { events = append(events, e) }

		turn, runErr := runner.Run(ctx, history, emit)
		// Each turn is named "<scenario>#<n>" so a per-turn Result is
		// traceable back to its position in the scenario.
		c := Case{Name: fmt.Sprintf("%s#%d", s.Name, i+1), Input: input}
		results = append(results, Result{Case: c, Turn: turn, Events: events, Err: runErr})
		if turn != nil {
			history = append(history, turn.Messages...)
		}
	}
	return results, nil
}

// Final returns the last turn's Result, the graded turn of a scenario. It
// panics on an empty slice, since a scenario always has at least one turn.
// So a scorer can write Final(results) without checking the length.
func Final(results []Result) Result {
	return results[len(results)-1]
}
