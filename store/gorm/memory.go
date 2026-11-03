package gormstore

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/panyam/chakra"
)

// DefaultMemoryTable is the table a SemanticMemoryStore uses when
// WithMemoryTableName is not set.
const DefaultMemoryTable = "agent_memories"

// identifierPattern is the whitelist for the table name. A developer sets
// the name, and the store composes it into raw SQL instead of binding it as
// a parameter. A match contains no quote or whitespace, so it is safe to
// interpolate directly.
const identifierPattern = `^[A-Za-z_][A-Za-z0-9_]*$`

var identifierRE = regexp.MustCompile(identifierPattern)

func validIdentifier(s string) bool { return identifierRE.MatchString(s) }

// DefaultEmbeddingDimensions is the width of the pgvector column when
// WithVectorDimensions is not set. 1536 is the width of OpenAI
// text-embedding-3-small, the common default. pgvector rejects an insert
// whose vector width differs from the column's. So a deployment with a
// different Embedder MUST pass WithVectorDimensions to match it.
const DefaultEmbeddingDimensions = 1536

// SemanticMemoryStore is the durable, ANN-backed MemoryStore, built on
// Postgres with the pgvector extension. It is the sibling of
// agent.InMemorySemanticStore. Both have the same MemoryStore contract, and
// both embed client-side through the Embedder seam. Here the database holds
// the vector index and ranks the top k, instead of an in-process brute-force
// scan. So the store survives process exit and scales past a working-memory
// scratchpad. It satisfies the same interface, so recall never branches on
// the backend. Swapping it in makes recall similarity-ranked and durable,
// with no change to the model-facing tools.
//
// The store is Postgres-only. RunStore and ToolResultStore in this module
// have a SQLite path, and this store has none, because the vector column,
// the "<=>" cosine-distance operator and the HNSW index are pgvector
// features. The store needs the "vector" extension but does not install it.
// CREATE EXTENSION needs privileges the store should not assume. The init
// SQL in mcpkit's docker/backends creates the extension, and
// NewSemanticMemoryStore fails clearly if the type is unknown.
//
// A namespace column lets one table hold many independent scratchpads. An
// operation uses the per-request Namespace (issue 1003) when it is set.
// Otherwise it uses the store's WithMemoryNamespace default, and empty
// means the global namespace (see effNS). So one store can serve many
// sessions through req.Namespace, and a store built per scratchpad still
// works with no per-request field.
//
// The store is safe for concurrent use, because the DB serializes. Embedding
// runs outside any transaction. A hosted Embedder makes a network call, and
// no DB round-trip is held open across it.
type SemanticMemoryStore struct {
	db        *gorm.DB
	embedder  agent.Embedder
	table     string
	namespace string
	dims      int
}

var _ agent.MemoryStore = (*SemanticMemoryStore)(nil)

// effNS resolves the namespace for one operation. It returns the
// per-request Namespace (issue 1003) when set, and otherwise the store's
// WithMemoryNamespace default. So one store can serve many sessions by
// request, and a store built per scratchpad still works with no
// per-request field.
func (s *SemanticMemoryStore) effNS(reqNS string) string {
	if reqNS != "" {
		return reqNS
	}
	return s.namespace
}

type memoryConfig struct {
	skipAutoMigrate bool
	table           string
	namespace       string
	dims            int
}

// MemoryOption customizes a SemanticMemoryStore. Distinct from the RunStore
// and ToolResultStore option types so the stores' options never mix.
type MemoryOption func(*memoryConfig)

// WithMemoryTableName points the store at a specific table, so it can live
// inside an existing schema next to unrelated tables. Empty keeps
// DefaultMemoryTable.
func WithMemoryTableName(name string) MemoryOption {
	return func(c *memoryConfig) {
		if name != "" {
			c.table = name
		}
	}
}

// WithMemoryNamespace binds the store to one namespace (scratchpad). Every
// Put/List/Delete is scoped to it, so a host can build one store per session
// id and keep the scratchpads isolated in a shared table. Empty (the
// default) is the global namespace.
func WithMemoryNamespace(ns string) MemoryOption {
	return func(c *memoryConfig) { c.namespace = ns }
}

// WithVectorDimensions sets the width of the pgvector column. The width
// MUST match the Embedder's output width, because pgvector rejects a
// mismatched insert. Zero keeps DefaultEmbeddingDimensions.
func WithVectorDimensions(n int) MemoryOption {
	return func(c *memoryConfig) {
		if n > 0 {
			c.dims = n
		}
	}
}

// WithoutMemoryAutoMigrate disables the DDL run at construction. Use it
// when something out of band manages the table and the HNSW index.
func WithoutMemoryAutoMigrate() MemoryOption {
	return func(c *memoryConfig) { c.skipAutoMigrate = true }
}

// NewSemanticMemoryStore returns a store over db, embedding notes and queries
// with embedder. It creates the table and an HNSW cosine index, unless
// WithoutMemoryAutoMigrate is passed. Both creates are idempotent, and both
// need the pgvector "vector" extension to exist already. The db handle and
// embedder are shared, not owned.
func NewSemanticMemoryStore(db *gorm.DB, embedder agent.Embedder, opts ...MemoryOption) (*SemanticMemoryStore, error) {
	if embedder == nil {
		return nil, fmt.Errorf("gormstore: SemanticMemoryStore needs an Embedder")
	}
	cfg := &memoryConfig{table: DefaultMemoryTable, dims: DefaultEmbeddingDimensions}
	for _, o := range opts {
		o(cfg)
	}
	// The store composes the table name into raw DDL and DML, where it
	// can't be bound as a parameter. So construction restricts it to a plain
	// SQL identifier instead of trusting quoting. A developer sets it with
	// WithMemoryTableName, and end users never do, so the check is defense
	// in depth. A validated identifier contains no quotes, so the hole is
	// closed outright and nothing depends on correct escaping.
	if !validIdentifier(cfg.table) {
		return nil, fmt.Errorf("gormstore: invalid memory table name %q (must match %s)", cfg.table, identifierPattern)
	}
	s := &SemanticMemoryStore{
		db:        db,
		embedder:  embedder,
		table:     cfg.table,
		namespace: cfg.namespace,
		dims:      cfg.dims,
	}
	if !cfg.skipAutoMigrate {
		if err := s.migrate(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// migrate creates the memory table and the HNSW cosine index. Construction
// fixes the vector column width (cfg.dims), and migrate assumes the
// extension is present. The table and index names derive from the
// configured table, so several stores can coexist in one database.
func (s *SemanticMemoryStore) migrate() error {
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %q (
		namespace  text        NOT NULL DEFAULT '',
		key        text        NOT NULL,
		body       jsonb       NOT NULL,
		embedding  vector(%d)  NOT NULL,
		created_at timestamptz NOT NULL,
		PRIMARY KEY (namespace, key)
	)`, s.table, s.dims)
	if err := s.db.Exec(ddl).Error; err != nil {
		return fmt.Errorf("gormstore: create memory table (is the pgvector extension installed?): %w", err)
	}
	idx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %q ON %q USING hnsw (embedding vector_cosine_ops)`,
		s.table+"_embedding_idx", s.table)
	if err := s.db.Exec(idx).Error; err != nil {
		return fmt.Errorf("gormstore: create memory index: %w", err)
	}
	return nil
}

// PutMemory embeds "key value", so a recall query can match either, and
// upserts the note. On conflict it refreshes the body and embedding and
// keeps created_at. So an update keeps the note's listing position, as in
// the in-memory stores. Embedding is synchronous, so a just-remembered fact
// is recallable at once.
func (s *SemanticMemoryStore) PutMemory(ctx context.Context, req agent.PutMemoryRequest) (agent.PutMemoryResponse, error) {
	item := req.Item
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	vecs, err := s.embedder.Embed(ctx, []string{item.Key + " " + item.Value})
	if err != nil {
		return agent.PutMemoryResponse{}, fmt.Errorf("gormstore: embedding memory %q: %w", item.Key, err)
	}
	if len(vecs) == 0 {
		return agent.PutMemoryResponse{}, fmt.Errorf("gormstore: embedder returned no vector for memory %q", item.Key)
	}
	body, err := json.Marshal(item)
	if err != nil {
		return agent.PutMemoryResponse{}, fmt.Errorf("gormstore: encoding memory %q: %w", item.Key, err)
	}
	sql := fmt.Sprintf(`INSERT INTO %q (namespace, key, body, embedding, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (namespace, key) DO UPDATE SET body = EXCLUDED.body, embedding = EXCLUDED.embedding`, s.table)
	err = s.db.WithContext(ctx).Exec(sql,
		s.effNS(req.Namespace), item.Key, string(body), pgvector.NewVector(vecs[0]), item.CreatedAt.UTC(),
	).Error
	if err != nil {
		return agent.PutMemoryResponse{}, err
	}
	return agent.PutMemoryResponse{}, nil
}

// ListMemories ranks notes in this namespace by cosine similarity to
// req.Query, most-relevant first, capped at req.Limit (0 = no cap). Score is
// 1 minus pgvector's cosine distance, so it matches agent.Embedding.Cosine
// (identical direction = 1). The summary lists everything with an empty
// Query. There is no query to rank against, so that path returns all notes
// oldest-first with Score 0.
func (s *SemanticMemoryStore) ListMemories(ctx context.Context, req agent.ListMemoriesRequest) (agent.ListMemoriesResponse, error) {
	if req.Query == "" {
		return s.listAll(ctx, s.effNS(req.Namespace), req.Limit)
	}
	qvecs, err := s.embedder.Embed(ctx, []string{req.Query})
	if err != nil {
		return agent.ListMemoriesResponse{}, fmt.Errorf("gormstore: embedding query: %w", err)
	}
	if len(qvecs) == 0 {
		return agent.ListMemoriesResponse{}, fmt.Errorf("gormstore: embedder returned no vector for query")
	}
	sql := fmt.Sprintf(`SELECT body, 1 - (embedding <=> ?) AS score
		FROM %q WHERE namespace = ?
		ORDER BY embedding <=> ?`, s.table)
	args := []any{pgvector.NewVector(qvecs[0]), s.effNS(req.Namespace), pgvector.NewVector(qvecs[0])}
	if req.Limit > 0 {
		sql += " LIMIT ?"
		args = append(args, req.Limit)
	}
	return s.scanScored(ctx, sql, args)
}

func (s *SemanticMemoryStore) listAll(ctx context.Context, ns string, limit int) (agent.ListMemoriesResponse, error) {
	sql := fmt.Sprintf(`SELECT body, 0 AS score FROM %q WHERE namespace = ?
		ORDER BY created_at, key`, s.table)
	args := []any{ns}
	if limit > 0 {
		sql += " LIMIT ?"
		args = append(args, limit)
	}
	return s.scanScored(ctx, sql, args)
}

// scanRow is one row of the ranked query: the stored MemoryItem JSON plus its
// per-query similarity score.
type scanRow struct {
	Body  string
	Score float64
}

func (s *SemanticMemoryStore) scanScored(ctx context.Context, sql string, args []any) (agent.ListMemoriesResponse, error) {
	var rows []scanRow
	if err := s.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return agent.ListMemoriesResponse{}, err
	}
	out := make([]agent.ScoredMemory, 0, len(rows))
	for _, r := range rows {
		var item agent.MemoryItem
		if err := json.Unmarshal([]byte(r.Body), &item); err != nil {
			return agent.ListMemoriesResponse{}, fmt.Errorf("gormstore: corrupt memory row: %w", err)
		}
		out = append(out, agent.ScoredMemory{Item: item, Score: r.Score})
	}
	return agent.ListMemoriesResponse{Items: out}, nil
}

// DeleteMemory removes a note by key within this namespace. An unknown key is
// Deleted=false, not an error (the MemoryStore contract).
func (s *SemanticMemoryStore) DeleteMemory(ctx context.Context, req agent.DeleteMemoryRequest) (agent.DeleteMemoryResponse, error) {
	sql := fmt.Sprintf(`DELETE FROM %q WHERE namespace = ? AND key = ?`, s.table)
	res := s.db.WithContext(ctx).Exec(sql, s.effNS(req.Namespace), req.Key)
	if res.Error != nil {
		return agent.DeleteMemoryResponse{}, res.Error
	}
	return agent.DeleteMemoryResponse{Deleted: res.RowsAffected > 0}, nil
}
