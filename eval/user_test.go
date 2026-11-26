package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/chakra"
)

// A scenario's User picks each message after the scripted Turns, sees the
// conversation so far, and ends the run when it says it's done.
func TestScenarioUserDrivesTheConversation(t *testing.T) {
	var seen []string
	s := Scenario{
		Name:  "refund",
		Turns: []string{"I want a refund for order 7."},
		User: func(_ context.Context, conv []agent.Message) (string, bool, error) {
			last := conv[len(conv)-1]
			seen = append(seen, last.Text)
			if strings.Contains(last.Text, "Shall I go ahead?") {
				return "Yes, please.", false, nil
			}
			return "", true, nil
		},
	}
	p := agent.NewStubProvider(agent.StubTurn{Text: "Order 7 qualifies. Shall I go ahead?"}, agent.StubTurn{Text: "Refunded."})
	results, err := RunScenario(context.Background(), agent.RunnerConfig{Provider: p}, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[1].Case.Input != "Yes, please." || results[1].Turn.Text != "Refunded." {
		t.Fatalf("results = %+v, want the user's yes as a second turn", results)
	}
	if len(seen) != 2 {
		t.Errorf("User saw %v, want a call after each agent turn", seen)
	}
}

// MaxUserTurns caps a user that never says it's done.
func TestScenarioUserIsCapped(t *testing.T) {
	s := Scenario{
		Name:         "chatty",
		Turns:        []string{"hi"},
		MaxUserTurns: 2,
		User:         func(context.Context, []agent.Message) (string, bool, error) { return "and another thing", false, nil },
	}
	p := agent.NewStubProvider(agent.StubTurn{Text: "a"}, agent.StubTurn{Text: "b"}, agent.StubTurn{Text: "c"}, agent.StubTurn{Text: "d"})
	results, err := RunScenario(context.Background(), agent.RunnerConfig{Provider: p}, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("%d turns, want the scripted one plus 2 from the user", len(results))
	}
}

// ModelUser asks a model to play the customer: the goal is its system
// prompt, the agent's replies are its user messages, and DONE ends it.
func TestModelUserPlaysTheCustomer(t *testing.T) {
	p := &recorder{reply: "Yes, go ahead."}
	u := ModelUser(p, "Get order 7 refunded.")
	next, done, err := u(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Text: "I want a refund for order 7."},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "1", Name: "lookup_order"}}},
		{Role: agent.RoleTool, Text: "order 7: delivered"},
		{Role: agent.RoleAssistant, Text: "Shall I go ahead?"},
	})
	if err != nil || done || next != "Yes, go ahead." {
		t.Fatalf("= %q, %v, %v", next, done, err)
	}
	req := p.req
	if !strings.Contains(req.Instructions, "Get order 7 refunded.") {
		t.Errorf("instructions lack the goal: %q", req.Instructions)
	}
	if len(req.Messages) != 3 || req.Messages[0].Role != agent.RoleUser ||
		req.Messages[1].Role != agent.RoleAssistant || req.Messages[1].Text != "I want a refund for order 7." ||
		req.Messages[2].Role != agent.RoleUser || req.Messages[2].Text != "Shall I go ahead?" {
		t.Errorf("messages = %+v, want an opening user message, then the customer's as assistant and the agent's text as user, tools left out", req.Messages)
	}
	for _, reply := range []string{" DONE ", "DONE\nThanks for your help!", "DONE."} {
		p.reply = reply
		if _, done, _ := u(context.Background(), nil); !done {
			t.Errorf("%q didn't end the conversation", reply)
		}
	}
}

type recorder struct {
	reply string
	req   agent.ProviderRequest
}

func (r *recorder) Generate(_ context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	r.req = req
	return &agent.ProviderResponse{Text: r.reply}, nil
}

func (r *recorder) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return agent.NewStubProvider(agent.StubTurn{Text: r.reply}).Stream(ctx, req)
}

// A suite case's User gets the provider its turns run on, as Scorers does,
// so a model-played customer can use the suite's model.
func TestSuiteCaseUserGetsTheProvider(t *testing.T) {
	p := agent.NewStubProvider(agent.StubTurn{Text: "Shall I go ahead?"}, agent.StubTurn{Text: "Refunded."})
	var got agent.Provider
	suite := Suite{
		Config: agent.RunnerConfig{Provider: p},
		Cases: []SuiteCase{{
			Scenario: Scenario{Name: "refund", Turns: []string{"Refund order 7."}},
			User: func(up agent.Provider) User {
				got = up
				asked := false
				return func(context.Context, []agent.Message) (string, bool, error) {
					if asked {
						return "", true, nil
					}
					asked = true
					return "Yes.", false, nil
				}
			},
			Scorers: func(agent.Provider) []Scorer { return []Scorer{Contains("Refunded")} },
		}},
	}
	r := suite.Run(context.Background())
	if got != agent.Provider(p) {
		t.Errorf("User got %v, want the suite's provider", got)
	}
	if r.Passed != 1 {
		t.Errorf("the user's yes didn't reach the agent: %s", r)
	}
}
