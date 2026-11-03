package host

import (
	"sync"
	"sync/atomic"

	gocurrent "github.com/panyam/gocurrent"
)

// eventSpine is the per-session event backbone: the retained log every surface
// reads from, the synchronous local observers, and the offset marking how much
// of the log the RunStore already holds.
//
// The three belong together because replay is stitched from two sources that
// must agree. log covers [persisted, len) in memory, and the RunStore covers
// [0, persisted). A two-step read of them would race, and could replay an
// event twice or skip one. Reading them as a consistent pair rules out both.
// Subscribe takes that snapshot under turnMu.
//
// The Queue itself is reachable as log rather than wrapped in delegating
// methods. Its barrier semantics (Resolve, AwaitResolution) implement the
// multi-surface ask protocol. Hiding them behind pass-throughs would obscure
// the contract that callers actually reason about.
type eventSpine struct {
	log *gocurrent.Queue[HostEvent]

	// mu serializes local delivery only. The async server connections, the
	// turn goroutine, and the event goroutines all emit, and the terminal
	// renderer is not concurrency-safe.
	mu        sync.Mutex
	observers []Observer

	// persisted is the log offset through which this run's events have been
	// written to the RunStore. The turn-end persist site advances it once
	// AppendEvents succeeds, and Subscribe reads it lock-free. It is 0 when
	// no RunStore is configured, so replay falls back to the whole window.
	persisted atomic.Int64
}

// emit records an event on the retained log and delivers it synchronously to
// every local observer, returning the event's offset in the log.
//
// The append happens before the fan-out and outside the lock. A web surface
// subscribes onto the log as its source of truth, so the log must record the
// event even if an observer blocks. The synchronous fan-out preserves the
// terminal rendering contract. A caller reading the renderer's io.Writer sees
// the event once emit returns.
func (s *eventSpine) emit(ev HostEvent) (offset int) {
	offset = s.log.Append(ev)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.observers {
		o.On(ev)
	}
	return offset
}
