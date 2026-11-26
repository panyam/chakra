package support

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Scripted is a stand-in model for CI. It plays both sides: when the
// request comes from eval.ModelUser it is the customer, who says yes to a
// question if the goal says to and is done otherwise. As the agent it
// knows the shop's tools, and has one habit for each step to correct:
//
//   - Without the policy pages it doesn't know the return window or the
//     warranty, so it says it isn't sure.
//   - It cites the warranty page unprompted, and the returns page only once
//     its instructions ask for citations.
//   - It never checks the policy before refunding or changing an address,
//     so only the policy guard stops it.
//   - Without escalate_to_human it can only apologise to the customer
//     talking to a lawyer.
//
// So each step passes more cases in CI, with no model. Only a live run says
// what a step does for a real one.
func Scripted() agent.Provider { return scripted{} }

type scripted struct{}

func (s scripted) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return agent.NewStubProvider(s.reply(req)).Stream(ctx, req)
}

func (s scripted) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return agent.NewStubProvider(s.reply(req)).Generate(ctx, req)
}

var orderID = regexp.MustCompile(`\bA\d\b`)

func (s scripted) reply(req agent.ProviderRequest) agent.StubTurn {
	if strings.HasPrefix(req.Instructions, "You are a customer") {
		return customerReply(req)
	}
	var opening, lastTool string
	lookedUp, asked, yes, acted := false, false, false, false
	for _, m := range req.Messages {
		switch m.Role {
		case agent.RoleUser:
			if opening == "" {
				opening = m.Text
			} else if asked && strings.Contains(strings.ToLower(m.Text), "yes") {
				yes = true
			}
		case agent.RoleAssistant:
			asked = asked || strings.Contains(m.Text, "Shall I")
			for _, tc := range m.ToolCalls {
				switch tc.Name {
				case "lookup_order":
					lookedUp = true
				case "refund_order", "change_address", "escalate_to_human":
					acted = true
				}
			}
		case agent.RoleTool:
			lastTool = m.Text
		}
	}
	docs := false
	for _, m := range req.Messages {
		docs = docs || strings.Contains(m.Text, "[returns]\n") || strings.Contains(m.Text, "[warranty]\n")
	}
	cite := strings.Contains(req.Instructions, "cite the page")
	handoff := false
	for _, t := range req.Tools {
		handoff = handoff || t.Name == "escalate_to_human"
	}
	page := func(name string) string {
		if cite {
			return " [" + name + "]"
		}
		return ""
	}
	call := func(name string, args map[string]any) agent.StubTurn {
		raw, _ := json.Marshal(args)
		return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: name, Args: core.NewRawJSON(raw)}}}
	}
	id := orderID.FindString(opening)
	refused := strings.Contains(lastTool, "policy:")
	lower := strings.ToLower(opening)

	switch {
	case strings.Contains(lower, "lawyer"):
		if !handoff {
			return agent.StubTurn{Text: "I'm sorry about the kettle. Please stop using it."}
		}
		if !acted {
			return call("escalate_to_human", map[string]any{"summary": "Kettle from order " + id + " sparked; the customer mentions a lawyer."})
		}
		return agent.StubTurn{Text: "I've handed this to a person, who'll reply within a day." + page("handoff")}
	case strings.Contains(lower, "return something"):
		if !docs {
			return agent.StubTurn{Text: "I'm not sure of the exact return window, sorry."}
		}
		return agent.StubTurn{Text: "You have 30 days from delivery to return an unopened item." + page("returns")}
	case strings.Contains(lower, "warranty"):
		if !docs {
			return agent.StubTurn{Text: "I'm not sure how long the warranty is, sorry."}
		}
		return agent.StubTurn{Text: "Headphones are electronics, so the warranty is one year from delivery [warranty]."}
	case id == "":
		return agent.StubTurn{Text: "Which order is this about?"}
	case !lookedUp:
		return call("lookup_order", map[string]any{"order_id": id})
	case strings.Contains(lower, "address") || strings.Contains(lower, "send order"):
		if !acted {
			addr := strings.TrimSuffix(strings.TrimSpace(opening[strings.LastIndex(opening, " to ")+4:]), "?")
			addr = strings.TrimSuffix(addr, " instead.")
			return call("change_address", map[string]any{"order_id": id, "address": addr})
		}
		if refused {
			return agent.StubTurn{Text: "Order " + id + " has already shipped, so its address can't change. The carrier can redirect it with the tracking number." + page("address")}
		}
		return agent.StubTurn{Text: "Done: order " + id + " will go to the new address." + page("address")}
	default:
		if !asked {
			return agent.StubTurn{Text: "I found order " + id + ". Shall I refund it in full?"}
		}
		if !yes {
			return agent.StubTurn{Text: "All right, I won't refund it."}
		}
		if !acted {
			return call("refund_order", map[string]any{"order_id": id})
		}
		if refused {
			return agent.StubTurn{Text: "I'm sorry, order " + id + " can't be refunded under our policy." + page("refunds")}
		}
		return agent.StubTurn{Text: "Order " + id + " is refunded. It reaches your card in 3 to 5 business days." + page("refunds")}
	}
}

// customerReply plays the customer: yes to a question when the goal says
// so, and done otherwise.
func customerReply(req agent.ProviderRequest) agent.StubTurn {
	if len(req.Messages) == 0 {
		return agent.StubTurn{Text: "DONE"}
	}
	last := strings.TrimSpace(req.Messages[len(req.Messages)-1].Text)
	if strings.HasSuffix(last, "?") && strings.Contains(req.Instructions, "say yes") {
		return agent.StubTurn{Text: "Yes, please."}
	}
	return agent.StubTurn{Text: "DONE"}
}
