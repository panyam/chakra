package agent

import (
	"container/list"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// InMemorySemanticStore is a MemoryStore that recalls by embedding similarity
// instead of substring match. It composes an Embedder (text -> vector) with
// an in-process brute-force cosine index. PutMemory embeds the note and keeps
// its vector. ListMemories embeds the query and returns items ranked by
// cosine similarity, each with its Score. It implements the same MemoryStore
// interface as the substring default. So swapping it in makes the recall
// tool and the summary semantic, with no change to the model-facing surface.
// The interface hides how retrieval works.
//
// The index is exact and O(n) per query. That is the right trade for a
// working-memory-sized scratchpad of tens to hundreds of notes. Approximate
// nearest-neighbor at scale belongs in a durable backend, such as a pgvector
// sibling MemoryStore. It does not belong in the in-process default.
//
// It is safe for concurrent use. For a hosted Embedder, embedding is a network
// call. It happens outside the lock, so Put/List never hold the mutex across
// I/O.
type InMemorySemanticStore struct {
	embedder Embedder
	tp       core.TracerProvider

	mu sync.Mutex
	// ns partitions the store by req.Namespace, where "" is the default. So
	// recall never crosses namespaces. maxEntries caps each namespace.
	ns         map[string]*nsSemantic
	maxEntries int
}

// nsSemantic is one namespace's semantic scratchpad. It holds items and their
// vectors. It also keeps insertion order, with the oldest at the front, for the
// empty-query listing and the cap.
type nsSemantic struct {
	items map[string]MemoryItem
	vecs  map[string]Embedding
	order *list.List
	elems map[string]*list.Element
}

func newNSSemantic() *nsSemantic {
	return &nsSemantic{items: map[string]MemoryItem{}, vecs: map[string]Embedding{}, order: list.New(), elems: map[string]*list.Element{}}
}

// bucket returns the sub-store for a namespace, creating it on first use. The
// caller holds s.mu.
func (s *InMemorySemanticStore) bucket(ns string) *nsSemantic {
	m := s.ns[ns]
	if m == nil {
		m = newNSSemantic()
		s.ns[ns] = m
	}
	return m
}

// InMemorySemanticStoreOption configures an InMemorySemanticStore.
type InMemorySemanticStoreOption func(*InMemorySemanticStore)

// WithSemanticMaxMemories caps the store at n items, evicting the oldest when
// a Put of a new key would exceed n. Zero or negative means unbounded.
func WithSemanticMaxMemories(n int) InMemorySemanticStoreOption {
	return func(s *InMemorySemanticStore) {
		if n > 0 {
			s.maxEntries = n
		}
	}
}

// WithSemanticTracerProvider opts the store into an agent.memory.recall span
// per similarity query. Nil / NoopTracerProvider means zero overhead.
func WithSemanticTracerProvider(tp core.TracerProvider) InMemorySemanticStoreOption {
	return func(s *InMemorySemanticStore) {
		if tp != nil {
			s.tp = tp
		}
	}
}

// NewInMemorySemanticStore builds a semantic store over embedder. The embedder
// is required. Every note and query is embedded with it, so query a store with
// the same Embedder that built it.
func NewInMemorySemanticStore(embedder Embedder, opts ...InMemorySemanticStoreOption) (*InMemorySemanticStore, error) {
	if embedder == nil {
		return nil, fmt.Errorf("agent: InMemorySemanticStore needs an Embedder")
	}
	s := &InMemorySemanticStore{
		embedder: embedder,
		tp:       core.NoopTracerProvider{},
		ns:       map[string]*nsSemantic{},
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// PutMemory embeds the note and upserts it. It embeds key + value, so a recall
// query matches either. Embedding is synchronous, so a just-remembered fact is
// immediately recallable. Background or batch indexing for a distillation
// write path is a separate concern.
func (s *InMemorySemanticStore) PutMemory(ctx context.Context, req PutMemoryRequest) (PutMemoryResponse, error) {
	item := req.Item
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	vecs, err := s.embedder.Embed(ctx, []string{item.Key + " " + item.Value})
	if err != nil {
		return PutMemoryResponse{}, fmt.Errorf("agent: embedding memory %q: %w", item.Key, err)
	}
	var vec Embedding
	if len(vecs) > 0 {
		vec = vecs[0]
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.bucket(req.Namespace)
	if _, exists := m.items[item.Key]; !exists {
		m.elems[item.Key] = m.order.PushBack(item.Key)
		for s.maxEntries > 0 && m.order.Len() > s.maxEntries {
			oldest := m.order.Front()
			key := oldest.Value.(string)
			m.order.Remove(oldest)
			delete(m.elems, key)
			delete(m.items, key)
			delete(m.vecs, key)
		}
	}
	m.items[item.Key] = item
	m.vecs[item.Key] = vec
	return PutMemoryResponse{}, nil
}

// ListMemories ranks items by cosine similarity to the query. An empty Query
// returns all items oldest-first with Score 0. The summary uses that "list
// everything" path, and it has no query to score against. Limit caps the
// result.
func (s *InMemorySemanticStore) ListMemories(ctx context.Context, req ListMemoriesRequest) (ListMemoriesResponse, error) {
	if req.Query == "" {
		return s.listAll(req.Namespace, req.Limit), nil
	}

	qvecs, err := s.embedder.Embed(ctx, []string{req.Query})
	if err != nil {
		return ListMemoriesResponse{}, fmt.Errorf("agent: embedding query: %w", err)
	}
	var qvec Embedding
	if len(qvecs) > 0 {
		qvec = qvecs[0]
	}

	_, span := s.tp.StartSpan(ctx, "agent.memory.recall",
		core.Attribute{Key: "agent.memory.limit", Value: fmt.Sprint(req.Limit)})
	defer span.End()

	s.mu.Lock()
	m := s.bucket(req.Namespace)
	scored := make([]ScoredMemory, 0, len(m.items))
	for e := m.order.Front(); e != nil; e = e.Next() {
		key := e.Value.(string)
		scored = append(scored, ScoredMemory{Item: m.items[key], Score: qvec.Cosine(m.vecs[key])})
	}
	s.mu.Unlock()

	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if req.Limit > 0 && len(scored) > req.Limit {
		scored = scored[:req.Limit]
	}
	span.SetAttribute("agent.memory.candidates", fmt.Sprint(len(scored)))
	if len(scored) > 0 {
		span.SetAttribute("agent.memory.top_score", fmt.Sprintf("%.4f", scored[0].Score))
	}
	return ListMemoriesResponse{Items: scored}, nil
}

func (s *InMemorySemanticStore) listAll(ns string, limit int) ListMemoriesResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.bucket(ns)
	out := make([]ScoredMemory, 0, m.order.Len())
	for e := m.order.Front(); e != nil; e = e.Next() {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, ScoredMemory{Item: m.items[e.Value.(string)]})
	}
	return ListMemoriesResponse{Items: out}
}

// DeleteMemory removes an item and its vector. An unknown key gives
// Deleted=false, not an error, as in the substring store.
func (s *InMemorySemanticStore) DeleteMemory(ctx context.Context, req DeleteMemoryRequest) (DeleteMemoryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.bucket(req.Namespace)
	if _, ok := m.items[req.Key]; !ok {
		return DeleteMemoryResponse{Deleted: false}, nil
	}
	delete(m.items, req.Key)
	delete(m.vecs, req.Key)
	if e, ok := m.elems[req.Key]; ok {
		m.order.Remove(e)
		delete(m.elems, req.Key)
	}
	return DeleteMemoryResponse{Deleted: true}, nil
}
