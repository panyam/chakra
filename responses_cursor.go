package agent

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// cursorKey hashes the parts of a history the model sees: role, text, the
// tool call a result answers, and each call's id, name and arguments.
// Timestamp is session metadata and is left out, so a history read back from
// a RunStore hashes the same as the one in memory.
func cursorKey(msgs []Message) [32]byte {
	h := sha256.New()
	count := func(n int) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		h.Write(b[:])
	}
	field := func(s string) {
		count(len(s))
		h.Write([]byte(s))
	}
	for _, m := range msgs {
		field(string(m.Role))
		field(m.Text)
		field(m.ToolCallID)
		count(len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			field(tc.ID)
			field(tc.Name)
			field(string(tc.Args.Raw()))
		}
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// cursorCache is a bounded LRU from history hash to response id.
type cursorCache struct {
	mu    sync.Mutex
	max   int
	order *list.List
	byKey map[[32]byte]*list.Element
}

type cursorEntry struct {
	key [32]byte
	id  string
}

func newCursorCache(max int) *cursorCache {
	return &cursorCache{max: max, order: list.New(), byKey: map[[32]byte]*list.Element{}}
}

func (c *cursorCache) get(k [32]byte) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byKey[k]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(e)
	return e.Value.(*cursorEntry).id, true
}

func (c *cursorCache) put(k [32]byte, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.byKey[k]; ok {
		e.Value.(*cursorEntry).id = id
		c.order.MoveToFront(e)
		return
	}
	c.byKey[k] = c.order.PushFront(&cursorEntry{key: k, id: id})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.byKey, last.Value.(*cursorEntry).key)
	}
}

func (c *cursorCache) drop(k [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.byKey[k]; ok {
		c.order.Remove(e)
		delete(c.byKey, k)
	}
}

func (c *cursorCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
