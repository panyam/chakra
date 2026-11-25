// Package agents lists chakra's agents for a job, each built as a ladder
// (mission #349). Every agent lives in its own directory with a README that
// walks through its steps. The shared machinery is in common: make
// exercise-agents runs each ladder and prints its table, and make walk-agent
// steps through one ladder with demokit.
package agents

import (
	"fmt"

	"github.com/panyam/chakra/examples/agents/calc"
	"github.com/panyam/chakra/examples/agents/codeq"
	"github.com/panyam/chakra/examples/agents/common"
)

// All lists the agents, in the order the exercise runs them.
var All = []common.Agent{
	calc.Agent(),
	codeq.Agent(),
}

// Find returns the agent called name.
func Find(name string) (common.Agent, error) {
	for _, a := range All {
		if a.Name == name {
			return a, nil
		}
	}
	return common.Agent{}, fmt.Errorf("agents: no agent %q", name)
}
