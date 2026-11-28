package research

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Scripted is a stand-in model for CI. It knows each question's answer and
// supporting pages, and has four habits, one for each step to correct.
// They copy, roughly, what Haiku 5.5 and the README's calibration runs
// showed a model doing wrong:
//
//   - On a question that needs a page in between, it answers from the
//     search results and cites the first one, unfetched. The citation
//     check sends it back to fetch the page, and then it cites the right
//     one.
//   - On a sum or comparison over two pages, it answers once it has read
//     the first page, unless it has notes. With the plan slot it writes
//     down the first page's fact and reads the second.
//   - It searches only Wikipedia, so it never finds a fact that only
//     Wikivoyage has, until the sub-agents search every source.
//   - On a census question it reads the archived revision, which it
//     finds first, and gives its older figure. The critic sends it back
//     to the current page.
//
// The cost cap changes nothing, since the stand-in reports no usage. So
// each step passes more in CI, with no model. Only a live run says what a
// step does for a real one.
func Scripted() agent.Provider { return scripted{} }

type scripted struct{}

func (s scripted) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return agent.NewStubProvider(s.reply(req)).Stream(ctx, req)
}

func (s scripted) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return agent.NewStubProvider(s.reply(req)).Generate(ctx, req)
}

var (
	scriptedCorpus     *corpus
	scriptedCorpusOnce sync.Once
)

func theCorpus() *corpus {
	scriptedCorpusOnce.Do(func() { scriptedCorpus, _ = loadCorpus() })
	return scriptedCorpus
}

// seen is what a conversation has done so far.
type seen struct {
	question  *question
	fetched   []string
	searched  string
	reports   string
	wrote     bool
	refused   bool
	reviewed  bool
	hasNotes  bool
	toolNames map[string]bool
}

func look(req agent.ProviderRequest) seen {
	var s seen
	s.toolNames = map[string]bool{}
	for _, t := range req.Tools {
		s.toolNames[t.Name] = true
	}
	names := map[string]string{}
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			names[tc.ID] = tc.Name
			switch tc.Name {
			case "fetch":
				var a fetchArgs
				_ = json.Unmarshal(tc.Args.Raw(), &a)
				s.fetched = append(s.fetched, canonical(a.URL))
			case agent.SlotToolName:
				s.wrote = true
			}
		}
		switch m.Role {
		case agent.RoleUser:
			if s.question == nil {
				s.question = find(m.Text)
			}
			s.refused = s.refused || refusal(m.Text)
			s.reviewed = s.reviewed || strings.Contains(m.Text, "A reviewer read")
		case agent.RoleSystem:
			s.refused = s.refused || refusal(m.Text)
			s.reviewed = s.reviewed || strings.Contains(m.Text, "A reviewer read")
			s.hasNotes = s.hasNotes || strings.HasPrefix(m.Text, "Your notes")
		case agent.RoleTool:
			switch names[m.ToolCallID] {
			case "search":
				s.searched = m.Text
			case "research":
				s.reports += m.Text
			}
		}
	}
	return s
}

func refusal(text string) bool {
	return strings.Contains(text, "cites no page") || strings.Contains(text, "without having fetched")
}

// find is the question a task names.
func find(text string) *question {
	for i := range questions {
		if strings.Contains(text, questions[i].text) {
			return &questions[i]
		}
	}
	return nil
}

func (q *question) census() bool { return strings.Contains(q.text, "census") }

// onlyVoyage reports whether a page the answer needs is on Wikivoyage.
func (q *question) needsVoyage() bool {
	return slices.ContainsFunc(q.support, func(u string) bool { return strings.HasPrefix(u, wv) })
}

// archived is the archive page for a question's first supporting page.
func (q *question) archived() string {
	c := theCorpus()
	if c == nil {
		return ""
	}
	title := theCorpus().page(q.support[0]).title
	for _, p := range c.pages {
		if p.source == archive && strings.HasPrefix(p.title, title+" (") {
			return p.url
		}
	}
	return ""
}

func call(name string, args map[string]any) agent.StubTurn {
	b, _ := json.Marshal(args)
	return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: name + fmt.Sprint(len(b)), Name: name, Args: core.NewRawJSON(b)}}}
}

func answer(text string, urls ...string) agent.StubTurn {
	return agent.StubTurn{Text: text + "\n" + strings.Join(urls, "\n")}
}

var childRE = regexp.MustCompile(`You research one task in the (\w+) pages`)

func (scripted) reply(req agent.ProviderRequest) agent.StubTurn {
	if len(req.Messages) == 1 && strings.HasPrefix(req.Messages[0].Text, "You check a researcher's answer") {
		return critique(req.Messages[0].Text)
	}
	s := look(req)
	if s.question == nil {
		return agent.StubTurn{Text: "I don't know."}
	}
	if m := childRE.FindStringSubmatch(req.Instructions); m != nil {
		return child(s, m[1])
	}
	return parent(s)
}

// critique says an answer citing an archived revision is unsupported.
func critique(prompt string) agent.StubTurn {
	if strings.Contains(prompt, "oldid=") {
		return agent.StubTurn{Text: "UNSUPPORTED. The answer relies on an older revision, and the current page gives a newer figure."}
	}
	return agent.StubTurn{Text: "SUPPORTED"}
}

// child researches one source: it fetches each page there that bears on
// the question, then reports them.
func child(s seen, source string) agent.StubTurn {
	q := s.question
	var want []string
	for _, u := range q.support {
		if theCorpus().page(u).source == source {
			want = append(want, u)
		}
	}
	if source == archive && q.census() {
		if a := q.archived(); a != "" {
			want = append(want, a)
		}
	}
	for _, u := range want {
		if !slices.Contains(s.fetched, canonical(u)) {
			return call("fetch", map[string]any{"url": u})
		}
	}
	if len(want) == 0 {
		return agent.StubTurn{Text: "The " + source + " pages say nothing on this."}
	}
	var b strings.Builder
	for _, u := range want {
		if strings.Contains(u, "oldid=") {
			fmt.Fprintf(&b, "An older figure, from %s\n", u)
		} else {
			fmt.Fprintf(&b, "%s, from %s\n", q.answers[0], u)
		}
	}
	return agent.StubTurn{Text: b.String()}
}

// parent answers the question with the tools it has.
func parent(s seen) agent.StubTurn {
	q := s.question
	if s.toolNames["research"] {
		if s.reports == "" {
			return call("research", map[string]any{"task": q.text})
		}
		if q.census() && !s.reviewed {
			if a := q.archived(); a != "" && strings.Contains(s.reports, a) {
				return answer("An older figure, from the archived page.", a)
			}
		}
		return answer(q.answers[0]+".", q.support...)
	}
	if s.searched == "" {
		return call("search", map[string]any{"query": q.text, "source": wikipedia})
	}
	if q.needsVoyage() {
		return agent.StubTurn{Text: "The Wikipedia pages don't say."}
	}
	if q.kind == twoHop && !s.refused {
		if u := cited(s.searched); len(u) > 0 {
			return answer(q.answers[0]+".", u[0])
		}
	}
	if q.census() && !s.reviewed {
		if a := q.archived(); a != "" {
			if !slices.Contains(s.fetched, canonical(a)) {
				return call("fetch", map[string]any{"url": a})
			}
			return answer("An older figure, from the archived page.", a)
		}
	}
	if !slices.Contains(s.fetched, canonical(q.support[0])) {
		return call("fetch", map[string]any{"url": q.support[0]})
	}
	if len(q.support) > 1 && (q.kind == count || q.kind == compare) {
		if !s.toolNames[agent.SlotToolName] {
			return answer("I found only part of it.", q.support[0])
		}
		if !s.wrote {
			return call(agent.SlotToolName, map[string]any{"slot": "plan", "text": "First fact from " + q.support[0] + ". Next: " + q.support[1]})
		}
		if !slices.Contains(s.fetched, canonical(q.support[1])) {
			return call("fetch", map[string]any{"url": q.support[1]})
		}
	}
	return answer(q.answers[0]+".", q.support...)
}
