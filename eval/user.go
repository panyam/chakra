package eval

import (
	"context"
	"strings"

	"github.com/panyam/chakra"
)

// userDone is what a ModelUser says to end the conversation.
const userDone = "DONE"

// ModelUser has p play the customer in a Scenario, as τ²-bench's simulated
// user does. goal is what the customer wants and knows, such as "Get order
// 7 refunded. You bought it 10 days ago and haven't opened it." It becomes
// the model's instructions.
//
// The model sees the conversation from the customer's side: its own
// earlier messages as assistant turns, and the agent's replies as user
// turns. Tool calls and results are the agent's own business and are left
// out. The model replies with the customer's next message, or DONE once the
// goal is met or can't be met.
func ModelUser(p agent.Provider, goal string) User {
	instructions := "You are a customer talking to a support agent. Stay in character, and write only what the customer says next: one short message, in plain words.\n\n" +
		"Your goal, and what you know:\n" + goal + "\n\n" +
		"Don't make up facts the goal doesn't give you. If the agent asks for something you don't know, say so. " +
		"When your goal is met, or the agent has clearly refused or handed you to a person, reply with exactly " + userDone + "."
	return func(ctx context.Context, conversation []agent.Message) (string, bool, error) {
		var msgs []agent.Message
		for _, m := range conversation {
			switch {
			case m.Role == agent.RoleUser:
				msgs = append(msgs, agent.Message{Role: agent.RoleAssistant, Text: m.Text})
			case m.Role == agent.RoleAssistant && strings.TrimSpace(m.Text) != "":
				msgs = append(msgs, agent.Message{Role: agent.RoleUser, Text: m.Text})
			}
		}
		// Some APIs, Anthropic's among them, need the first message to be the
		// user's, and from the customer's side the agent hasn't spoken yet.
		msgs = append([]agent.Message{{Role: agent.RoleUser, Text: "(The support chat has started. Write your first message.)"}}, msgs...)
		resp, err := p.Generate(ctx, agent.ProviderRequest{Instructions: instructions, Messages: msgs})
		if err != nil {
			return "", false, err
		}
		next := strings.TrimSpace(resp.Text)
		if next == userDone || next == "" {
			return "", true, nil
		}
		return next, false, nil
	}
}
