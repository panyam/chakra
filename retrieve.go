package agent

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Snippet is one piece of context a Retriever found for a task. Source
// says where it came from, such as a file and line, a document id, or a
// query, and the model sees it next to Text. Score ranks snippets against
// each other, higher first. Scores from different retrievers are compared
// as they are, so a host mixing retrievers puts them on one scale.
type Snippet struct {
	Source string
	Text   string
	Score  float64
}

// RetrievalQuery is what a Retriever is asked. Task is the text of the
// turn's last user message. Budget is the token budget for the whole
// injection, shared by every retriever in the stage, so a retriever can stop
// early once it has found that much. Returning more is fine, since the stage
// trims to the budget by score.
type RetrievalQuery struct {
	Task   string
	Budget int
}

// Retriever finds context for a task before the turn's first model call
// (#280). The contract has no index or embedding in it. A repo map, a
// vector store, a database probe or a graph query can each sit behind it.
//
// Name labels the retriever in EventRetrieved. Retrieve returns snippets in
// any order. An error skips this retriever for the turn, and the stage
// records it in RetrievalInfo.Failed, so a store that is down makes the
// answer worse without failing the turn. Retrievers in one stage run
// concurrently.
type Retriever interface {
	Name() string
	Retrieve(ctx context.Context, q RetrievalQuery) ([]Snippet, error)
}

// NewRetriever makes a Retriever from a name and a function.
func NewRetriever(name string, fn func(ctx context.Context, q RetrievalQuery) ([]Snippet, error)) Retriever {
	return funcRetriever{name: name, fn: fn}
}

type funcRetriever struct {
	name string
	fn   func(ctx context.Context, q RetrievalQuery) ([]Snippet, error)
}

func (f funcRetriever) Name() string { return f.name }

func (f funcRetriever) Retrieve(ctx context.Context, q RetrievalQuery) ([]Snippet, error) {
	return f.fn(ctx, q)
}

// defaultRetrievalBudget is RetrievalConfig.Budget when it is zero.
const defaultRetrievalBudget = 2000

// RetrievalConfig configures NewRetrievalStage.
type RetrievalConfig struct {
	// Retrievers are asked in parallel. Their snippets are merged into one
	// list by score.
	Retrievers []Retriever
	// Budget caps the injected snippets, in tokens as Estimator counts them.
	// Zero means 2000.
	Budget int
	// Estimator counts a snippet's tokens, source included. Nil means
	// CharTokenEstimator, about four characters a token.
	Estimator TokenEstimator
	// AsUser injects the block as a RoleUser message instead of a RoleSystem
	// one. Some chat templates reject a system message that isn't the first,
	// as with TurnEndVerdict.AsUser.
	AsUser bool
}

// NewRetrievalStage gathers context before a turn's first model call
// (#280). On step 1 it asks every retriever about the turn's last user
// message. It merges their snippets by score, highest first, and drops
// exact duplicates. Then it keeps snippets in that order while they fit the
// budget, skipping any that don't fit so a smaller one further down can
// still go in. The kept snippets go into one message, each under its source,
// placed just before the last user message so the user's words stay last.
//
// It is a ScopeTurn stage, so the block reaches every step of this turn but
// never joins TurnResult.Messages. The next turn retrieves afresh for its own
// task instead of carrying this one's context in history. Each run emits one
// EventRetrieved, listing what went in, what the budget dropped and which
// retrievers failed, so a trace shows what the model was given.
//
// The built-in Compactor runs before this stage on every step. So a block
// added on step 1 can be compacted on a later step, like any other message
// near the head. Keeping it just before the user message puts it in
// SummarizingCompactor's verbatim tail for as long as the tail reaches the
// user message.
//
// With no user message, or nothing found, it adds nothing.
func NewRetrievalStage(cfg RetrievalConfig) StepStage {
	if cfg.Budget <= 0 {
		cfg.Budget = defaultRetrievalBudget
	}
	if cfg.Estimator == nil {
		cfg.Estimator = CharTokenEstimator{}
	}
	return StepStage{
		Name:  "agent.retrieval",
		Scope: ScopeTurn,
		Run: func(ctx context.Context, info StepInfo, v StepView) (StepView, error) {
			if info.Step != 1 {
				return v, nil
			}
			at := -1
			for i := len(v.Messages) - 1; i >= 0; i-- {
				if v.Messages[i].Role == RoleUser {
					at = i
					break
				}
			}
			if at < 0 {
				return v, nil
			}
			block, rec := retrieve(ctx, cfg, RetrievalQuery{Task: v.Messages[at].Text, Budget: cfg.Budget})
			info.Emit(Event{Kind: EventRetrieved, Step: info.Step, Retrieval: rec})
			if block == "" {
				return v, nil
			}
			role := RoleSystem
			if cfg.AsUser {
				role = RoleUser
			}
			v.Messages = slices.Insert(v.Messages, at, Message{Role: role, Text: block})
			return v, nil
		},
	}
}

// RetrievalInfo is what a retrieval stage did, on EventRetrieved. It holds
// sources and scores but no snippet text, so an event stays small.
type RetrievalInfo struct {
	// Budget is the stage's token budget, and Tokens is what the injected
	// snippets used of it.
	Budget int `json:"budget"`
	Tokens int `json:"tokens"`
	// Injected lists the snippets that went in, in the order the model sees
	// them.
	Injected []RetrievedSnippet `json:"injected,omitempty"`
	// Dropped lists the snippets that didn't fit the budget.
	Dropped []RetrievedSnippet `json:"dropped,omitempty"`
	// Failed lists the retrievers that returned an error.
	Failed []RetrievalFailure `json:"failed,omitempty"`
}

// RetrievedSnippet records one snippet in RetrievalInfo.
type RetrievedSnippet struct {
	Retriever string  `json:"retriever"`
	Source    string  `json:"source"`
	Score     float64 `json:"score"`
	Tokens    int     `json:"tokens"`
}

// RetrievalFailure records a retriever that returned an error.
type RetrievalFailure struct {
	Retriever string `json:"retriever"`
	Error     string `json:"error"`
}

type scoredSnippet struct {
	Snippet
	retriever string
	tokens    int
}

// retrieve asks the retrievers, merges and trims their snippets, and returns
// the block to inject (empty when nothing fits) with its record.
func retrieve(ctx context.Context, cfg RetrievalConfig, q RetrievalQuery) (string, *RetrievalInfo) {
	found := make([][]Snippet, len(cfg.Retrievers))
	errs := make([]error, len(cfg.Retrievers))
	var wg sync.WaitGroup
	for i, r := range cfg.Retrievers {
		wg.Go(func() { found[i], errs[i] = r.Retrieve(ctx, q) })
	}
	wg.Wait()

	rec := &RetrievalInfo{Budget: cfg.Budget}
	var all []scoredSnippet
	seen := map[[2]string]int{}
	for i, r := range cfg.Retrievers {
		if errs[i] != nil {
			rec.Failed = append(rec.Failed, RetrievalFailure{Retriever: r.Name(), Error: errs[i].Error()})
			continue
		}
		for _, s := range found[i] {
			key := [2]string{s.Source, s.Text}
			if j, ok := seen[key]; ok {
				if s.Score > all[j].Score {
					all[j].Score, all[j].retriever = s.Score, r.Name()
				}
				continue
			}
			seen[key] = len(all)
			all = append(all, scoredSnippet{Snippet: s, retriever: r.Name()})
		}
	}
	slices.SortStableFunc(all, func(a, b scoredSnippet) int { return cmp.Compare(b.Score, a.Score) })

	var b strings.Builder
	for _, s := range all {
		s.tokens = cfg.Estimator.Estimate([]Message{{Text: s.Source + "\n" + s.Text}})
		entry := RetrievedSnippet{Retriever: s.retriever, Source: s.Source, Score: s.Score, Tokens: s.tokens}
		if rec.Tokens+s.tokens > cfg.Budget {
			rec.Dropped = append(rec.Dropped, entry)
			continue
		}
		rec.Tokens += s.tokens
		rec.Injected = append(rec.Injected, entry)
		if b.Len() == 0 {
			b.WriteString("Context gathered for this task, most relevant first. Each part names its source.\n")
		}
		fmt.Fprintf(&b, "\n[%d] %s\n%s\n", len(rec.Injected), s.Source, s.Text)
	}
	return b.String(), rec
}
