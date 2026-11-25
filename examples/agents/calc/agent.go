package calc

import "github.com/panyam/chakra/examples/agents/common"

// Agent is calc as the exercise and the walk run it.
func Agent() common.Agent {
	return common.Agent{
		Name:         "calc",
		Ladder:       common.Static(Ladder),
		Instructions: Instructions,
		Scripted:     Scripted,
	}
}
