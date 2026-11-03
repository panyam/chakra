package agent

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"
)

// RunStore is the persistence seam for runs. A session accumulates messages
// across turns, and optionally events. A run is an append-only log of them.
// Surfaces persist TurnResult.Messages after each turn. Resume is LoadRun
// followed by Run over the loaded messages. Fork is ForkRun followed by
// divergence. Both are cheap because the Runner is stateless over history.
// The Runner itself never touches a RunStore.
//
// API shape follows the gRPC-style convention pinned in
// stores/STORAGE_SEAMS.md:
//
//	Method(ctx context.Context, req XRequest) (XResponse, error)
//
// ctx threads cancellation, deadlines, and trace context.
// Application-level state (Found, Created) lives on the response. error
// is reserved for storage-layer failures, such as connection drops and
// corrupt records. In particular, an unknown RunID is app-state
// (Found=false), never an error.
//
// The interface and its in-memory default live here. Every method takes or
// returns agent.Message / agent.Event. So hosting them in mcpkit's stores/
// package would make mcpkit depend on this module, which the A6 corollary
// forbids. Durable backends are satellite modules (store/redis, store/gorm),
// so their dependencies are kept out of this module.
//
// CreateRun with an explicit RunID and ForkRun are all-or-nothing. On
// error, no run observable at the requested ID came into existence. A
// run exists at that ID only because some create or fork fully
// committed. Imagine a client calls ForkRun with ID r2 and the call
// times out. It can't tell whether the fork happened, so it retries
// with r2. If a run exists at r2, the first call committed in full and
// the retry can use it. If none exists, the first call left nothing
// behind and the retry starts clean (see ForkRunRequest). Implementers
// must uphold this contract. Suppose a partially-forked run reports
// Created=false to a retry. That is a contract violation, not a quirk.
type RunStore interface {
	CreateRun(ctx context.Context, req CreateRunRequest) (CreateRunResponse, error)
	AppendMessages(ctx context.Context, req AppendMessagesRequest) (AppendMessagesResponse, error)
	AppendEvents(ctx context.Context, req AppendEventsRequest) (AppendEventsResponse, error)
	LoadRun(ctx context.Context, req LoadRunRequest) (LoadRunResponse, error)
	ForkRun(ctx context.Context, req ForkRunRequest) (ForkRunResponse, error)

	// ListRuns enumerates stored runs as lightweight RunInfo (no message
	// bodies), for a session picker or a dashboard. Paged via an opaque
	// cursor. NextCursor is empty when the last page was returned. A
	// non-agent consumer (a poller, a UI) wants this too, so it returns
	// protocol/data objects, not model-facing ones.
	ListRuns(ctx context.Context, req ListRunsRequest) (ListRunsResponse, error)
}

// RunInfo is the header of a run. It has identity, lineage, and counts, but
// not the message or event bodies, so a listing is cheap. MessageCount is
// the current length of the message log. A picker renders the fork lineage
// (ParentID, ForkPoint) as the session tree.
type RunInfo struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId,omitempty"`

	// ForkPoint is the number of ParentID's messages copied into this run at
	// the fork, and zero for runs created directly. It is a message count,
	// never a timestamp or a storage sequence value. See Run.ForkPoint for why.
	// Comparing it against MessageCount tells a picker how far this run has
	// diverged from its parent.
	ForkPoint int `json:"forkPoint,omitempty"`

	CreatedAt time.Time `json:"createdAt"`

	// MessageCount is the current length of this run's message log, so a
	// listing can show size without loading bodies.
	MessageCount int `json:"messageCount"`
}

// ListRunsRequest pages the run listing. Cursor is empty for the first
// page and echoes a prior response's NextCursor thereafter. Its content is
// backend-defined and opaque. Limit caps the page (zero means the
// backend's default).
type ListRunsRequest struct {
	Cursor string
	Limit  int
}

// ListRunsResponse carries one page. NextCursor is empty on the last page.
// Ordering is best-effort newest-first where the backend supports it, as the
// in-memory and gorm backends do. The Redis backend lists in scan order,
// which is unordered, and its implementation documents this.
type ListRunsResponse struct {
	Runs       []RunInfo
	NextCursor string
}

// DefaultListRunsLimit bounds a page when ListRunsRequest.Limit is zero.
const DefaultListRunsLimit = 50

// Run is one persisted run, with identity, fork lineage, and the
// append-only logs. Messages is the conversation history, and a resume
// feeds it back into Runner.Run. Events is the optional replay/audit
// stream. Both element types are wire-serializable by constraint A2, so
// Run itself marshals cleanly through encoding/json. Durable backends
// depend on that.
type Run struct {
	ID string `json:"id"`

	// ParentID is the run this one was forked from. Empty for runs
	// created directly.
	ParentID string `json:"parentId,omitempty"`

	// ForkPoint is the number of ParentID's messages copied into this
	// run at the fork. It gives the fork position as a backend-neutral
	// message count. It is never a timestamp, because this run's
	// CreatedAt records the fork's wall-clock moment. It is never a
	// storage sequence value either. Zero for runs created directly.
	// Surfaces such as rewind pickers and history UIs read it as lineage
	// metadata. Nothing reconstructs history from it, since every run
	// owns its full copy.
	ForkPoint int `json:"forkPoint,omitempty"`

	CreatedAt time.Time `json:"createdAt"`

	Messages []Message `json:"messages"`
	Events   []Event   `json:"events,omitempty"`
}

// CreateRunRequest starts a new empty run. RunID is optional. Empty asks
// the store to generate a unique ID. Non-empty claims a caller-chosen
// name, such as a session name or a ticket ID. Claiming an ID that
// already exists is not an overwrite. The store leaves the existing run
// intact and reports Created=false.
type CreateRunRequest struct {
	RunID string
}

// CreateRunResponse carries the run's identity. Created is false when
// the requested RunID already existed (the existing run is untouched).
// Stores always report Created=true for generated IDs.
type CreateRunResponse struct {
	RunID   string
	Created bool
}

// AppendMessagesRequest appends messages to a run's log in order.
// Callers pass exactly the entries that a turn added, the user message
// plus TurnResult.Messages. The stored log then threads the same way as
// in-process history.
//
// Implementations set Message.Timestamp to their own clock for every
// appended message whose Timestamp is zero. They preserve non-zero
// values verbatim, so the caller wins, and a surface can stamp the user
// message at keypress. One boundary has one rule, so code that
// constructs messages never needs to remember to stamp.
type AppendMessagesRequest struct {
	RunID    string
	Messages []Message
}

// AppendMessagesResponse reports whether the run existed. Found=false
// means nothing was written. The caller appended to a run it never
// created, or to one that was pruned. That is a caller bug to surface,
// not a storage fault.
type AppendMessagesResponse struct {
	Found bool
}

// AppendEventsRequest appends events to a run's audit/replay log. The
// event log is optional and independent of the message log. A store
// accepts events for any existing run whether or not the surface also
// persists messages.
type AppendEventsRequest struct {
	RunID  string
	Events []Event
}

// AppendEventsResponse reports whether the run existed. Found=false
// means nothing was written (see AppendMessagesResponse).
type AppendEventsResponse struct {
	Found bool
}

// LoadRunRequest fetches a run by ID.
type LoadRunRequest struct {
	RunID string
}

// LoadRunResponse carries the run when Found. The returned Run is the
// caller's to keep. Implementations return copies (or freshly decoded
// values), never aliases into store-internal state, so mutating
// Run.Messages cannot corrupt the log.
type LoadRunResponse struct {
	Run   Run
	Found bool
}

// ForkRunRequest copies an existing run's logs into a new run so the
// copy can diverge. NewRunID follows CreateRunRequest.RunID semantics.
// Empty generates a unique ID, and non-empty claims a caller-chosen one.
//
// A non-empty NewRunID is also the fork's idempotency key. Forks are
// all-or-nothing (see RunStore). Imagine a retry loop mints one
// deterministic ID, such as a session-scoped name or a ULID, and reuses
// it across attempts. The loop converges. A retry after a failure finds
// nothing at the ID and forks clean. A retry after an unobserved
// success gets Created=false against a complete fork. The caller can
// confirm that fork by loading it and checking ParentID.
type ForkRunRequest struct {
	RunID    string
	NewRunID string

	// AtMessage forks from an earlier point. A positive value copies
	// only the first AtMessage messages (checkpoint/rewind semantics).
	// Zero or negative copies everything, today's behavior. A value at
	// or beyond the source's length clamps to a full copy. The fork
	// reads the source's length when it starts, so appends racing the
	// fork come after the cut.
	//
	// The audit stream is not sliceable by message index, so a partial
	// fork copies NO events. Only a full copy brings the event log
	// across. The fork's message log is complete either way, and resume
	// needs only that.
	AtMessage int
}

// ForkRunResponse identifies the fork. Found is false when the source
// run does not exist. Created is false when NewRunID was claimed and
// already existed, and then no copy happens. On success both are true.
// RunID names the new run, and its ParentID records the lineage.
// ForkPoint is the message count actually copied. That count is the
// resolved fork position after clamping, and the fork's Run persists it
// too.
type ForkRunResponse struct {
	RunID     string
	Found     bool
	Created   bool
	ForkPoint int
}

// InMemoryRunStore is the default RunStore: a mutex-guarded map, useful
// for tests and single-process sessions that only need in-lifetime
// resume/fork. It is safe for concurrent use. Nothing survives process
// exit. Durable deployments swap in a sibling backend behind the same
// interface.
type InMemoryRunStore struct {
	mu   sync.Mutex
	runs map[string]*runEntry
	seq  int
}

// runEntry consolidates one run's state (constraint C2: one entry struct
// instead of parallel same-keyed maps).
type runEntry struct {
	parentID  string
	forkPoint int
	createdAt time.Time
	messages  []Message
	events    []Event
}

// NewInMemoryRunStore returns an empty in-memory RunStore.
func NewInMemoryRunStore() *InMemoryRunStore {
	return &InMemoryRunStore{runs: map[string]*runEntry{}}
}

// CreateRun implements RunStore. Generated IDs are sequential
// ("run-1", "run-2", ...). They are deterministic on purpose, since an
// in-memory store never shares an ID space across processes.
func (s *InMemoryRunStore) CreateRun(ctx context.Context, req CreateRunRequest) (CreateRunResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := req.RunID
	if id == "" {
		s.seq++
		id = fmt.Sprintf("run-%d", s.seq)
	} else if _, ok := s.runs[id]; ok {
		return CreateRunResponse{RunID: id, Created: false}, nil
	}
	s.runs[id] = &runEntry{createdAt: time.Now()}
	return CreateRunResponse{RunID: id, Created: true}, nil
}

// AppendMessages implements RunStore, stamping zero Timestamps per the
// AppendMessagesRequest rule.
func (s *InMemoryRunStore) AppendMessages(ctx context.Context, req AppendMessagesRequest) (AppendMessagesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.runs[req.RunID]
	if !ok {
		return AppendMessagesResponse{Found: false}, nil
	}
	start := len(e.messages)
	e.messages = append(e.messages, req.Messages...)
	now := time.Now()
	for i := start; i < len(e.messages); i++ {
		if e.messages[i].Timestamp.IsZero() {
			e.messages[i].Timestamp = now
		}
	}
	return AppendMessagesResponse{Found: true}, nil
}

// AppendEvents implements RunStore.
func (s *InMemoryRunStore) AppendEvents(ctx context.Context, req AppendEventsRequest) (AppendEventsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.runs[req.RunID]
	if !ok {
		return AppendEventsResponse{Found: false}, nil
	}
	e.events = append(e.events, req.Events...)
	return AppendEventsResponse{Found: true}, nil
}

// LoadRun implements RunStore. The returned Run's slices are clones, so
// callers may append to or mutate them freely.
func (s *InMemoryRunStore) LoadRun(ctx context.Context, req LoadRunRequest) (LoadRunResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.runs[req.RunID]
	if !ok {
		return LoadRunResponse{}, nil
	}
	return LoadRunResponse{
		Run: Run{
			ID:        req.RunID,
			ParentID:  e.parentID,
			ForkPoint: e.forkPoint,
			CreatedAt: e.createdAt,
			Messages:  slices.Clone(e.messages),
			Events:    slices.Clone(e.events),
		},
		Found: true,
	}, nil
}

// ListRuns implements RunStore, newest-first. The cursor is a decimal
// offset into the ordered set. That works for an in-memory store, because
// its set is stable within a process.
func (s *InMemoryRunStore) ListRuns(ctx context.Context, req ListRunsRequest) (ListRunsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]RunInfo, 0, len(s.runs))
	for id, e := range s.runs {
		infos = append(infos, RunInfo{
			ID:           id,
			ParentID:     e.parentID,
			ForkPoint:    e.forkPoint,
			CreatedAt:    e.createdAt,
			MessageCount: len(e.messages),
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		if !infos[i].CreatedAt.Equal(infos[j].CreatedAt) {
			return infos[i].CreatedAt.After(infos[j].CreatedAt)
		}
		return infos[i].ID < infos[j].ID
	})

	offset := 0
	if req.Cursor != "" {
		if n, err := strconv.Atoi(req.Cursor); err == nil && n > 0 {
			offset = n
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultListRunsLimit
	}
	if offset >= len(infos) {
		return ListRunsResponse{}, nil
	}
	end := offset + limit
	var next string
	if end < len(infos) {
		next = strconv.Itoa(end)
	} else {
		end = len(infos)
	}
	return ListRunsResponse{Runs: infos[offset:end], NextCursor: next}, nil
}

// ForkRun implements RunStore. The fork gets cloned logs, so parent and
// fork diverge independently after the copy. See ForkRunRequest for the
// AtMessage cut and event-log semantics.
func (s *InMemoryRunStore) ForkRun(ctx context.Context, req ForkRunRequest) (ForkRunResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.runs[req.RunID]
	if !ok {
		return ForkRunResponse{}, nil
	}
	id := req.NewRunID
	if id == "" {
		s.seq++
		id = fmt.Sprintf("run-%d", s.seq)
	} else if _, exists := s.runs[id]; exists {
		return ForkRunResponse{RunID: id, Found: true, Created: false}, nil
	}
	n := len(src.messages)
	if req.AtMessage > 0 && req.AtMessage < n {
		n = req.AtMessage
	}
	var events []Event
	if n == len(src.messages) {
		events = slices.Clone(src.events)
	}
	s.runs[id] = &runEntry{
		parentID:  req.RunID,
		forkPoint: n,
		createdAt: time.Now(),
		messages:  slices.Clone(src.messages[:n]),
		events:    events,
	}
	return ForkRunResponse{RunID: id, Found: true, Created: true, ForkPoint: n}, nil
}
