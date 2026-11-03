package agent

import (
	"container/list"
	"context"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// ToolResultStore is the persistence seam for offloaded tool results. An
// OffloadingSource stores full tool outputs here, out of band and keyed by
// a ref. The conversation then holds only a compact stub, and the model
// fetches the detail on demand with read_tool_result. It is the "just in
// time context" primitive. It is lossless and costs something only on
// lookup. Compaction complements it, and compaction is lossy and costs
// something every time.
//
// The API follows the gRPC-style convention in stores/STORAGE_SEAMS.md.
// Each method is Method(ctx, req) (resp, error). App state goes on the
// response, and the error is reserved for storage-layer faults. So an
// unknown ref is app state (Found=false), never an error. A backend can
// evict a blob while its stub is still in the conversation.
// read_tool_result turns Found=false into a graceful "no longer available"
// answer instead of a failure.
//
// The interface and its in-memory default live in agent/, because they
// handle core.ToolResult and pair with the agent-only OffloadingSource
// (A6). Durable backends are sibling modules (agent/store/redis,
// agent/store/gorm). Each backend sets its own retention at construction,
// with a native TTL, a GC sweep, or an LRU cap. Retention is never part of
// this contract, so the read path is built to tolerate eviction.
type ToolResultStore interface {
	// PutToolResult stores a result and returns nothing beyond error.
	// The caller assigns refs, and OffloadingSource mints them. The
	// store treats them as opaque. OffloadingSource names a result by
	// its content hash, so the same content always gets the same ref.
	// It checks GetToolResult first and skips the Put when the ref is
	// found. After an eviction, the same ref is Put again, and a store
	// must accept that by overwriting.
	PutToolResult(ctx context.Context, req PutToolResultRequest) (PutToolResultResponse, error)

	// GetToolResult fetches a result by ref. Found=false means the ref
	// is unknown, because it was never stored or was evicted. That is
	// the caller's cue to degrade gracefully, and it is not an error.
	GetToolResult(ctx context.Context, req GetToolResultRequest) (GetToolResultResponse, error)
}

// PutToolResultRequest carries the ref and the full result to retain.
type PutToolResultRequest struct {
	Ref    string
	Result core.ToolResult
}

// PutToolResultResponse is empty today. It exists so the method can add
// app state later, such as an assigned ref or a stored-bytes count,
// without a signature break. That follows the gRPC-style convention.
type PutToolResultResponse struct{}

// GetToolResultRequest fetches by ref.
type GetToolResultRequest struct {
	Ref string
}

// GetToolResultResponse holds the result when Found. The returned value
// is the caller's own copy. In-memory implementations return the stored
// value without a clone. That is safe because results are treated as
// immutable once stored, and OffloadingSource never mutates one.
type GetToolResultResponse struct {
	Result core.ToolResult
	Found  bool
}

// InMemoryToolResultStore is the default ToolResultStore. It is a
// mutex-guarded map and is safe for concurrent use. Nothing survives
// process exit, so durable deployments swap in a sibling backend behind
// the same interface. WithMaxToolResults sets an optional entry cap.
// Past the cap, the store evicts the least-recently-stored ref. The
// graceful read contract makes that eviction safe. Default is unbounded.
type InMemoryToolResultStore struct {
	mu      sync.Mutex
	results map[string]core.ToolResult
	// order tracks insertion order for the LRU cap, and front is oldest.
	// It is nil when maxEntries == 0, which means unbounded with no
	// bookkeeping.
	order      *list.List
	elems      map[string]*list.Element
	maxEntries int
}

// ToolResultStoreOption configures an InMemoryToolResultStore.
type ToolResultStoreOption func(*InMemoryToolResultStore)

// WithMaxToolResults caps the store at n stored results, evicting the
// oldest when a Put would exceed n. Zero or negative means unbounded,
// which is the default. Eviction is safe, because read_tool_result turns
// an unknown ref into a "no longer available" answer, not an error.
func WithMaxToolResults(n int) ToolResultStoreOption {
	return func(s *InMemoryToolResultStore) {
		if n > 0 {
			s.maxEntries = n
			s.order = list.New()
			s.elems = map[string]*list.Element{}
		}
	}
}

// NewInMemoryToolResultStore returns an empty in-memory store.
func NewInMemoryToolResultStore(opts ...ToolResultStoreOption) *InMemoryToolResultStore {
	s := &InMemoryToolResultStore{results: map[string]core.ToolResult{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// PutToolResult implements ToolResultStore.
func (s *InMemoryToolResultStore) PutToolResult(ctx context.Context, req PutToolResultRequest) (PutToolResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.results[req.Ref]; !exists && s.order != nil {
		s.elems[req.Ref] = s.order.PushBack(req.Ref)
		for s.order.Len() > s.maxEntries {
			oldest := s.order.Front()
			ref := oldest.Value.(string)
			s.order.Remove(oldest)
			delete(s.elems, ref)
			delete(s.results, ref)
		}
	}
	s.results[req.Ref] = req.Result
	return PutToolResultResponse{}, nil
}

// GetToolResult implements ToolResultStore.
func (s *InMemoryToolResultStore) GetToolResult(ctx context.Context, req GetToolResultRequest) (GetToolResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, ok := s.results[req.Ref]
	if !ok {
		return GetToolResultResponse{}, nil
	}
	return GetToolResultResponse{Result: res, Found: true}, nil
}
