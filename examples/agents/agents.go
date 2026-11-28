// Package agents lists chakra's agents for a job, each built as a ladder
// (mission #349). Every agent lives in its own directory with a README that
// walks through its steps, and a Makefile that includes common/agent.mk. The
// shared machinery is in common: Climb runs a ladder and prints its table,
// and Walk steps through one with demokit.
package agents

import (
	"fmt"

	"github.com/panyam/chakra/examples/agents/calc"
	"github.com/panyam/chakra/examples/agents/codefix"
	"github.com/panyam/chakra/examples/agents/codeq"
	"github.com/panyam/chakra/examples/agents/common"
	"github.com/panyam/chakra/examples/agents/copilot"
	"github.com/panyam/chakra/examples/agents/research"
	"github.com/panyam/chakra/examples/agents/support"
)

// All lists the agents, in the order the exercise runs them.
var All = []common.Agent{
	calc.Agent(),
	codeq.Agent(),
	codefix.Agent(),
	support.Agent(),
	copilot.Agent(),
	research.Agent(),
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
