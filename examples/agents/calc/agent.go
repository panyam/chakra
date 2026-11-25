package calc

import "github.com/panyam/chakra/examples/agents/common"

// Agent is calc as the exercise and the walk run it.
func Agent() common.Agent {
	return common.Agent{
		Name:         "calc",
		Ladder:       common.Static(Ladder),
		Instructions: Instructions,
		Scripted:     Scripted,
		About: []string{
			"calc answers six arithmetic questions: a sum, a difference, and four products and quotients.",
			"It's the smallest ladder, there to show how a walk and its table read.",
		},
		StepNotes: map[string]string{
			"model only":  "The model answers from its head, with no tools.",
			"+ calc tool": "The model can call calc, which works out one operation on two integers.",
		},
	}
}
