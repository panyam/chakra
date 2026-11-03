package host

import (
	"context"

	"github.com/panyam/chakra"
)

// ContextStage is one named step of pre-turn context assembly. It takes the
// messages so far and returns them with its own contribution folded in.
//
// A stage that has nothing to add returns its input unchanged. Stages do not
// report errors. Suppose a store is down or an embedder timed out. The
// producer that needs it contributes nothing, and the turn goes on. Context
// assembly is best-effort by nature, and a missing recall block makes the
// answer worse without breaking it.
type ContextStage struct {
	// Name identifies the stage in stageNames and in diagnostics. Use a
	// dotted form scoped to the contributor ("memory.recall") so a pipeline
	// listing says who added what.
	Name string

	// Run folds this stage's contribution into the messages so far.
	Run func(ctx context.Context, msgs []agent.Message) []agent.Message
}

// contextPipeline declares, in one place, how a turn's context is assembled
// and why it is assembled in that order.
//
// Before this existed the order was real but implicit, and it had grown
// across two layers. Host RunTurn drained events, appended the user message,
// then wove in the memory summary and recall. Meanwhile the Runner compacted
// at the top of its loop. Every individual ordering was defensible. Nobody
// designed the structure, though, and a new stage had no declared place to
// go.
//
// # The two phases
//
// Durable stages produce messages that join the session's history and
// persist. These are injected events, then the user's own message.
// Transient stages produce a per-turn view. The model sees it once and it is
// never written back. These are the memory summary and relevance recall.
//
// The split is structural rather than a convention, because a mistake here
// is silent and compounds. Imagine a transient block written into history.
// It would be re-injected next turn, then summarized alongside real
// conversation. Over time the scratchpad would become indistinguishable from
// what the user said. Two slices make that mistake unrepresentable instead of
// merely discouraged.
//
// # Order, and why
//
//	events -> user -> summary -> recall -> [compaction, Runner-side]
//
// Events precede the user message because they describe what happened before
// the user spoke. Recall is the most salient, because it was retrieved for
// this turn specifically, while the summary is ambient. So recall follows the
// summary and sits closest to the user message. Both are woven in just before
// the user message rather than appended after it, so the user's words remain
// the last thing the model reads.
//
// # Compaction runs in the Runner, and runs last
//
// Compaction is the terminal fit-to-budget stage. It is named here but not
// executed here. It runs on the Runner so that an eval can grade a
// compaction strategy without standing a host up. Naming it here keeps the
// pipeline honest about what happens to the messages after assemble returns.
//
// Compaction now runs last by decision, not because of where the code
// happened to sit. Consider the two orders. inject-then-compact guarantees
// the assembled context fits the budget. But a freshly retrieved block can
// fall into the compacted head and be summarized, which wastes a model call
// on content that was already a summary. compact-then-inject keeps
// retrievals verbatim. But injections can push the result back over the
// budget after it was fit, and the provider then fails the turn outright.
//
// A degraded answer beats a failed one, so inject-then-compact wins. The cost
// is real, though, and it has a sharp edge. SummarizingCompactor keeps its
// most recent KeepRecent messages verbatim. So injected blocks survive only
// while KeepRecent exceeds the number of blocks plus the user message. With a
// small KeepRecent and several producers, this turn's recall can be
// summarized the moment it is created. See RunnerConfig.Compactor.
//
// # Adding a stage
//
// Pick the phase by whether the stage's output should persist. Within the
// phase, place the stage after the stages it depends on. The
// injection-budget arbiter (#1024) would be a transient stage. It would run
// after every producer and before compaction. It waits until contention
// between producers is measurable enough to rank them on a common scale.
type contextPipeline struct {
	durable   []ContextStage
	transient []ContextStage
}

// runDurable applies the durable stages and appends the user's message,
// returning what the session should keep as its history.
//
// It is separate from runTransient, rather than one assemble call, because
// the host must do real work between the two phases. The run id is created
// lazily on the first persisted turn, and session-scoped memory derives its
// namespace from that id. If recall ran before the run existed, it would
// query the shared default scratchpad instead of this session's. The phases
// are two methods because a mandatory step sits between them, not because
// the pipeline wanted splitting.
func (p *contextPipeline) runDurable(ctx context.Context, prior []agent.Message, user agent.Message) []agent.Message {
	out := prior
	for _, st := range p.durable {
		out = st.Run(ctx, out)
	}
	return append(out, user)
}

// runTransient weaves the per-turn producers into a copy of history and
// returns what the model sees now. The result never aliases history's backing
// array, so the caller can keep one and send the other without either
// corrupting the other.
func (p *contextPipeline) runTransient(ctx context.Context, history []agent.Message) []agent.Message {
	out := make([]agent.Message, len(history))
	copy(out, history)
	for _, st := range p.transient {
		out = st.Run(ctx, out)
	}
	return out
}

// stageNames lists the pipeline in execution order, durable phase first, for
// diagnostics and tests that pin the declared order.
func (p *contextPipeline) stageNames() []string {
	out := make([]string, 0, len(p.durable)+len(p.transient))
	for _, st := range p.durable {
		out = append(out, st.Name)
	}
	for _, st := range p.transient {
		out = append(out, st.Name)
	}
	return out
}

// weaveBeforeUser inserts blocks as RoleSystem messages immediately before the
// final message, which by construction is the user's. Every transient
// producer shares it, so "closest to the user message is most salient" is
// implemented once rather than re-derived per stage.
func weaveBeforeUser(msgs []agent.Message, blocks []string) []agent.Message {
	if len(blocks) == 0 || len(msgs) == 0 {
		return msgs
	}
	n := len(msgs)
	out := make([]agent.Message, 0, n+len(blocks))
	out = append(out, msgs[:n-1]...)
	for _, b := range blocks {
		out = append(out, agent.Message{Role: agent.RoleSystem, Text: b})
	}
	out = append(out, msgs[n-1])
	return out
}
