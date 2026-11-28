package research

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/chakra/examples/agents/common"
)

// Every question's supporting pages are in the corpus, and unless the
// answer takes a sum or a comparison, one of them states it.
func TestEveryAnswerIsInASupportingPage(t *testing.T) {
	c, err := loadCorpus()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range questions {
		found := false
		for _, u := range q.support {
			p := c.page(u)
			if p == nil {
				t.Errorf("%q: no page %s", q.text, u)
				continue
			}
			for _, a := range q.answers {
				found = found || strings.Contains(norm(p.text), norm(a))
			}
		}
		if !found && q.kind != count && q.kind != compare {
			t.Errorf("%q: no supporting page says %q", q.text, q.answers[0])
		}
	}
}

func TestSearchFindsTheNamedPageFirst(t *testing.T) {
	c, err := loadCorpus()
	if err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]string{
		"Muckle Flugga lighthouse automated": wp + "Muckle_Flugga_Lighthouse",
		"Good Shepherd IV ferry built":       wp + "Good_Shepherd_IV",
		"Sule Skerry lighthouse":             wp + "Sule_Skerry",
	} {
		hits := c.search(query, "", 5)
		if len(hits) == 0 || hits[0].p.url != want {
			t.Errorf("search %q: want %s first, got %v", query, want, urls(hits))
		}
	}
	for _, h := range c.search("Foula ferry", wikivoyage, 5) {
		if h.p.source != wikivoyage {
			t.Errorf("search in wikivoyage returned %s", h.p.url)
		}
	}
}

func urls(hits []hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.p.url)
	}
	return out
}

func TestFetchRefusesAURLOutsideTheCorpus(t *testing.T) {
	c, err := loadCorpus()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := c.tools(wikivoyage)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://example.com/", wp + "Foula"} {
		r, err := tools.Call(context.Background(), "fetch", map[string]any{"url": u})
		if err == nil && (r == nil || !r.IsError) {
			t.Errorf("fetch %s from wikivoyage's tools: want an error, got %+v", u, r)
		}
	}
	r, err := tools.Call(context.Background(), "fetch", map[string]any{"url": wv + "Foula"})
	if err != nil || r.IsError {
		t.Fatalf("fetch %sFoula: %v %+v", wv, err, r)
	}
}

func turn(text string) eval.Result {
	return eval.Result{Turn: &agent.TurnResult{Text: text}}
}

func TestAnswerScorer(t *testing.T) {
	q := question{answers: []string{"17"}}
	for text, want := range map[string]bool{
		"17\n" + wp + "Foula":          true,
		"170 people":                   false,
		"See " + wp + "Foula?oldid=17": false,
	} {
		if got := answerScorer(q).Score(turn(text)).Pass; got != want {
			t.Errorf("answer %q: pass = %v, want %v", text, got, want)
		}
	}
	q = question{answers: []string{"Fetlar"}, rival: "Foula"}
	for text, want := range map[string]bool{
		"Fetlar had more than Foula":  true,
		"Foula had fewer than Fetlar": false,
	} {
		if got := answerScorer(q).Score(turn(text)).Pass; got != want {
			t.Errorf("answer %q: pass = %v, want %v", text, got, want)
		}
	}
}

func TestCitesScorer(t *testing.T) {
	c, err := loadCorpus()
	if err != nil {
		t.Fatal(err)
	}
	q := question{answers: []string{"17"}, support: []string{wp + "Foula"}}
	for text, want := range map[string]bool{
		"17\n" + wp + "Foula":  true,
		"17\n" + wp + "Foula.": true,
		"17":                   false,
		"17\n" + wv + "Foula":  false,
		"17\n" + wp + "Foula\nhttps://example.com/foula": false,
	} {
		if got := citesScorer(c, q).Score(turn(text)).Pass; got != want {
			t.Errorf("answer %q: pass = %v, want %v", text, got, want)
		}
	}
}

// The citation check refuses a URL that only a search result named, and
// accepts one a fetch returned.
func TestCitationCheckWantsFetchedPages(t *testing.T) {
	c, err := loadCorpus()
	if err != nil {
		t.Fatal(err)
	}
	u := wp + "Foula"
	msgs := func(tool string) []agent.Message {
		return []agent.Message{
			{Role: agent.RoleUser, Text: "q"},
			{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "1", Name: tool}}},
			{Role: agent.RoleTool, ToolCallID: "1", Text: "Foula\n" + u + "\n\ntext"},
		}
	}
	check := citationCheck(c)
	for _, tc := range []struct {
		name, text, tool string
		refuse           bool
	}{
		{"fetched", "17\n" + u, "fetch", false},
		{"only searched", "17\n" + u, "search", true},
		{"reported by a sub-agent", "17\n" + u, "research", false},
		{"cites nothing", "17", "fetch", true},
	} {
		v, err := check(context.Background(), agent.TurnEndInfo{StepsLeft: 3, Text: tc.text, Messages: msgs(tc.tool)})
		if err != nil {
			t.Fatal(err)
		}
		if (v.Continue != "") != tc.refuse {
			t.Errorf("%s: refused = %v, want %v (%s)", tc.name, v.Continue != "", tc.refuse, v.Continue)
		}
	}
}

func TestMeterCountsStreamedAndGeneratedUsage(t *testing.T) {
	turn := func() agent.StubTurn {
		return agent.StubTurn{Deltas: []agent.Delta{
			{Kind: agent.DeltaText, Text: "ok"},
			{Kind: agent.DeltaUsage, Usage: &agent.Usage{InputTokens: 10, OutputTokens: 2}},
			{Kind: agent.DeltaFinish, FinishReason: "stop"},
		}}
	}
	m := &meter{Provider: agent.NewStubProvider(turn(), turn())}
	s, err := m.Stream(context.Background(), agent.ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Recv(); err != nil {
			break
		}
	}
	if _, err := m.Generate(context.Background(), agent.ProviderRequest{}); err != nil {
		t.Fatal(err)
	}
	if u := m.take(); u.InputTokens != 20 || u.OutputTokens != 4 {
		t.Errorf("took %+v, want 20 in and 4 out", u)
	}
	if u := m.take(); u.InputTokens != 0 {
		t.Errorf("second take = %+v, want zero", u)
	}
}

func TestAURLFindsItsPageInAnyCommonSpelling(t *testing.T) {
	c, err := loadCorpus()
	if err != nil {
		t.Fatal(err)
	}
	want := c.page(wp + "David_Stevenson_(engineer)")
	if want == nil {
		t.Fatal("no page for David Stevenson")
	}
	for _, u := range []string{
		wp + "David_Stevenson_%28engineer%29",
		"http://en.wikipedia.org/wiki/David_Stevenson_(engineer)",
		wp + "David Stevenson (engineer)",
		wp + "David_Stevenson_(engineer)/",
	} {
		if c.page(u) != want {
			t.Errorf("%s: no page", u)
		}
	}
}

// Fetching a page part by part, from each part's next offset, gives back
// the whole page, with no part much over pageChunk characters.
func TestChunksCoverThePage(t *testing.T) {
	c, err := loadCorpus()
	if err != nil {
		t.Fatal(err)
	}
	p := c.page(wp + "Fair_Isle")
	var b strings.Builder
	off, parts := 0, 0
	for {
		part, next := chunk(p.text, off)
		if len(part) > pageChunk+4 {
			t.Errorf("part at %d is %d characters", off, len(part))
		}
		b.WriteString(part)
		parts++
		if next == 0 {
			break
		}
		if next <= off {
			t.Fatalf("next %d doesn't move past %d", next, off)
		}
		off = next
	}
	if b.String() != p.text {
		t.Errorf("the parts don't make up the page")
	}
	if parts < 5 {
		t.Errorf("Fair Isle came in %d parts, want it long enough to page", parts)
	}
}

// On the stand-in model each step corrects one of its habits, so the
// ladder climbs at every step but the cost cap.
func TestEachStepFixesAHabit(t *testing.T) {
	r, err := common.Climb(context.Background(), Agent(), common.Model{Provider: Scripted(), Name: "scripted"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{4, 8, 13, 16, 20, 20}
	for i, s := range r.Steps {
		if s.Err != "" {
			t.Fatalf("%s: %s", s.Step, s.Err)
		}
		if s.Report.Passed != want[i] {
			t.Errorf("%s: %d passed, want %d\n%s", s.Step, s.Report.Passed, want[i], r.Table())
		}
	}
}
