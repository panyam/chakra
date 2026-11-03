// Package gormstore is the relational backend for agent.RunStore. It
// targets Postgres via GORM. SQLite also works, and the tests default to it.
//
// It is a sibling module with its own go.mod, so GORM and the database
// drivers are kept out of the root module. store/redis is split out the
// same way, and so is mcpkit's events SDK with its stores/gorm module. The
// wire format is encoding/json. Constraint A2 makes agent messages and
// events wire-serializable.
//
// AutoMigrate manages the schema on construction. Where an out-of-band
// migration tool owns the schema, disable it with WithoutAutoMigrate.
//
//	agent_runs          id (PK), parent_id, created_at
//	agent_run_messages  seq (PK, autoincrement), run_id (indexed), body
//	agent_run_events    seq (PK, autoincrement), run_id (indexed), body
//
// Each body is a JSON document in a jsonb column on Postgres, or in a text
// column on SQLite. A row holds one agent.Message or agent.Event, and loads
// order rows by seq. The run row marks that the run exists. CreateRun claims
// it with INSERT ... ON CONFLICT DO NOTHING. ForkRun copies both log tables
// inside one transaction, so a fork here is an exact cut, as it is in the
// Redis backend. An append racing the fork either commits before it and is included,
// or commits after it and is excluded. It is never half-copied.
//
// Schema changes follow mcpkit's fresh-deploys-only convention. Column
// changes ship with no migration recipe, and operators recreate the tables.
// DEPLOYMENT.md in mcpkit's events SDK (experimental/ext/events) gives the
// rationale.
//
// The package also ships two agent-memory backends:
//
//	ToolResultStore        agent.ToolResultStore (Postgres or SQLite)
//	SemanticMemoryStore    agent.MemoryStore, Postgres + pgvector ONLY
//
// SemanticMemoryStore is the durable counterpart to
// agent.InMemorySemanticStore. It embeds notes and queries client-side
// through the agent.Embedder seam. Then it runs an ANN top-k in SQL over a
// pgvector column ("ORDER BY embedding <=> $query LIMIT k"). So recall
// survives process exit and scales past a scratchpad. It has no SQLite
// path, because the vector type, the cosine-distance operator and the HNSW
// index are pgvector features. It assumes the "vector" extension already
// exists. The init SQL in mcpkit's docker/backends creates it. A namespace
// column (WithMemoryNamespace) lets many scratchpads share one table. The
// per-request Namespace field is issue 1003.
package gormstore
