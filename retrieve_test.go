package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func fixedRetriever(name string, snippets ...Snippet) Retriever {
	return NewRetriever(name, func(context.Context, RetrievalQuery) ([]Snippet, error) { return snippets, nil })
}

// retrieveRun runs one turn over a stub that calls one tool and then
// answers, with the retrieval stage on.
func retrieveRun(t *testing.T, cfg RetrievalConfig, history ...Message) (*StubProvider, *TurnResult, []Event) {
	t.Helper()
	src := NewFuncSource()
	AddFunc(src, "look", "looks", func(context.Context, struct{}) (string, error) { return "seen", nil })
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{call("c1", "look", `{}`)}},
		StubTurn{Text: "done"},
	)
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: src, StepStages: []StepStage{NewRetrievalStage(cfg)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 {
		history = []Message{{Role: RoleUser, Text: "fix the parser"}}
	}
	var events []Event
	res, err := r.Run(context.Background(), history, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	return stub, res, events
}

func retrievals(events []Event) []RetrievalInfo {
	var out []RetrievalInfo
	for _, e := range events {
		if e.Kind == EventRetrieved {
			out = append(out, *e.Retrieval)
		}
	}
	return out
}

func TestRetrievalInjectsByScoreWithSources(t *testing.T) {
	var asked RetrievalQuery
	repo := NewRetriever("repo", func(_ context.Context, q RetrievalQuery) ([]Snippet, error) {
		asked = q
		return []Snippet{
			{Source: "parse.go:10", Text: "func Parse()", Score: 0.4},
			{Source: "lex.go:3", Text: "func Lex()", Score: 0.9},
		}, nil
	})
	docs := fixedRetriever("docs", Snippet{Source: "README.md", Text: "Parse reads a file.", Score: 0.7})
	stub, _, events := retrieveRun(t, RetrievalConfig{Retrievers: []Retriever{repo, docs}, Budget: 1000})

	if asked.Task != "fix the parser" || asked.Budget != 1000 {
		t.Fatalf("query = %+v, want the user's task and the budget", asked)
	}
	reqs := stub.Requests()
	msgs := reqs[0].Messages
	if len(msgs) != 2 || msgs[0].Role != RoleSystem || msgs[1].Role != RoleUser {
		t.Fatalf("step 1 messages = %+v, want the context block then the user message", msgs)
	}
	block := msgs[0].Text
	lex, readme, parse := strings.Index(block, "lex.go:3"), strings.Index(block, "README.md"), strings.Index(block, "parse.go:10")
	if lex < 0 || readme < 0 || parse < 0 || !(lex < readme && readme < parse) {
		t.Fatalf("block not in score order with sources:\n%s", block)
	}
	if !strings.Contains(block, "func Lex()") {
		t.Fatalf("block lacks snippet text:\n%s", block)
	}
	got := retrievals(events)
	if len(got) != 1 || len(got[0].Injected) != 3 || got[0].Injected[0].Source != "lex.go:3" || got[0].Injected[0].Retriever != "repo" {
		t.Fatalf("retrieval events = %+v, want one listing three snippets, lex.go first from repo", got)
	}
}

func TestRetrievalReachesEveryStepButNotTheRecord(t *testing.T) {
	stub, res, _ := retrieveRun(t, RetrievalConfig{Retrievers: []Retriever{fixedRetriever("repo", Snippet{Source: "a.go", Text: "A", Score: 1})}})
	reqs := stub.Requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	for i, req := range reqs {
		n := 0
		for _, m := range req.Messages {
			if strings.Contains(m.Text, "a.go") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("request %d carries the block %d times, want once", i+1, n)
		}
	}
	for _, m := range res.Messages {
		if strings.Contains(m.Text, "a.go") {
			t.Fatalf("the context block joined the record: %+v", m)
		}
	}
}

func TestRetrievalDropsLowScoresOverBudget(t *testing.T) {
	big := strings.Repeat("x", 400) // 100 tokens at 4 chars a token
	small := strings.Repeat("y", 40)
	r := fixedRetriever("repo",
		Snippet{Source: "low.go", Text: big, Score: 0.1},
		Snippet{Source: "high.go", Text: big, Score: 0.9},
		Snippet{Source: "tiny.go", Text: small, Score: 0.05},
	)
	stub, _, events := retrieveRun(t, RetrievalConfig{Retrievers: []Retriever{r}, Budget: 150})
	block := stub.Requests()[0].Messages[0].Text
	if !strings.Contains(block, "high.go") || strings.Contains(block, "low.go") || !strings.Contains(block, "tiny.go") {
		t.Fatalf("want high.go and tiny.go in, low.go out:\n%s", block)
	}
	info := retrievals(events)[0]
	if info.Tokens > 150 || len(info.Dropped) != 1 || info.Dropped[0].Source != "low.go" {
		t.Fatalf("info = %+v, want low.go dropped and tokens within 150", info)
	}
}

func TestRetrievalSkipsAFailingRetriever(t *testing.T) {
	bad := NewRetriever("index", func(context.Context, RetrievalQuery) ([]Snippet, error) { return nil, errors.New("index offline") })
	good := fixedRetriever("repo", Snippet{Source: "a.go", Text: "A", Score: 1})
	stub, _, events := retrieveRun(t, RetrievalConfig{Retrievers: []Retriever{bad, good}})
	if !strings.Contains(stub.Requests()[0].Messages[0].Text, "a.go") {
		t.Fatal("the working retriever's snippet is missing")
	}
	info := retrievals(events)[0]
	if len(info.Failed) != 1 || info.Failed[0].Retriever != "index" || info.Failed[0].Error != "index offline" {
		t.Fatalf("failed = %+v, want index offline noted", info.Failed)
	}
}

func TestRetrievalRunsOncePerTurn(t *testing.T) {
	calls := 0
	r := NewRetriever("repo", func(context.Context, RetrievalQuery) ([]Snippet, error) {
		calls++
		return []Snippet{{Source: "a.go", Text: "A", Score: 1}}, nil
	})
	retrieveRun(t, RetrievalConfig{Retrievers: []Retriever{r}})
	if calls != 1 {
		t.Fatalf("retriever called %d times in a two-step turn, want 1", calls)
	}
}

func TestRetrievalSitsBeforeTheLastUserMessage(t *testing.T) {
	history := []Message{
		{Role: RoleUser, Text: "earlier"},
		{Role: RoleAssistant, Text: "ok"},
		{Role: RoleUser, Text: "fix the parser"},
	}
	var asked string
	r := NewRetriever("repo", func(_ context.Context, q RetrievalQuery) ([]Snippet, error) {
		asked = q.Task
		return []Snippet{{Source: "a.go", Text: "A", Score: 1}}, nil
	})
	stub, _, _ := retrieveRun(t, RetrievalConfig{Retrievers: []Retriever{r}, AsUser: true}, history...)
	msgs := stub.Requests()[0].Messages
	if asked != "fix the parser" {
		t.Fatalf("task = %q, want the last user message", asked)
	}
	if len(msgs) != 4 || msgs[2].Role != RoleUser || !strings.Contains(msgs[2].Text, "a.go") || msgs[3].Text != "fix the parser" {
		t.Fatalf("messages = %+v, want the block as a user message just before the last one", msgs)
	}
}

func TestRetrievalWithNothingFoundAddsNothing(t *testing.T) {
	stub, _, events := retrieveRun(t, RetrievalConfig{Retrievers: []Retriever{fixedRetriever("repo")}})
	if n := len(stub.Requests()[0].Messages); n != 1 {
		t.Fatalf("step 1 has %d messages, want only the user's", n)
	}
	if got := retrievals(events); len(got) != 1 || len(got[0].Injected) != 0 {
		t.Fatalf("events = %+v, want one empty retrieval record", got)
	}
}

func TestRetrievalInfoIsWireSafe(t *testing.T) {
	_, _, events := retrieveRun(t, RetrievalConfig{Retrievers: []Retriever{fixedRetriever("repo", Snippet{Source: "a.go", Text: "A", Score: 0.5})}})
	for _, e := range events {
		if e.Kind != EventRetrieved {
			continue
		}
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var back Event
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if back.Retrieval == nil || back.Retrieval.Injected[0].Source != "a.go" || back.Retrieval.Injected[0].Score != 0.5 || !strings.Contains(back.Retrieval.Block, "A") {
			t.Fatalf("round trip lost the record: %s", raw)
		}
		return
	}
	t.Fatal("no retrieval event")
}

func TestRetrievalKeepsOneCopyOfADuplicate(t *testing.T) {
	a := fixedRetriever("repo", Snippet{Source: "a.go", Text: "A", Score: 0.3})
	b := fixedRetriever("index", Snippet{Source: "a.go", Text: "A", Score: 0.8})
	stub, _, events := retrieveRun(t, RetrievalConfig{Retrievers: []Retriever{a, b}})
	if n := strings.Count(stub.Requests()[0].Messages[0].Text, "a.go"); n != 1 {
		t.Fatalf("a.go appears %d times, want once", n)
	}
	got := retrievals(events)[0].Injected
	if len(got) != 1 || got[0].Score != 0.8 || got[0].Retriever != "index" {
		t.Fatalf("injected = %+v, want one a.go at the higher score, from index", got)
	}
}

// A host stage after retrieval rebuilds the messages on every step, which
// drops the block. The host keeps the block from EventRetrieved and puts it
// back, so every step still sends it (#320).
func TestAStageThatRebuildsTheMessagesCanPutTheBlockBack(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "look", "looks", func(context.Context, struct{}) (string, error) { return "seen", nil })
	stub := NewStubProvider(StubTurn{ToolCalls: []ToolCall{call("c1", "look", `{}`)}}, StubTurn{Text: "done"})
	question := Message{Role: RoleUser, Text: "fix the parser"}
	var kept string
	rebuild := StepStage{Name: "host.rebuild", Scope: ScopeTurn, Run: func(_ context.Context, _ StepInfo, v StepView) (StepView, error) {
		msgs := []Message{question}
		if kept != "" {
			msgs = []Message{{Role: RoleSystem, Text: kept}, question}
		}
		v.Messages = append(msgs, v.Messages[slices.IndexFunc(v.Messages, func(m Message) bool { return m.Role == RoleUser && m.Text == question.Text })+1:]...)
		return v, nil
	}}
	retrieval := NewRetrievalStage(RetrievalConfig{Retrievers: []Retriever{fixedRetriever("repo", Snippet{Source: "a.go", Text: "func A()", Score: 0.5})}})
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: src, StepStages: []StepStage{retrieval, rebuild}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), []Message{question}, func(e Event) {
		if e.Kind == EventRetrieved {
			kept = e.Retrieval.Block
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kept, "func A()") {
		t.Fatalf("EventRetrieved carried block %q", kept)
	}
	for i, req := range stub.Requests() {
		if !slices.ContainsFunc(req.Messages, func(m Message) bool { return m.Text == kept }) {
			t.Errorf("step %d sent no block: %+v", i+1, req.Messages)
		}
	}
}
