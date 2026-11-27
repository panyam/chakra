package copilot

import "github.com/panyam/chakra/examples/agents/common"

// Agent is copilot as the exercise and the walk run it.
func Agent() common.Agent {
	return common.Agent{
		Name:         "copilot",
		Ladder:       Ladder,
		Instructions: Instructions,
		Scripted:     Scripted,
		About: []string{
			"copilot sits beside a kanban board with three columns: To do, Doing and Done. The user works on the board, and the agent sees what they do, reacts, and moves cards itself. The board is a fake page in Go, with tools to read the board, move, add and rename cards, and highlight one.",
			"Every case runs inside a host.App, as agentweb would serve the agent. The user's actions reach it as events through App.Deliver, and a trigger can start a turn nobody typed. App.Settle waits for those before the session moves on.",
			"A case is a user session: messages, actions between turns, and actions timed to land while the agent works. It's graded on the board afterwards, on whether the agent overwrote a change the user made while it worked, and on whether the trigger fired when it should, and once.",
		},
		StepNotes: map[string]string{
			"chat only":          "The App with no page tools. The agent can only talk.",
			"+ page tools":       "Tools to read the board and change it, and a line in the prompt saying to read before changing.",
			"+ page events":      "The page sends each user action to the App with App.Deliver. The host's injection policy puts it in the next turn's context, as a sentence the event's hint templates.",
			"+ trigger":          "A trigger on card.moved with to=Done starts a turn by itself: check the card's checklist, and move it back if items are open.",
			"+ critical events":  "The host injects events at turn start. The page now marks its events priority critical, so the host also passes one the user sends mid-turn before the agent's next model call.",
			"+ stale-edit guard": "Tool middleware refuses an edit to a card the user changed after the agent last read the board, and tells the agent what changed. The refusal is a free result, so it costs no step.",
		},
	}
}
