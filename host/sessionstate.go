package host

import (
	"sync"
	"sync/atomic"

	"github.com/panyam/chakra"
)

// sessionState is the persistence half of a session. It records where runs
// are stored, which run is active, and where the /sessions picker last paged
// to.
//
// Locking here is deliberately not uniform, because the state is not.
//
//   - store is set once at construction and read-only afterwards.
//   - runID is guarded by the App's turnMu, not by anything here. It changes
//     only at points a turn already owns (attach, resume, fork, lazy create).
//     A second lock would add a hierarchy without removing a race.
//     runIDAtomic mirrors it for readers that cannot take turnMu.
//   - cursor has its own mutex. Paging is a UI action unrelated to a turn,
//     and it can arrive while one is running.
//
// App holds it by value rather than by pointer, because every field is
// zero-value usable. A nil store means persistence is off, and the rest are
// a string, an atomic, and a mutex. The tests build partially-constructed
// Apps freely, and those keep working. A pointer would make them panic on a
// nil cluster, for no gain. Never copy it, because a copy of the mutex and
// atomic is meaningless.
type sessionState struct {
	store agent.RunStore

	// runID is the run that turns append to. App.turnMu guards it. Write
	// through setRunID so the atomic mirror matches it.
	runID string

	// runIDAtomic mirrors runID for a lock-free read. The session-scoped
	// memory namespace func reads it during a turn while turnMu is already
	// held, so it must not go through anything that takes turnMu.
	runIDAtomic atomic.Value

	cursorMu sync.Mutex
	cursor   string
}

// setRunID updates the active run id and its lock-free mirror together.
// Caller holds turnMu (every write site does). All four writers (AttachRun,
// resumeLocked, Fork, ensureRunLocked) go through here, so the mirror cannot
// drift out of sync.
func (s *sessionState) setRunID(id string) {
	s.runID = id
	s.runIDAtomic.Store(id)
}

// currentRunID reads the active run id WITHOUT taking turnMu, so it is safe
// to call from inside a turn. The memory namespace func runs while RunTurn
// already holds turnMu, and a locking read would deadlock there. It returns
// "" before any run is set, and the session-scoped namespace func maps ""
// to the shared default scratchpad.
func (s *sessionState) currentRunID() string {
	if v := s.runIDAtomic.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// rememberCursor records where the last /sessions page ended, so "more" can
// advance. The store's cursor is opaque, so the host has to hold it.
func (s *sessionState) rememberCursor(c string) {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	s.cursor = c
}

// nextCursor returns the paging position, empty when there is no more.
func (s *sessionState) nextCursor() string {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	return s.cursor
}
