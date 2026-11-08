package anthropic

import (
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	agent "github.com/panyam/chakra"
)

// thinkingBindingBeta is the beta that lets a request say what to do with a
// resent thinking block the API no longer accepts.
const thinkingBindingBeta = "thinking-binding-controls-2026-08-01"

// thinkingStoreCap bounds how many assistant turns a Provider keeps thinking
// for. Each entry holds one step's reply, tool inputs included.
const thinkingStoreCap = 256

// ThinkingStats counts what the provider did with thinking blocks since it
// was built.
type ThinkingStats struct {
	// Resent counts the thinking blocks sent back with an earlier assistant
	// message.
	Resent int
	// Dropped counts the resent blocks the API reported it dropped, from the
	// response's input_transformations. A dropped block means the provider
	// judged the history unchanged and the API did not.
	Dropped int
}

// ThinkingStats reports the counts so far. It is safe to call at any time.
func (p *Provider) ThinkingStats() ThinkingStats {
	return ThinkingStats{Resent: int(p.resent.Load()), Dropped: int(p.dropped.Load())}
}

// key identifies one assistant message at one place in one conversation.
type key [32]byte

// storedTurn is an assistant reply as the API returned it, thinking blocks
// included, ready to send back in place of the message chakra rebuilds.
type storedTurn struct {
	msg    sdk.MessageParam
	blocks int
}

// thinkingStore keeps replies with thinking in them, oldest out first.
type thinkingStore struct {
	mu      sync.Mutex
	cap     int
	entries map[key]storedTurn
	order   []key
}

func newThinkingStore(n int) *thinkingStore {
	return &thinkingStore{cap: n, entries: map[key]storedTurn{}}
}

func (s *thinkingStore) put(k key, t storedTurn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[k]; !ok {
		s.order = append(s.order, k)
	}
	s.entries[k] = t
	for len(s.order) > s.cap {
		delete(s.entries, s.order[0])
		s.order = s.order[1:]
	}
}

func (s *thinkingStore) get(k key) (storedTurn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.entries[k]
	return t, ok
}

// prefix hashes what a thinking block is bound to, one message at a time.
// The API checks that the system prompt, the tool set and every earlier
// message are unchanged since the block was made. So the hash starts from
// the system prompt and the tools, sorted by name since the API compares
// them as a set, and folds in each message as it was sent.
type prefix struct{ h key }

func newPrefix(model string, system []sdk.TextBlockParam, tools []sdk.ToolUnionParam) prefix {
	sorted := slices.Clone(tools)
	slices.SortFunc(sorted, func(a, b sdk.ToolUnionParam) int {
		return strings.Compare(toolName(a), toolName(b))
	})
	raw, _ := json.Marshal(struct {
		Model  string               `json:"model"`
		System []sdk.TextBlockParam `json:"system"`
		Tools  []sdk.ToolUnionParam `json:"tools"`
	}{model, system, sorted})
	return prefix{sha256.Sum256(raw)}
}

func toolName(t sdk.ToolUnionParam) string {
	if t.OfTool != nil {
		return t.OfTool.Name
	}
	return ""
}

// add folds in one message as it went on the wire.
func (p prefix) add(m sdk.MessageParam) prefix {
	raw, _ := json.Marshal(m)
	return prefix{sha256.Sum256(append(p.h[:], raw...))}
}

// at keys the assistant message m when the conversation before it hashes to
// p. m is chakra's own message, so the key holds only when the Runner sends
// back what this provider returned, unchanged.
func (p prefix) at(m agent.Message) key {
	type call struct {
		ID, Name string
		Args     json.RawMessage
	}
	calls := make([]call, len(m.ToolCalls))
	for i, tc := range m.ToolCalls {
		args := tc.Args.Raw()
		if len(args) == 0 {
			args = []byte("{}")
		}
		calls[i] = call{tc.ID, tc.Name, json.RawMessage(args)}
	}
	raw, _ := json.Marshal(struct {
		Text  string
		Calls []call
	}{m.Text, calls})
	return sha256.Sum256(append(append(p.h[:], 'A'), raw...))
}

// thinkingBlocks counts the thinking and redacted thinking blocks in a reply.
func thinkingBlocks(msg *sdk.Message) int {
	n := 0
	for _, b := range msg.Content {
		if b.Type == "thinking" || b.Type == "redacted_thinking" {
			n++
		}
	}
	return n
}

// countDropped reads input_transformations from a message_start's message,
// which the SDK has no field for, and counts the dropped thinking blocks.
func countDropped(raw string) int {
	var m struct {
		InputTransformations []struct {
			Type string `json:"type"`
		} `json:"input_transformations"`
	}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return 0
	}
	n := 0
	for _, t := range m.InputTransformations {
		if t.Type == "thinking_dropped" {
			n++
		}
	}
	return n
}

// keep stores a finished reply's thinking under the key its assistant
// message will have on the next step. end hashes the request it answered.
func (p *Provider) keep(end prefix, msg *sdk.Message, resp agent.Message) {
	if p.cfg.NoThinkingReplay {
		return
	}
	if n := thinkingBlocks(msg); n > 0 {
		p.thinking.put(end.at(resp), storedTurn{msg: msg.ToParam(), blocks: n})
	}
}
