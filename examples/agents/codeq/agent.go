package codeq

import (
	"context"

	"github.com/panyam/chakra/eval"
	"github.com/panyam/chakra/examples/agents/common"
	"github.com/panyam/demokit"
)

// Agent is codeq as the exercise, the walk and agentchat run it.
func Agent() common.Agent {
	return common.Agent{
		Name:         "codeq",
		Ladder:       Ladder,
		Ladders:      map[string]func(context.Context) (eval.Ladder, func(), error){"effort": EffortLadder},
		Instructions: Instructions,
		Scripted:     Scripted,
		Chat:         chat,
		About: []string{
			"codeq answers questions about Go code in two small modules, example.com/shop and example.com/ledger.",
			"Every answer is a set of rows: import paths, or qualified names such as example.com/shop/orders.Place. eval.Rows grades them.",
			"Three things trip a model. A question scoped to one module, where the tool also returns rows from the other. A question about non-test files, where one row comes only from a test. And a bare function name, which the callers tool refuses.",
		},
		StepNotes: map[string]string{
			"tools only":     "The model gets a two-line prompt and the five tools. Nothing tells it about the scoping rules.",
			"+ instructions": "The prompt now says which tool answers what, and spells out the two scoping rules.",
			"+ stuck policy": "A stuck detector watches for a call repeated three times, and adds a note to the next model call saying so. It matters when callers refuses a bare name and the model asks again.",
			"+ answer check": "A turn-end hook refuses an answer unless every line is a row the index knows, and sends the model back for rows alone.",
		},
		Walk: walk,
	}
}

// walk is the default walk with a section on the tool server after the
// intro.
func walk(d *demokit.Demo, w *common.Walker) {
	w.Intro(d)
	d.Section("The tool server",
		"cmd/codeserver is an MCP server. The ladder builds it, runs it as its own process, and connects over stdio.",
		"",
		"| tool | answers |",
		"|---|---|",
		"| packages | the packages of a module |",
		"| imports | what one package imports |",
		"| importers | what imports a package, from every module |",
		"| find | the qualified name of a function or type |",
		"| callers | what calls a function, from every module |",
	)
	w.Steps(d)
	w.Table(d)
}

// chat runs the last step in agentchat: its prompt, and codeserver on the
// fixture.
func chat() (common.ChatSetup, error) {
	cmd, args, dir, err := ChatServer()
	if err != nil {
		return common.ChatSetup{}, err
	}
	return common.ChatSetup{Instructions: ChatInstructions, Servers: []common.ChatServer{{ID: "codeq", Command: cmd, Args: args, Dir: dir}}}, nil
}
