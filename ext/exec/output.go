package exec

import (
	"bytes"
	"fmt"
	"sync"
)

// capBuffer collects a stream and keeps at most max bytes of it, dropping from
// the middle.
//
// A failing build states the problem in its first lines (a config error, a
// missing tool) or its last ones (the failure summary, the assertion). A
// tail-only buffer loses the first kind, and a head-only one loses the second.
//
// os/exec runs a goroutine for stdout and one for stderr, and both write to
// it, so it locks.
type capBuffer struct {
	mu      sync.Mutex
	max     int
	head    bytes.Buffer
	tail    []byte
	dropped int
}

func newCapBuffer(max int) *capBuffer {
	return &capBuffer{max: max}
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)

	half := c.max / 2
	if room := half - c.head.Len(); room > 0 {
		take := min(room, len(p))
		c.head.Write(p[:take])
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}

	// Everything past the head is a rolling window of the last half bytes.
	c.tail = append(c.tail, p...)
	if excess := len(c.tail) - (c.max - half); excess > 0 {
		c.tail = c.tail[excess:]
		c.dropped += excess
	}
	return n, nil
}

// String renders everything that was kept, with a line naming what was not.
func (c *capBuffer) String() string {
	s, _ := c.View(0)
	return s
}

// View renders at most max bytes of what was kept, dropping more from the
// middle, and reports whether anything is missing. Zero or less means all of
// it.
//
// View states the count for a reason. Imagine a model that cannot tell
// truncated output from complete output. It will read a passing tail as a
// passing run. The count covers both drops, the capture's and the view's, so
// it counts every printed byte that never reached the reader.
func (c *capBuffer) View(max int) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	head, tail := c.head.Bytes(), c.tail
	dropped := c.dropped
	if kept := len(head) + len(tail); max > 0 && kept > max {
		all := append(append([]byte{}, head...), tail...)
		head, tail = all[:max/2], all[kept-(max-max/2):]
		dropped += kept - max
	}
	if dropped == 0 {
		return string(head) + string(tail), false
	}
	return fmt.Sprintf("%s\n... %d bytes of output dropped from the middle ...\n%s",
		head, dropped, tail), true
}

// Truncated reports whether the capture dropped anything.
func (c *capBuffer) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped > 0
}
