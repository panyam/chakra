package codeq

import "github.com/panyam/chakra/examples/agents/common"

// Agent is codeq as the exercise, the walk and agentchat run it.
func Agent() common.Agent {
	return common.Agent{
		Name:         "codeq",
		Ladder:       Ladder,
		Instructions: Instructions,
		Scripted:     Scripted,
		Chat:         chat,
	}
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
