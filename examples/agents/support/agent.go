package support

import "github.com/panyam/chakra/examples/agents/common"

// Agent is support as the exercise and the walk run it.
func Agent() common.Agent {
	return common.Agent{
		Name:         "support",
		Ladder:       Ladder,
		Instructions: Instructions,
		Scripted:     Scripted,
		About: []string{
			"support answers customers of Threadline, a small online shop. It has six policy pages (returns, refunds, shipping, address, warranty, handoff) and tools over an orders database: lookup_order, refund_order, change_address, and later escalate_to_human.",
			"A model plays each customer from a goal, as tau2-bench's simulated user does, so a conversation runs until the customer is satisfied or refused. A case is graded on the database afterwards (was the order refunded? did the address change?), or on the answer and the page it cites.",
			"Three of the eight customers ask for something the policy doesn't allow: a refund 45 days after delivery, a refund on a final sale, and an address change after shipping.",
		},
		StepNotes: map[string]string{
			"tools + policy": "The shop's tools, and a two-line policy summary in the prompt. The pages themselves aren't available.",
			"+ docs":         "A retrieval stage finds the two policy pages that share the most words with the customer's message, and puts them before the model's call.",
			"+ citations":    "The prompt asks for a [page] citation on any policy, and a turn-end hook sends back an answer that cites nothing.",
			"+ policy guard": "Tool middleware refuses a refund or an address change the policy doesn't allow, before it reaches the database, and tells the model why.",
			"+ handoff":      "escalate_to_human, and a rule for when to use it: legal action, safety problems, anything the pages don't cover.",
		},
	}
}
