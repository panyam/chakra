package longmemeval

import (
	"context"
	"fmt"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// SessionMemory reads a history one dated session at a time, and gives an
// agent tools to answer from it.
//
// It is the shape a memory design implements to be graded on LongMemEval
// through MemoryArm. A fact extractor, a per-session summarizer and raw-turn
// retrieval each implement the same two methods, so they get the same ingest
// loop, spend accounting and tool hand-off. The default memory mode is one
// implementation, over a MemoryStore (see DatasetAdapter.NewStore).
type SessionMemory interface {
	// Ingest reads one session into memory. It reports what that spent, by
	// model name, the way eval.Setup.Usage does, so "" means the suite's
	// ModelName. A memory that calls no model returns nil.
	Ingest(ctx context.Context, s Session) (map[string]agent.Usage, error)

	// Tools is what the agent answers the question with.
	Tools() agent.ToolSource
}

// Session is one haystack session.
type Session struct {
	// ID is the dataset's session id. When the dataset has none for this
	// position, it is "session-<index>".
	ID string
	// Date is the session's date as the dataset writes it, such as
	// "2023/05/20 (Sat) 02:21". Empty when the dataset gives none.
	Date string
	// Turns is the session's messages, in order.
	Turns []Turn
}

// MemoryArm returns a DatasetAdapter.Arm that grades a SessionMemory.
//
// For each instance it builds a fresh memory with newMemory, so nothing
// leaks from one question to the next. It ingests every haystack session in
// the dataset's order, and hands the agent the memory's tools. The arm sets
// no instructions, so the default ones carry the question's date.
//
// The spend of every Ingest call is summed into eval.Setup.Usage, which the
// report shows as the case's SetupUsage, apart from the question's own
// usage. When an Ingest fails, the arm returns the error along with the
// spend so far, and the suite fails the case and still reports that spend.
func MemoryArm(newMemory func() (SessionMemory, error)) func(context.Context, Instance) (eval.Setup, error) {
	return func(ctx context.Context, inst Instance) (eval.Setup, error) {
		mem, err := newMemory()
		if err != nil {
			return eval.Setup{}, fmt.Errorf("longmemeval: memory for %s: %w", inst.QuestionID, err)
		}
		var spent map[string]agent.Usage
		for _, s := range inst.sessions() {
			usage, err := mem.Ingest(ctx, s)
			spent = addSpend(spent, usage)
			if err != nil {
				return eval.Setup{Usage: spent}, fmt.Errorf("longmemeval: ingesting %s for %s: %w", s.ID, inst.QuestionID, err)
			}
		}
		return eval.Setup{Tools: mem.Tools(), Usage: spent}, nil
	}
}

// sessions lists the haystack as dated sessions, in the dataset's order.
func (i Instance) sessions() []Session {
	out := make([]Session, len(i.HaystackSessions))
	for idx, turns := range i.HaystackSessions {
		s := Session{ID: fmt.Sprintf("session-%d", idx), Turns: turns}
		if idx < len(i.HaystackSessionIDs) && i.HaystackSessionIDs[idx] != "" {
			s.ID = i.HaystackSessionIDs[idx]
		}
		if idx < len(i.HaystackDates) {
			s.Date = i.HaystackDates[idx]
		}
		out[idx] = s
	}
	return out
}

// addSpend adds one Ingest's usage into the running total, by model name.
func addSpend(total, add map[string]agent.Usage) map[string]agent.Usage {
	for model, u := range add {
		if total == nil {
			total = map[string]agent.Usage{}
		}
		total[model] = total[model].Add(u)
	}
	return total
}

// storeMemory is memory mode as a SessionMemory. It stores each session as
// one item in a MemoryStore, and the agent answers with the working-memory
// tools over that store.
//
// It stores one item per session rather than per turn. The benchmark scopes
// its evidence to sessions (answer_session_ids names sessions). Splitting a
// two-turn exchange into two items would separate the assistant's reply from
// the question it answers.
type storeMemory struct {
	store agent.MemoryStore
	tools agent.ToolSource
}

// newStoreMemory wraps a store from newStore, or an in-memory substring
// store when newStore is nil.
func newStoreMemory(newStore func() (agent.MemoryStore, error)) (*storeMemory, error) {
	var store agent.MemoryStore = agent.NewInMemoryMemoryStore()
	if newStore != nil {
		s, err := newStore()
		if err != nil {
			return nil, err
		}
		store = s
	}
	tools, err := agent.NewMemorySource(store)
	if err != nil {
		return nil, err
	}
	return &storeMemory{store: store, tools: tools}, nil
}

// Ingest puts the session into the store under its id. It calls no model,
// so it reports no spend.
func (m *storeMemory) Ingest(ctx context.Context, s Session) (map[string]agent.Usage, error) {
	_, err := m.store.PutMemory(ctx, agent.PutMemoryRequest{
		Item: agent.MemoryItem{Key: s.ID, Value: renderSession(s)},
	})
	return nil, err
}

// Tools is remember, recall and forget over the store.
func (m *storeMemory) Tools() agent.ToolSource { return m.tools }
