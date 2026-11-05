package agent

import (
	"context"
	"fmt"
	"strings"
)

// Compactor shrinks a turn's history before the model sees it, trading some
// fidelity for a smaller context. Tool-result offloading is lossless, and its
// cost comes only on lookup. Compaction is the lossy counterpart. Its cost is
// unconditional and it cannot be undone, so it is the tool of last resort for
// keeping a long conversation under a model's context budget.
//
// The Runner calls Compact before every model call. It compacts its working
// messages, not the history it was handed. So Run is still stateless over that
// history, and the turn's record keeps every message. A Compactor MUST be a
// pure function of its input, with no turn or session state. It MUST return
// the input unchanged when nothing needs compacting. The Runner detects a
// no-op by length and emits an EventCompaction only when the count actually
// drops.
type Compactor interface {
	Compact(ctx context.Context, history []Message) ([]Message, error)
}

// TokenEstimator estimates the token cost of a message slice so a Compactor
// can decide when history exceeds a budget. Real tokenizers are
// provider-specific and heavy. The default is a cheap character heuristic. A
// real tokenizer can drop in behind this seam, and a follow-up is filed for
// that. An estimate must be monotonic. More or longer messages never estimate
// fewer tokens.
type TokenEstimator interface {
	Estimate(msgs []Message) int
}

// defaultCharsPerToken approximates English tokenization (~4 characters per
// token) for CharTokenEstimator.
const defaultCharsPerToken = 4

// CharTokenEstimator estimates tokens as total text length divided by
// CharsPerToken. It counts message text plus tool-call argument JSON, because
// argument JSON actually bloats a long tool-using conversation. Zero
// CharsPerToken uses 4.
type CharTokenEstimator struct {
	CharsPerToken int
}

// Estimate implements TokenEstimator.
func (e CharTokenEstimator) Estimate(msgs []Message) int {
	per := e.CharsPerToken
	if per <= 0 {
		per = defaultCharsPerToken
	}
	chars := 0
	for _, m := range msgs {
		chars += len(m.Text)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Args.Raw())
		}
	}
	return chars / per
}

// defaultKeepRecent is how many trailing messages SummarizingCompactor keeps
// verbatim when no KeepRecent is configured. That is enough to preserve the
// active exchange and still collapse the older head.
const defaultKeepRecent = 6

// SummarizingConfig configures a SummarizingCompactor.
type SummarizingConfig struct {
	// Provider summarizes the head of the conversation. Required.
	Provider Provider

	// Estimator decides when history is over budget. Nil uses
	// CharTokenEstimator with the default ratio.
	Estimator TokenEstimator

	// MaxTokens is the budget. Compaction fires only when the estimate
	// exceeds it. Required (must be > 0).
	MaxTokens int

	// KeepRecent is how many trailing messages are kept verbatim. Zero uses
	// 6. The cut is nudged earlier if it would orphan a
	// tool-result message from its call, so the kept tail is always
	// self-contained.
	KeepRecent int

	// Instructions overrides the summarizer system prompt. Empty uses a
	// built-in prompt that asks for durable facts, decisions, and open
	// threads.
	Instructions string
}

// SummarizingCompactor collapses the head of an over-budget conversation into
// a single RoleSystem summary produced by a model, keeping a recent tail
// verbatim. It is the first, heuristic-budget compaction strategy. A
// token-accurate estimator is a follow-up.
type SummarizingCompactor struct {
	cfg SummarizingConfig
}

const defaultSummarizerInstructions = "You compress the earlier part of a conversation into a compact summary " +
	"an AI assistant will carry forward in place of the original messages. Preserve durable facts, decisions, " +
	"user preferences, names, numbers, identifiers, and any open threads or pending tasks. Write terse notes, " +
	"not prose, and do not invent anything that is not in the text."

// NewSummarizingCompactor validates cfg and returns the compactor. Provider
// is required and MaxTokens must be positive.
func NewSummarizingCompactor(cfg SummarizingConfig) (*SummarizingCompactor, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("agent: SummarizingCompactor needs a Provider")
	}
	if cfg.MaxTokens <= 0 {
		return nil, fmt.Errorf("agent: SummarizingCompactor needs MaxTokens > 0")
	}
	if cfg.Estimator == nil {
		cfg.Estimator = CharTokenEstimator{}
	}
	if cfg.KeepRecent <= 0 {
		cfg.KeepRecent = defaultKeepRecent
	}
	if cfg.Instructions == "" {
		cfg.Instructions = defaultSummarizerInstructions
	}
	return &SummarizingCompactor{cfg: cfg}, nil
}

// Compact implements Compactor. Under budget it is a no-op. Otherwise it
// summarizes the head into one RoleSystem message and prepends that message
// to the verbatim tail.
func (c *SummarizingCompactor) Compact(ctx context.Context, history []Message) ([]Message, error) {
	if c.cfg.Estimator.Estimate(history) <= c.cfg.MaxTokens {
		return history, nil
	}

	cut := len(history) - c.cfg.KeepRecent
	// Do not orphan a tool result from the assistant call that produced it.
	// Pull the cut earlier until the tail starts on a non-tool message.
	for cut > 0 && history[cut].Role == RoleTool {
		cut--
	}
	if cut <= 0 {
		// Nothing ahead of the tail can be summarized, because history is
		// short or all tool messages. Leave history untouched rather than
		// emit an empty summary.
		return history, nil
	}

	head, tail := history[:cut], history[cut:]
	summary, err := c.summarize(ctx, head)
	if err != nil {
		return nil, err
	}

	out := make([]Message, 0, 1+len(tail))
	out = append(out, Message{Role: RoleSystem, Text: summary})
	out = append(out, tail...)
	return out, nil
}

// Fits reports whether msgs are within MaxTokens by the configured estimator.
// The Runner uses it after a mid-turn compaction: if they still don't fit,
// compacting again on the next step would only summarize a summary, so the
// turn ends with ErrContextBudget instead.
func (c *SummarizingCompactor) Fits(msgs []Message) bool {
	return c.cfg.Estimator.Estimate(msgs) <= c.cfg.MaxTokens
}

func (c *SummarizingCompactor) summarize(ctx context.Context, head []Message) (string, error) {
	req := ProviderRequest{
		Instructions: c.cfg.Instructions,
		Messages: []Message{{
			Role: RoleUser,
			Text: "Conversation excerpt to summarize:\n\n" + renderHistory(head),
		}},
	}
	resp, err := c.cfg.Provider.Generate(ctx, req)
	if err != nil {
		return "", fmt.Errorf("agent: summarizing head: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Text) == "" {
		return "", fmt.Errorf("agent: summarizer returned no text")
	}
	return "Summary of earlier conversation:\n" + strings.TrimSpace(resp.Text), nil
}

// renderHistory flattens messages to a role-prefixed text block for the
// summarizer to read. Tool calls are shown by name + args so the summary can
// preserve what was done.
func renderHistory(msgs []Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Text)
		for _, tc := range m.ToolCalls {
			b.WriteString("\n  [calls ")
			b.WriteString(tc.Name)
			b.WriteString(" ")
			b.WriteString(strings.TrimSpace(string(tc.Args.Raw())))
			b.WriteString("]")
		}
	}
	return b.String()
}
