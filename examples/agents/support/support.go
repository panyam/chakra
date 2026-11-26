// Package support is a customer-support agent for a small online shop
// (#354). It answers from six policy pages in docs/, and acts on a small
// orders database through tools: look up an order, refund it, change its
// address, and hand off to a person. A model plays each customer from a
// goal (eval.ModelUser), and a case is graded on what the database looks
// like afterwards, or on the answer and the page it cites.
package support

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/mcpkit/core"
)

// Instructions is the agent's system prompt on every step.
const Instructions = "You are the support agent for Threadline, an online shop. Help the customer with their order, and keep replies short."

// policy is what the first step puts in the prompt: the rules in brief,
// without the pages behind them.
const policy = `
Policy in brief: refunds are for orders delivered within 30 days, unopened and not final sale, and the customer has to confirm first. An address can change only while the order is processing.`

// citations is what the third step adds to the prompt.
const citations = `
When you state a policy, cite the page it comes from in brackets, such as [returns] or [refunds].`

// handoffRules is what the fifth step adds to the prompt.
const handoffRules = `
Use escalate_to_human when the customer mentions legal action, reports a safety problem, or asks for something the policy pages don't cover. Tell them a person will follow up.`

// customer is one case: what the customer says first, the goal a model
// plays them from, and how the case is graded.
type customer struct {
	opening string
	goal    string
	want    string
	check   func(s *shop, answer string) (bool, string)
}

func refunded(id string, want bool) func(*shop, string) (bool, string) {
	return func(s *shop, _ string) (bool, string) {
		o, _ := s.get(id)
		return o.Refunded == want, fmt.Sprintf("order %s refunded: %v", id, o.Refunded)
	}
}

func address(id, want string) func(*shop, string) (bool, string) {
	return func(s *shop, _ string) (bool, string) {
		o, _ := s.get(id)
		return o.Address == want, fmt.Sprintf("order %s ships to %q", id, o.Address)
	}
}

// answers passes when the agent's replies say one of the phrases and cite
// page.
func answers(page string, phrases ...string) func(*shop, string) (bool, string) {
	return func(_ *shop, answer string) (bool, string) {
		lower := strings.ToLower(answer)
		said := slices.ContainsFunc(phrases, func(p string) bool { return strings.Contains(lower, p) })
		cites := slices.Contains(cited(answer), page)
		return said && cites, fmt.Sprintf("says %v, cites [%s]: %v", said, page, cites)
	}
}

func escalated(s *shop, _ string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.escalations) > 0, fmt.Sprintf("%d hand-offs", len(s.escalations))
}

var customers = []customer{
	{"Hi, I'd like a refund for order A1.",
		"You are Ana. You want a refund for order A1, a wool scarf delivered 10 days ago. It's unopened. If the agent asks you to confirm, say yes.",
		"order A1 refunded", refunded("A1", true)},
	{"Can I get my money back for order A2? The jacket doesn't fit.",
		"You are Ben. You want a refund for order A2, a rain jacket. You got it about six weeks ago and haven't opened it. If asked to confirm, say yes.",
		"order A2 not refunded: 45 days since delivery", refunded("A2", false)},
	{"I want to return the boots from order A3.",
		"You are Cal. You want a refund for order A3, boots you bought on clearance 5 days ago. They're unopened. If asked to confirm, say yes.",
		"order A3 not refunded: final sale", refunded("A3", false)},
	{"Please send order A4 to 12 Oak St, Springfield instead.",
		"You are Dee. You want order A4 sent to 12 Oak St, Springfield.",
		`order A4 ships to "12 Oak St, Springfield"`, address("A4", "12 Oak St, Springfield")},
	{"I moved. Can you change the address on order A5 to 30 Main St, Capital City?",
		"You are Eve. You want order A5 sent to 30 Main St, Capital City.",
		`order A5 still ships to "5 Lake Dr, Capital City": it has shipped`, address("A5", "5 Lake Dr, Capital City")},
	{"How many days do I have to return something?",
		"You want to know how long you have to return an item. You don't have an order in mind.",
		"says 30 days, cites [returns]", answers("returns", "30 days", "30-day", "thirty days")},
	{"How long is the warranty on headphones?",
		"You want to know how long the warranty on headphones lasts. You don't have an order number to give.",
		"says one year, cites [warranty]", answers("warranty", "one year", "1 year", "12 months", "one-year", "1-year")},
	{"My kettle from order A6 sparked when I plugged it in. I'm talking to a lawyer.",
		"You are Fin. Your kettle from order A6 sparked, and you're angry and talking to a lawyer. You want a person to deal with it.",
		"handed off to a person", escalated},
}

// Ladder is support's five steps over its eight customers.
func Ladder(context.Context) (eval.Ladder, func(), error) {
	s := &shop{}
	s.reset()
	base, err := s.tools(false)
	if err != nil {
		return eval.Ladder{}, nil, err
	}
	withHandoff, err := s.tools(true)
	if err != nil {
		return eval.Ladder{}, nil, err
	}
	var cs []eval.SuiteCase
	for _, c := range customers {
		c := c
		cs = append(cs, eval.SuiteCase{
			Scenario: eval.Scenario{Name: c.opening, Turns: []string{c.opening}, MaxUserTurns: 4},
			Setup: func(context.Context) (eval.Setup, error) {
				s.reset()
				return eval.Setup{}, nil
			},
			User: func(p agent.Provider) eval.User {
				customer := eval.ModelUser(p, c.goal)
				return func(ctx context.Context, conversation []agent.Message) (string, bool, error) {
					s.heard(conversation)
					return customer(ctx, conversation)
				}
			},
			Scorers: func(agent.Provider) []eval.Scorer { return []eval.Scorer{outcome(s, c)} },
		})
	}
	return eval.Ladder{
		Name: "support",
		Steps: []eval.Step{
			{Name: "tools + policy", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.Tools = base
				cfg.Instructions += "\n" + policy
				return cfg, nil
			}},
			{Name: "+ docs", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.StepStages = append(slices.Clip(cfg.StepStages), agent.NewRetrievalStage(agent.RetrievalConfig{
					Retrievers: []agent.Retriever{agent.NewRetriever("policy pages", func(_ context.Context, q agent.RetrievalQuery) ([]agent.Snippet, error) {
						var out []agent.Snippet
						for i, d := range search(q.Task, 2) {
							out = append(out, agent.Snippet{Source: d.name, Text: "[" + d.name + "]\n" + d.text, Score: float64(2 - i)})
						}
						return out, nil
					})},
				}))
				return cfg, nil
			}},
			{Name: "+ citations", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.Instructions += "\n" + citations
				cfg.TurnEnd = append(slices.Clip(cfg.TurnEnd), citationCheck)
				return cfg, nil
			}},
			{Name: "+ policy guard", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.ToolMiddleware = append(slices.Clip(cfg.ToolMiddleware), policyGuard(s))
				return cfg, nil
			}},
			{Name: "+ handoff", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.Tools = withHandoff
				cfg.Instructions += "\n" + handoffRules
				return cfg, nil
			}},
		},
		Cases: cs,
	}, func() {}, nil
}

// outcome grades a case on the shop's state, or on the final answer.
func outcome(s *shop, c customer) eval.Scorer {
	return scorer(func(r eval.Result) eval.Score {
		// A customer often says thanks after getting an answer, so the
		// agent's last reply is "You're welcome". Every reply counts.
		s.mu.Lock()
		replies := slices.Clone(s.replies)
		s.mu.Unlock()
		if r.Turn != nil && (len(replies) == 0 || replies[len(replies)-1] != r.Turn.Text) {
			replies = append(replies, r.Turn.Text)
		}
		ok, detail := c.check(s, strings.Join(replies, "\n"))
		score := eval.Score{Name: "Outcome", Pass: ok, Detail: detail, Want: c.want}
		if ok {
			score.Value = 1
		}
		return score
	})
}

type scorer func(eval.Result) eval.Score

func (f scorer) Score(r eval.Result) eval.Score { return f(r) }

// citationCheck refuses an answer that cites no policy page, unless it's a
// question back to the customer or the last step.
func citationCheck(_ context.Context, info agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
	text := strings.TrimSpace(info.Text)
	if info.StepsLeft <= 1 || len(cited(text)) > 0 || strings.HasSuffix(text, "?") {
		return agent.TurnEndVerdict{}, nil
	}
	return agent.TurnEndVerdict{Continue: "Cite the policy page your answer relies on, in brackets, such as [returns] or [refunds]."}, nil
}

// policyGuard refuses a refund or an address change the policy doesn't
// allow, before it reaches the database, and tells the model why.
func policyGuard(s *shop) agent.ToolMiddleware {
	return func(ctx context.Context, info agent.ToolCallInfo, next agent.ToolCallFunc) (*core.ToolResult, error) {
		var args struct {
			OrderID string `json:"order_id"`
		}
		if info.Call.Name != "refund_order" && info.Call.Name != "change_address" {
			return next(ctx, info)
		}
		if err := info.Call.Args.Bind(&args); err != nil {
			return next(ctx, info)
		}
		o, ok := s.get(args.OrderID)
		if !ok {
			return next(ctx, info)
		}
		why := refundable(o)
		if info.Call.Name == "change_address" {
			why = readdressable(o)
		}
		if why == "" {
			return next(ctx, info)
		}
		res := core.ErrorResult(fmt.Sprintf("policy: %s on order %s isn't allowed: %s. Tell the customer, and don't retry.", info.Call.Name, o.ID, why))
		return &res, nil
	}
}
