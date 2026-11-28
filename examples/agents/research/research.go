// Package research is an agent that researches a question across sources
// and answers with citations (#355). Its sources are snapshots of real
// pages about Shetland's islands and lighthouses: Wikipedia, Wikivoyage, and
// older Wikipedia revisions. A search tool and a fetch tool read the
// snapshots, so runs repeat and need no network. The eval asks twenty
// questions with short answers, in the style of GAIA. A case passes when the
// answer is right and it cites a page that supports it.
package research

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// Instructions is the agent's system prompt on every step.
const Instructions = `You answer questions by researching pages with the search and fetch tools. Don't answer from memory.
Give a short answer, then the URLs of the pages that support it, each on its own line.`

// question is one case: its text, the answers that count as right, and
// the URLs of the pages that support an answer. A case passes on citations
// when it cites at least one of them.
type question struct {
	text    string
	answers []string
	support []string
	kind    string
	// rival is the wrong choice in a question that names two, such as
	// "Foula or Fetlar". The answer must name the right one first.
	rival string
}

// The kinds of question.
const (
	lookup   = "lookup"
	twoHop   = "two-hop"
	conflict = "conflict"
	compare  = "compare"
	count    = "count"
)

// state is what the current case owns: its notes, when the plan slot is
// on, and the meter for model calls its turn's Usage leaves out. Cases run
// one at a time, and each case's Setup makes fresh notes and zeroes the
// meter.
type state struct {
	mu    sync.Mutex
	slots *agent.Slots
	m     *meter
}

// meter returns the ladder's meter, wrapping p the first time.
func (s *state) meter(p agent.Provider) *meter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = &meter{Provider: p}
	}
	return s.m
}

// spent is what the meter counted for the case, keyed as the suite's model.
func (s *state) spent() map[string]agent.Usage {
	s.mu.Lock()
	m := s.m
	s.mu.Unlock()
	if m == nil {
		return nil
	}
	return map[string]agent.Usage{"": m.take()}
}

func (s *state) get() *agent.Slots {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slots
}

func (s *state) reset() error {
	slots, err := agent.NewSlots(agent.SlotSpec{
		Name:        "plan",
		Description: "Your plan: the sub-questions still to answer, and each fact found so far with the URL it came from.",
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.slots = slots
	if s.m != nil {
		s.m.take()
	}
	s.mu.Unlock()
	return nil
}

// Ladder returns research's steps over its questions.
func Ladder(context.Context) (eval.Ladder, func(), error) {
	c, err := loadCorpus()
	if err != nil {
		return eval.Ladder{}, nil, err
	}
	st := &state{}
	var cs []eval.SuiteCase
	for _, q := range questions {
		cs = append(cs, eval.SuiteCase{
			Scenario: eval.Scenario{Name: q.text, Turns: []string{q.text}},
			Setup:    func(context.Context) (eval.Setup, error) { return eval.Setup{Spent: st.spent}, st.reset() },
			Scorers: func(agent.Provider) []eval.Scorer {
				return []eval.Scorer{answerScorer(q), citesScorer(c, q), eval.NoError()}
			},
		})
	}
	tools, err := c.tools("")
	if err != nil {
		return eval.Ladder{}, nil, err
	}
	return eval.Ladder{
		Name: "research",
		Steps: []eval.Step{
			{Name: "search + fetch", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.Tools = tools
				return cfg, nil
			}},
			{Name: "+ citation check", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.TurnEnd = append(slices.Clip(cfg.TurnEnd), citationCheck(c))
				return cfg, nil
			}},
			{Name: "+ plan slot", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				return withPlan(cfg, st)
			}},
			{Name: "+ sub-agents", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				return withSubAgents(cfg, c, st)
			}},
			{Name: "+ critic", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.TurnEnd = append(slices.Clip(cfg.TurnEnd), critic(st.meter(cfg.Provider), c))
				return cfg, nil
			}},
			{Name: "+ cost cap", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.TreeBudget = agent.TreeBudget{MaxTokens: costCap}
				return cfg, nil
			}},
		},
		Cases: cs,
	}, func() {}, nil
}

// withPlan offers the plan slot as a tool and shows its notes before each
// model call.
func withPlan(cfg agent.RunnerConfig, st *state) (agent.RunnerConfig, error) {
	cfg.Instructions += "\n" + planRules
	cfg.Tools = &planTools{base: cfg.Tools, st: st}
	cfg.StepStages = append(slices.Clip(cfg.StepStages), agent.StepStage{
		Name:  "research.plan",
		Scope: agent.ScopeRequest,
		Run: func(ctx context.Context, info agent.StepInfo, view agent.StepView) (agent.StepView, error) {
			if slots := st.get(); slots != nil {
				return slots.StepStage().Run(ctx, info, view)
			}
			return view, nil
		},
	})
	return cfg, nil
}

const planRules = `Before searching, write your plan with write_slot, in the plan slot: the sub-questions the answer needs. As you find each fact, add it with its URL.`

var urlRE = regexp.MustCompile(`https?://[^\s<>()\[\]"'` + "`" + `]+(?:\([^\s)]*\)[^\s<>()\[\]"'` + "`" + `]*)?`)

// cited returns the URLs an answer names, with trailing punctuation cut.
func cited(text string) []string {
	var out []string
	for _, u := range urlRE.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,;:")
		if !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}

// read returns the URLs the turn's fetch results and sub-agent reports
// name. A URL that only a search result names doesn't count, since a
// snippet isn't the page.
func read(msgs []agent.Message) map[string]bool {
	names := map[string]string{}
	out := map[string]bool{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			names[tc.ID] = tc.Name
		}
		if m.Role != agent.RoleTool || names[m.ToolCallID] == "search" {
			continue
		}
		for _, u := range cited(m.Text) {
			out[canonical(u)] = true
		}
	}
	return out
}

// citationCheck refuses an answer that cites no URL, or cites a URL the
// turn never read, and says which. On the last step it accepts what it
// gets, so a refusal can't fail a turn that had an answer.
func citationCheck(c *corpus) agent.TurnEndHook {
	return func(_ context.Context, info agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
		if info.StepsLeft <= 1 {
			return agent.TurnEndVerdict{}, nil
		}
		urls := cited(info.Text)
		if len(urls) == 0 {
			return agent.TurnEndVerdict{Continue: "Your answer cites no page. Fetch the pages that support it and list their URLs after the answer."}, nil
		}
		seen := read(info.Messages)
		var unread []string
		for _, u := range urls {
			if !seen[canonical(u)] || c.page(u) == nil {
				unread = append(unread, u)
			}
		}
		if len(unread) > 0 {
			return agent.TurnEndVerdict{Continue: fmt.Sprintf("You cite %s without having fetched it this turn. Fetch each page you cite and check that it says what you claim, or drop it.", strings.Join(unread, ", "))}, nil
		}
		return agent.TurnEndVerdict{}, nil
	}
}

var normRE = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// norm lowercases s and turns each run of other characters into one space,
// with a space at each end, so a word matches only whole.
func norm(s string) string {
	return " " + strings.TrimSpace(normRE.ReplaceAllString(strings.ToLower(s), " ")) + " "
}

// answerText is the answer with its URLs taken out, so a number in a URL
// can't pass a case.
func answerText(text string) string {
	return urlRE.ReplaceAllString(text, " ")
}

// answerScorer passes when the answer names one of the right answers as
// whole words.
func answerScorer(q question) eval.Scorer {
	return scorer(func(r eval.Result) eval.Score {
		s := eval.Score{Name: "Answer", Want: q.answers[0]}
		if r.Turn == nil {
			s.Detail = "no answer"
			return s
		}
		got := norm(answerText(r.Turn.Text))
		for _, a := range q.answers {
			at := strings.Index(got, norm(a))
			if at < 0 {
				continue
			}
			if q.rival != "" {
				if r := strings.Index(got, norm(q.rival)); r >= 0 && r < at {
					s.Detail = "the answer names " + q.rival + " first"
					return s
				}
			}
			s.Pass, s.Value = true, 1
			return s
		}
		s.Detail = "the answer doesn't say " + q.answers[0]
		return s
	})
}

// citesScorer passes when the answer cites at least one page that supports
// it, and every URL it cites is a real page.
func citesScorer(c *corpus, q question) eval.Scorer {
	return scorer(func(r eval.Result) eval.Score {
		s := eval.Score{Name: "Cites", Want: strings.Join(q.support, " or ")}
		if r.Turn == nil {
			s.Detail = "no answer"
			return s
		}
		urls := cited(r.Turn.Text)
		for _, u := range urls {
			if c.page(u) == nil {
				s.Detail = "cites " + u + ", which isn't a page"
				return s
			}
		}
		for _, u := range urls {
			if slices.ContainsFunc(q.support, func(s string) bool { return canonical(s) == canonical(u) }) {
				s.Pass, s.Value = true, 1
				return s
			}
		}
		if len(urls) == 0 {
			s.Detail = "cites nothing"
		} else {
			s.Detail = "cites no page that supports the answer"
		}
		return s
	})
}

type scorer func(eval.Result) eval.Score

func (f scorer) Score(r eval.Result) eval.Score { return f(r) }
