package agent

import (
	"context"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// defaultRepeatNote is RepeatCacheConfig.Note when it is empty.
const defaultRepeatNote = "(This call already returned the above earlier in this turn, so it wasn't run again.)"

// defaultRepeatMax is RepeatCacheConfig.Max when it is zero.
const defaultRepeatMax = 3

// RepeatCacheConfig configures NewRepeatCache.
type RepeatCacheConfig struct {
	// Key says which calls count as the same. Two calls with the same key in
	// one turn are a repeat. Returning false leaves the call out, so it
	// always runs. Nil keys a call by its tool name and its arguments as
	// canonical JSON, so key order doesn't matter. A host whose tool takes a
	// field that doesn't change the answer, such as a reason for the call,
	// leaves that field out of the key.
	Key func(ToolCallInfo) (string, bool)
	// Note follows the first answer on its own line, so the model knows the
	// call didn't run again. Empty means a short note saying so.
	Note string
	// Max caps the free answers per turn. Past it a repeat runs and its step
	// counts, so a model that keeps repeating still reaches ErrMaxSteps.
	// Zero means 3.
	Max int
}

// NewRepeatCache answers a repeated call to an idempotent tool from the
// first answer, as a free result (#317). Imagine a model that asks for a
// package's license twice in one turn. Nothing changed in between, so the
// answer is the same. The cache sends the first answer back with Note, and
// the tool doesn't run. The repeat costs no step while the turn is under
// RunnerConfig.MaxFreeRefusals, as FreeResult says.
//
// Only a call whose ToolCallInfo.Idempotent is set is cached, so a tool
// that changes something always runs. A tool reports that with
// idempotentHint or readOnlyHint. The cache keeps one turn's answers, and
// the next turn starts empty, since the world may have changed between
// turns. It keeps only answers that succeeded, so a call that failed runs
// again.
//
// It answers before anything after it in RunnerConfig.ToolMiddleware runs.
// So put it after any gate whose decision should still apply to a repeat.
func NewRepeatCache(cfg RepeatCacheConfig) ToolMiddleware {
	if cfg.Key == nil {
		cfg.Key = defaultRepeatKey
	}
	if cfg.Note == "" {
		cfg.Note = defaultRepeatNote
	}
	if cfg.Max <= 0 {
		cfg.Max = defaultRepeatMax
	}
	// id tells this cache's entries apart from another cache's in the turn.
	id := new(int)
	return func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
		key, ok := cfg.Key(info)
		c, _ := turnValue(ctx, id, func() any { return &repeatTurn{answers: map[string]string{}} }).(*repeatTurn)
		if !ok || !info.Idempotent || c == nil {
			return next(ctx, info)
		}
		if text, hit := c.lookup(key, cfg.Max); hit {
			return nil, &FreeResultError{Text: text + "\n\n" + cfg.Note, Reason: "repeat of an earlier call"}
		}
		res, err := next(ctx, info)
		if err == nil && res != nil && !res.IsError {
			c.store(key, toolResultText(res))
		}
		return res, err
	}
}

func defaultRepeatKey(info ToolCallInfo) (string, bool) {
	return info.Call.Name + " " + canonicalArgs(info.Call.Args.Raw()), true
}

// repeatTurn is one turn's cached answers.
type repeatTurn struct {
	mu      sync.Mutex
	answers map[string]string
	served  int
}

// lookup returns key's first answer and counts it served, while fewer than
// max have been.
func (c *repeatTurn) lookup(key string, max int) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	text, ok := c.answers[key]
	if !ok || c.served >= max {
		return "", false
	}
	c.served++
	return text, true
}

// store keeps the first answer for key.
func (c *repeatTurn) store(key, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.answers[key]; !ok {
		c.answers[key] = text
	}
}

// turnStoreKey is the context key for a turn's store. RunTurn installs a
// fresh one, so a value kept there lives exactly as long as the turn, and two
// turns running at once on one Runner keep separate values.
type turnStoreKey struct{}

type turnStore struct {
	mu   sync.Mutex
	vals map[any]any
}

func withTurnStore(ctx context.Context) context.Context {
	return context.WithValue(ctx, turnStoreKey{}, &turnStore{vals: map[any]any{}})
}

// turnValue returns the turn's value for key, made with init the first
// time. It returns nil outside a turn.
func turnValue(ctx context.Context, key any, init func() any) any {
	s, _ := ctx.Value(turnStoreKey{}).(*turnStore)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vals[key]
	if !ok {
		v = init()
		s.vals[key] = v
	}
	return v
}
