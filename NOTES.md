# agent/ — implementation notes

Why the agent layer is shaped the way it is, and what bit us building it. Written for
whoever touches this code next, not for someone learning to use it.

- **How to use it:** `host/README.md`, `surfaces/chat/README.md`
- **Invariants:** `CONSTRAINTS.md` (A1–A9)
- **Design frames:** `docs/AGENT_DESIGN.md`, `docs/AGENT_COMPOSITION.md`, `docs/AGENT_MEMORY_FLOW.md`
- **Roadmap:** `docs/AGENT_SDK_ROADMAP.md`
- **Terminal surface lore:** `surfaces/chat/NOTES.md`

Phases 0–3 of the SDK roadmap have shipped. Phase 4 (durable workflows) was dropped as a
non-goal, recorded as constraint A8. Phases 5–7 remain open as epics 1050 / 1051 / 1052.

---

## The layering rule (A6)

Put a primitive in `client/` (or an events/skills SDK) if any non-agent consumer would want
it: a script, a service, a dashboard poller, `cmd/testclient`. Put it in `chakra` only if it
needs a model and a turn to make sense.

The tell is the natural return type. A protocol object (`core.DetailedTask`, `events.Event`,
`InputResponses`) is client-layer. A model-facing object (`core.ToolResult`, injected context,
a proactive turn) is agent-layer.

By that test, task polling, `BackgroundTask`, and `StreamChan` live in `client/`, while
`EventInjectionPolicy`, `TriggerPolicy`, and the Runner live here. The same test puts `RunStore`,
`ToolResultStore`, and `MemoryStore` in `chakra` rather than a root `stores/`. They
traffic in `agent.Message` / `core.ToolResult` / `MemoryItem`, so moving them to the root would
make the root depend on agent.

## Background goroutines

Use `core.DetachForBackground(ctx)`, never `context.WithoutCancel`. `DetachForBackground`
replaces the dead POST-scoped requestFunc/notifyFunc with the session-level persistent push.
Async sub-agents, the agent pool and task dispatch all outlive a turn *and* call MCP server
tools, so they all need it.

---

## Runner, control, and cancellation

`Runner.RunTurn(ctx, TurnRequest)` is the real entry point, and `Run` is a shim. The Runner is
a deterministic fan-out-then-join pure function over history. It holds no state between turns,
so one Runner can be reused across turns, and resume, fork, eval and compaction compose.

**Per-tool-call cancellation** goes over `TurnRequest.Control <-chan Control`. `Control{}` with
an empty CallID cancels every in-flight call in one send, and `Control{CallID}` cancels one. A
cancelled call feeds `"cancelled by user"` back to the model and **the turn continues**. To
abort outright, cancel `RunTurn`'s own ctx.

Cancellation must be checked on all three outcome shapes, and we got this wrong first.
Transports return an error. `FuncSource` converts a handler's `ctx.Err()` into an `IsError`
result. A success can race the cancel, and the user's cancel wins that race.
`EventToolCancelled` has a `Reason` field, not `Error`, so error-keyed eval scorers do not
count interrupts as errors.

`Control` is the extensible steering envelope, and future verbs are additive `Kind` values.
Mid-turn *content* ("/btw") is deliberately not a Control. It routes through injection so it
ends up in history.

### Upward signals (#1165, PR 1168)

A child calls the `signal_parent` tool to raise a **non-referential** `Signal` to its parent.
`SignalKind` is one of `escalate`, `custom` or `preempt`. The child reports only its own state and
never names a sibling, because under A7 it cannot know what its siblings are doing. The parent holds
the fan-out inventory and its own policy, so the parent decides what to do about the others.

`cancel_siblings` was deliberately dropped as a kind. It implies sibling-awareness, and it
would do nothing anyway. A non-interruptible parent reads signals at the join, and by then the
siblings have already joined.

**Two-key ctx sink.** We first had a single key, and caught the bug before tests did.
`dispatchSinkKey` holds a dispatch's sink for its own children. `AgentSource.Call` snapshots the
spawner's sink into `parentSinkKey`. `RaiseSignal` reads `parentSinkKey`, so a child raises to
its spawner. If it read its own dispatch sink, that sink would shadow the spawner's. A
grandchild therefore reaches its immediate parent. Guarded by
`TestUpwardSignal_NestedReachesImmediateParent`.

The parent drains signals at the dispatch join. It reacts through `RunnerConfig.SignalPolicy`
(built-in `AbortOnEscalate`) or by injecting a `RoleSystem` note into the next step.
`EventSignal` reports each signal to surfaces.

### Interruptible turn (#1167, PR 1170)

`RunnerConfig.Interruptible` is off by default. When it is on, `dispatch` breaks the fan-out
join barrier on the first mid-flight signal. It cancels the remaining calls, returns partial
results, and lets the existing `RunTurn` loop re-plan. `RunTurn` did not change, because the
existing post-dispatch loop already re-enters.

**Calls run under their own ctx, `callBase`, separate from the step's `parent`.** A
fan cancel must leave the *step* ctx (`parent`) live. Then cancelled siblings read "cancelled
by user", and the turn does not abort. `callTool`'s `cancelled()` is
`call-ctx-done && parent-live`. Calls run under a cancellable `callBase = WithCancel(parent)`.
In non-interruptible mode `callBase == parent`, so the default path is byte-identical. After
the cancel, `dispatch` still calls `wg.Wait()` so every result slot is filled, because providers
require a result per tool call. `dispatch` returns `([]Message, []Signal)`.

### preempt is parent-granted, not child-authoritative (#1176, PR 1178)

A `custom` FYI signal never breaks the barrier. `escalate` always breaks, because the parent
must handle it. A `preempt` breaks and cancels the in-flight losers **only when
`RunnerConfig.PreemptGrant` honors it**. The default is nil, and then a preempt is only
injected. The model decides on re-plan, so a rogue or prompt-injected child cannot
unilaterally kill its siblings.

Under A7 a child cannot know whether the tree as a whole has enough, so its preempt is
advisory. `signalSink.breakOn` gates `raise`'s notify, and the Runner sets it to
`Runner.shouldBreakOn`. `agent.GrantAllPreempts` is the trust-all grant. The host exposes it as
`Config.AllowPreempt`, which defaults to false.

Letting the grant select *which* in-flight calls to cancel via the #936 registry is #1177.

### Tree budget (#1032, PR 1162)

`agent.TreeBudget{MaxSteps, MaxTokens}` is a shared counter threaded through ctx. It caps total
model steps and tokens across a turn's whole tree, which covers the parent, sub-agents, fan-out
members and handoff rounds. It caps the aggregate. Per-Runner `MaxSteps`, `WithAgentCallBudget`
(call count) and `Team.MaxHandoffs` each cap one piece.

The top-level Runner installs it from `RunnerConfig.TreeBudget` if ctx has none yet. Every child
inherits the same live counter through ctx, so the sharing needs no plumbing beyond existing ctx
propagation. Steps are pre-decremented at the step-loop top; tokens are post-hoc from provider
`Usage`, so a turn can overshoot `MaxTokens` by at most one step. Exhaustion raises `ErrTreeBudget`,
which `AgentSource` softens to an `IsError` result for a sub-agent.

---

## Memory

Design map: `docs/AGENT_MEMORY_FLOW.md`. Phase 2 (epic 926) is complete.

### The MemoryStore seam

`agent/memory.go`: `PutMemory` / `ListMemories(Query, Limit)` / `DeleteMemory`, gRPC-style
req/resp, unknown-key delete is app state rather than an error.

**`ListMemories` returns `[]ScoredMemory`, not `[]MemoryItem`.** `Score` is per-query. It is
a field on the result, not the stored item, so a reranker can add signals without touching the
fact.

**Retrieval sits behind this one interface.** Substring, O(n) cosine and pgvector ANN all
implement the same `ListMemories(Query, Limit=k)` signature. There is deliberately no separate public
`VectorStore` seam; #1021 revisits that only if doc-RAG needs one.

Implementations: `InMemoryMemoryStore` (substring, Score 1), `InMemorySemanticStore` (Embedder
plus brute-force cosine), `redisstore.MemoryStore` (durable substring), and
`gormstore.SemanticMemoryStore` (durable pgvector ANN). `InMemorySemanticStore` used to be
`SemanticMemoryStore`. We renamed it because "semantic" is a property of a MemoryStore impl,
not a separate interface.

### Namespace (#1003) and session scoping (#1140)

`Namespace` is a **per-request field** on `PutMemoryRequest` / `ListMemoriesRequest` /
`DeleteMemoryRequest`. Each namespace is an independent scratchpad for one session or user. An
empty namespace means the shared default, and recall never crosses namespaces. Every backend
honors it. The in-memory stores partition their maps. `gormstore.SemanticMemoryStore` reads
`req.Namespace` and falls back to `WithMemoryNamespace`. `redisstore.MemoryStore` stores each
namespace as one Redis hash (`<prefix>:<namespace>` → key→JSON,
`DefaultMemoryKeyPrefix = "mcpkit.agent.memory"`). It preserves CreatedAt on update so order is
stable, and an optional per-namespace TTL (`WithMemoryTTL`) cleans up abandoned sessions.

`MemorySource` threads it via `WithMemoryNamespaceFunc(func() string)`; nil means shared.

**The deadlock trap.** `MemoryConfig.SessionScoped` makes `registerMemory` pass
`WithMemoryNamespaceFunc(a.currentRunID)`. The memory tools and summary/recall injection both
call the namespace func *inside a turn, while `turnMu` is held*. `App.RunID()` takes `turnMu`,
so calling it there would re-enter and deadlock. The fix is a lock-free `currentRunID()`. It
reads an `atomic.Value` mirror of `runID`. All four writers (`AttachRun`, `resumeLocked`,
`Fork`, `ensureRunLocked`) go through one `setRunID`, so the mirror cannot drift.

A durable *semantic* Redis backend is still a follow-up; durable semantic recall is the
pgvector store's job.

### pgvector store (#1019, PR 1047)

Postgres plus pgvector only, no SQLite path. The vector type, the `<=>` cosine-distance
operator and the HNSW index are all pgvector features. The store needs the `vector` extension
but does **not** install it, because `CREATE EXTENSION` needs privileges.
`docker/backends`' `init/01-agent.sql` creates it on a fresh volume only. Tests are PG-only and
skip when it is unavailable, so the sqlite-first RunStore and ToolResult tests are unaffected.

`PutMemory` embeds `key+" "+value` client-side and upserts `(namespace, key, body JSON,
embedding, created_at)`. **On conflict it preserves `created_at`**, so listing position is
stable.
`ListMemories` embeds the query, orders by `embedding <=> $q LIMIT k`, and reports
**Score = 1 − cosine distance** to match `Embedding.Cosine`. An empty query gives oldest-first
with Score 0. Dimension is `WithVectorDimensions` (default 1536) and **must match the
Embedder** or pgvector rejects the write.

**The constructor checks that the table name is a plain identifier** (`^[A-Za-z_][A-Za-z0-9_]*$`).
The name goes into raw DDL/DML, and identifiers cannot be bound. `%q` is Go escaping, not Postgres
identifier quoting, so validation is the right tool rather than quoting.

### MemorySource and injection

`MemorySource` is a leaf ToolSource like `FuncSource`: `remember` / `recall` / `forget` tools,
plus `Summary(SummaryOptions)` (ambient, recency-budgeted) and
`RecallRelevant(query, RecallOptions{TopK, MinScore})` (relevant to this turn). Tool names are
exported constants and deliberately fixed.

**Memory injection is two transient, self-capping producers.** They write into the per-turn
slice before the user message, **never into `a.history`**. Before #1010 they appended a
snapshot every turn, and the snapshots stacked up in both history and the RunStore log. Events
are different. They do persist, because each one is drained once.

- `Summary` (InjectSummary, recency-budgeted, #1011)
- `Recall` (InjectRecall, top-K over `RecallMinScore`). A semantic store scores *every* note,
  so the `MinScore` floor is the **poison guard**.

Each is its own step and is **not routed through `EventInjectionPolicy`**. Recall is a query
result, not an event with a name, hint, and window. PR 1016 renamed that type from
`InjectionPolicy` for this reason. A unified budget across events, summary and recall is the
deferred arbiter (#1024). The order of the pre-turn transforms is implicit and grew by
accretion. Compaction runs in the Runner, so injection happens before compaction without
anyone choosing that. An explicit context pipeline is #1026.

### Compaction

`agent/compaction.go`: `SummarizingCompactor` plus a `TokenEstimator` / `CharTokenEstimator`
heuristic, hooked as `RunnerConfig.Compactor`. It runs **in the Runner**, not in the host, as the
first turn-scoped stage of the step chain before every model call (chakra#7). The eval harness
builds a RunnerConfig, **so it can grade compaction**. It rewrites the turn's working
messages, never the record. `EventCompaction` fires only on a real shrink, with the step. An error
aborts the turn, as Selector does. Imagine a turn past step 1 that still doesn't fit after
compacting. It ends with `ErrContextBudget` and keeps its messages, rather than summarizing a
summary every step.

### Embedder

`agent/embedder.go` is a sibling seam to Provider: `Embed([]string) ([]Embedding, error)`.
**`Embedding` is a defined type `[]float32` with a `Cosine` method**. A future SIMD/normalize
kernel can go there (#1018). `OpenAIEmbedder` is no-SDK net/http against `/embeddings`;
`StubEmbedder` is deterministic bag-of-words hashing so the semantic path is CI-testable.

### Sub-agents have no working memory (A7, #1151, PR 1153)

Enforced, not conventional. The memory source is registered on the main `multi`. Personas get
a filtered `serverTools` view, and `AgentSource.Call` bypasses the injection.
`TestSubAgentCannotReachParentMemory` guards this. We checked that it fails if personas are
handed `multi`.

A child may not run in the same place as its parent. In-process `AgentSource` is the degenerate
co-located case. Shared parent memory would assume co-location, and A2 already forbids that
assumption. A child that needs memory **owns its own setup entirely**. Whoever builds the child
configures its store and its namespace, just as a stateful MCP tool owns its DB. Never
point a `WithMemoryNamespaceFunc(a.currentRunID)` at the *parent's* namespace.

Parent→child transfer is params plus injection. Hierarchical recall across children is deferred.
It needs a prefix or hierarchical namespace query, and the seam has only exact match today.

---

## Tool-result offloading

The just-in-time-context layer: lossless, pay-on-lookup, complementary to compaction rather
than a replacement (#966/#971/#972/#973/#977).

`OffloadingSource` wraps a `ToolSource` like `FilterSource` does. It stores an over-threshold
*successful* result and replaces it with a **stub**. The stub is a normal `ToolResult`, and its
text is a preview plus a ref. The RoleTool message, the `tool-end` event and the persisted log
all hold the stub. So the log matches what the model actually saw, and the Runner needs no
change.

Wrap the **aggregate** MultiSource, so one `read_tool_result` and one store cover every server. The
host does this when `Config.Offload != nil`. **An `IsError` result is never offloaded.** Truncating
an error is worse than its size. A per-tool threshold of `0` pins a tool inline. Size is measured on
`toolResultText`, **text only**, so a large base64 blob will not trip it yet (#979).

`ToolResultStore` (`agent/toolresultstore.go`) is `Put`/`Get` with an in-memory default. **Retention
is deliberately not in the interface.** Imagine the model still holds a stub, and the backend evicts
its ref. The model calls `read_tool_result` on it and gets "no longer available" with
`IsError:false`, and the turn carries on. So any backend may evict. Redis uses native TTL, and gorm
uses a caller-driven `PruneExpired`. The dep-free `FileToolResultStore` is in `chakra` itself. It
uses only the stdlib, writes one JSON file per ref with temp-then-rename, and escapes ref→filename
injectively. It suits a local coding agent with no server, because the blobs are files the agent can
read directly. The gorm backend supports bring-your-own-table via `WithToolResultTableName`,
resolved through `db.Table`.

`read_tool_result` takes `{ref, offset?, limit?, pattern?, context?}`: a char window or a regex grep
*into* the blob. Query, don't page. Text-only today.

**`context` is two-sided, and that is not symmetry for its own sake** (#41). Grep returns lines that
match. A caller can often name one line, but the information is on a different line. `go test -v`
prints the assertion *above* the `--- FAIL` marker, and plain `go test` prints it *below*. A caller
grepping the marker needs the opposite side in the two cases, and it can't tell which case it is
looking at. One knob always reaches the detail. Two knobs would need knowledge the caller does
not have. Both shapes are pinned by real captured transcripts in `testdata/`.

Grep is also **bounded** now (`defaultGrepMaxLines`, overridable by `limit`) and says what it cut.
It used to return every match however many there were. Imagine a loose pattern over a big
transcript. Grep would put back inline the payload that offloading had just removed. Context
multiplies that by `2N+1`, so once we added the field, the bound was no longer optional.

**The cap has to be downstream, and in `ext/exec` it is not** (#41, measured). The paragraph below
warns against reinventing truncation, and `ext/exec`'s `capBuffer` does exactly that. It keeps 32
KiB head+tail and drops the middle *inside* `Source.Call`. `OffloadingSource` only ever sees the
returned `ToolResult`, so it never gets the dropped bytes and cannot serve them. We fed it a real 42
KB `go test ./... -v` transcript with one failure. Only the trailing summary word `FAIL` survived,
and both the assertion and the `--- FAIL: TestX` line were gone. The model learns that something
broke and gets nothing to act on. Raise `exec.Config.MaxOutput` and the full transcript reaches the
store. Only then does the model have anything to grep. Ordering beats tuning here.

Deferred: binary offloading (#979), and streaming/handle-based results for hundreds of MB via
MCP resource links plus a ranged store (#980). `Put` is not the bottleneck. Upstream
materialization is.

**A new tool must not reinvent this** (#1284). Writing `list_files` and `search_files`, the obvious
instinct is to cap the returned bytes so a big repo cannot blow the context window. That is already
solved, one layer up, for every tool at once. Two truncation mechanisms then disagree about the same
output. The tool's cap cuts first, so it also throws away bytes the store would have kept.

The line is A6. A **result-count** limit belongs to the tool: it bounds the work actually done and
means something to a caller ("the first 100 matches"). **Context economics** belong to the agent
layer, where `OffloadingSource` wraps the aggregate and already owns them. So those tools take a
`limit`, report what it dropped, and do no byte-level truncation at all.

---

## Safety, approval, and reversal

### Annotation defaults are not symmetric (#1260, PR 1265)

`toolHints` in `agent/runner.go` resolves `readOnlyHint`, `destructiveHint`, and `idempotentHint`
in one function rather than three reads. Their spec defaults disagree with Go's zero value, and in
different directions. Absent `readOnlyHint` means not read-only
and absent `idempotentHint` means not idempotent, so both fall to `false`. **Absent
`destructiveHint` means destructive.** Imagine a caller reads the annotation map directly and takes
the zero value. It would treat every unannotated tool as safe to run unattended.

`readOnlyHint` wins over the other two. The spec says those two mean something only when a tool
writes. So a read-only tool reports non-destructive and idempotent, whatever else it declared.
Passing a server's self-contradiction through to policy is worse than normalizing it.

We chose plain bools that the Runner resolves. The downside is that a hand-built
`ToolCallInfo{}` reads as non-destructive. The field's doc says so, and the tests exercise the
resolution path rather than hand-set values. We considered a tri-state and rejected it, because it
adds a type to a struct that is heading for the 1.0 freeze.

`ModeReversibleAuto` keys on the result. Against servers that skip `destructiveHint` entirely it
behaves exactly like `ModeAlwaysAsk`. We want it to fail in that direction. The host's
`"auto-edit"` alias now points at this mode. It used to point at `ModeReadOnlyAuto`, which never
auto-allowed an edit.

### Spotlight provenance: only `operator` exempts (#1262, PR 1266)

`SpotlightConfig.Classify` returns one of four labels instead of a bool. It is tempting to exempt
`server` as well as `operator`. We don't, because **trusting the relay is not trusting what it
relayed.**
Imagine the operator runs a server, and it returns a third-party document. That server is a
courier, not an author. So only `operator` output reaches the model unfenced. That label means the
process computed the output rather than passed it along.

`resolveProvenance` closes the label set *before* the exemption check, so `"Operator"`,
`"oprator"`, and `""` all fence rather than exempt. The set is deliberately closed rather than
pass-through. Someone may plausibly want a finer taxonomy. Opening the set later is additive, and
closing it later would break callers.

The default fence describes the origin in prose ("from a source the operator has not vouched
for") rather than emitting the raw label. A model was never told what `world` means, and the prose
still makes sense to it.

### Provenance is derived from the source, not restated in config (#1268, PR 1274)

`spotlight.tools` shipped in #1262 as the only input. It is correct only while someone maintains
it. Connect a server, and every tool it advertises silently defaults to `world`. Then the labels
stop distinguishing anything, and the differentiated fencing never gets used. The host
already knows what each source is, because it chose the id when it called `Add`.

The derivation table lives in `agent/host/provenance.go`:

| source id | label |
|---|---|
| a connected MCP server (`sc.ID`, recorded at Add time) | `server` |
| `subagent:*`, `fanout:*`, `serveragents:*` | `agent` |
| `host`, `runner-control` | `operator` |
| an extension, or anything unmatched | none → `world` |

**`operator` is a closed allowlist and must remain one.** An extension is arbitrary code that may
shell out or fetch, so the host has no standing to vouch for it. If the host derived `operator` for
an extension, the mitigation would be silently *disabled*. Every other mistake here only makes it
noisy. Everything unmatched falls through to `world`, which marks.

Two things that shaped the implementation:

- **You cannot map a tool name back to its source by matching the name.** A colliding name is
  offered to the model as the qualified `sourceID/name` form, so the classifier sees either shape.
  `MultiSource.OwnerOf` shares `resolveLocked` with `Call`, and `resolveLocked` returns the source
  id rather than the source for this reason. Imagine the classifier disagreed with the dispatcher.
  It would attribute output to a source that never served the call. Not labelling the output is
  better than that. `TestMultiSourceOwnerOfMatchesCall` pins the two against each other through a
  `Resolver`.
- **An ambiguous name gets no label rather than a guess.** Picking the first claimant would give a
  plausible answer, but `Call` would not have acted on it.

The classifier has a 2s timeout. `SpotlightConfig.Classify` takes no context. An index miss makes
`MultiSource` re-list every source, and that goes over the network. An unreachable
server must not stall tool dispatch.

In #1273 an extension's tools got the `world` fence. The fence said "fetched from outside this
system", but the code ran in-process and fetched nothing. The marking was right and the sentence was
false. For a safety feature that is the worst combination. A fence that gives a wrong reason
teaches the model that its claims are approximate.

The fix softens the clause to "from a source the operator has not vouched for". That is true of a
fetched page and of an in-process extension tool alike. The enum still has four labels. A fifth
(`local`, `unvouched`) would reopen the taxonomy #1262 settled, for one case. We could let an
extension declare its own provenance. Then the thing being classified would make a
security-relevant claim about itself. A server does not get to label its own output for the same
reason.

The cost is real and small. The `world` fence is now marginally less specific for genuine web
content, and the strongest strategies target that case. The parts that matter most (UNTRUSTED,
DATA never instructions) are untouched, and `ProvenanceWorld`'s doc still names web content as the
motivating case.

**The tree's two fence texts still do not collapse.** The write-up before the change said they
would. `fenceCritiqueReason` gives two reasons for not reusing `delimitMark`. The sentence names a
tool, and it claims a fetch. This fixes only the second. To collapse them, `delimitMark` would have
to stop naming a tool or take a caller-supplied source clause. That is a refactor, not a byproduct.
And the two describe different things anyway. One describes a tool result, and the other a policy
verdict.

That PR also corrected three comments in `agent/multi_source.go`. They described the qualified
form as `sourceID_name`, but the code has always joined with `/`. `Add` rejects `/` in a source id
*because* it is the separator. We found the stale docs only through a red test.

### Reversal: restore and compensate are different operations (#1267, PRs 1270/1271/1272)

`ext/checkpoint` holds the seam. We rewrote it to avoid a design mistake. You can put undo in the
host only if you first restrict undo to files, and that assumes the conclusion. The host cannot
know how to undo `create_issue`, but the tool author can. Files are the first
implementation, not the definition.

`Reversal` has two fields, because there are two kinds of reversal:

- **Restore** is a true inverse. Local, order-independent, idempotent, unaffected by intervening
  work, near-certain to succeed. The harness runs it unattended.
- **Compensate** is a new action that partially offsets an old one. Not an inverse (notifications
  fired), order-dependent, fails on permissions the original never needed, and breaks once
  something depends on the effect. **Never auto-run.**

`Reversal.Reversible()` requires a `Restore`. If a compensation counted, a tool could auto-approve
under `ModeReversibleAuto` because of an offset nobody verified. Chaining compensations
automatically would be a saga orchestrator, which A8 rules out.

Three more things bit us or nearly did:

- **Restore is idempotent, not atomic.** POSIX has no multi-file rename. Restore first stages
  everything to temp files beside their destinations, then renames. So a missing blob or an
  unwritable directory fails before the tree is touched. A failure during the rename phase leaves a
  mix. The fix is to run it again, because the manifest is never consumed. Claiming atomicity would
  read as a guarantee and provide none.
- **Capture runs outside the permission gate**, because extension middleware runs before the gate.
  If the gate then denies the call, the call is still captured. That is harmless, because restoring
  an unchanged file is a no-op. The alternative is capturing after the pre-state is gone.
- **`/undo` reports what it could not undo.** Imagine a turn edits three files and files an issue.
  Without the report, `/undo` says "3 files restored" and never mentions the issue. A safety net
  with an unreported hole is worse than none, because people stop checking it. The report leaves
  out read-only and denied calls. Neither had anything to undo, and people learn to skip a padded
  list.

### The undo proposer runs in a fresh context (#1267, PR 1272)

`ModelProposer` builds its conversation from the gap list alone, and the turn's history has no path
to it. This is a security property, not tidiness. Imagine the turn went wrong through prompt
injection. If the cleanup ran inside that context, the attacker would write the cleanup. The gap
list is itself untrusted, because it holds arguments the model chose, possibly under influence. So
a proposal goes to a human rather than to the dispatcher.

This is an open question for the freeze. `Reversal.Compensate` and the proposer tier both suggest a
call, and a human approves it. They differ only in who wrote the suggestion. If `Compensate` never
gets a producer, they collapse into one mechanism with two entry points.

### Confining a path is not a check, it is a handle (#1275, PR 1278)

`ext/files` shipped an escape. The fix is worth keeping, because the reasoning applies to anything
that takes a path from tool arguments.

The first version resolved symlinks on a path's **parent directory** and then appended the basename
unresolved. A symlink as the *final* component walked straight out. `workspace/innocent.md ->
/etc/passwd` passed containment, and then the open followed it. The test for this used a symlinked
*parent* (`link/secret`). Parent-resolution catches exactly that case, so the suite was green and
the property did not hold. A test can exercise the one member of a class that already works, and
it still reads as coverage of the whole class.

The missing case was not the fault. **Checking a path and opening it by name are two operations, and
every way out of a directory is in the gap between them.** The open follows a symlink, and the check
does not. So a check on the path text describes a name, not the file that name will reach. The
filesystem can also change in between, so even a correct check describes the past. A `resolve`
function can fix neither, because the check is separate from the open.

So confinement is an `os.Root` handle. It resolves each component at open time against the directory
it holds, and refuses anything that leaves it. Check and use become one act. That closes the TOCTOU
window and the symlink escape together. `files.rel` remains only to produce a readable refusal
before any syscall. Its doc comment says it is not the enforcement, so nobody audits it for
completeness it was never meant to have.

A follow-on: the temp file's mode is set with `fchmod` on the descriptor, because chmod by name
re-resolves that name and is documented as racy even through `os.Root`.

The same handle covers traversal. Traversal is easy to build separately and get wrong (#1284).
`fs.WalkDir` over `root.FS()` cannot leave the root, and it does **not** descend into a symlinked
directory. So we wrote no traversal logic, and still neither an escape nor a link cycle is
reachable. A listing still shows a symlink as a name, which is useful. Reading *through* it is
refused. `filepath.WalkDir` over a joined path would need its own containment argument.

One more design note: `write_file` has **no create-or-overwrite mode**, on purpose. Without
`expect_hash` the call is a create, refused if the path exists. With it the call is a replace,
refused if the hash is stale. So no argument combination writes content to a file nobody looked at.
A convenience flag would be an escape hatch, and the anchored editor would become decorative. A
model reaches for such a flag as soon as anchoring feels awkward.

### The same shape in checkpoint wanted a different answer (#1281, PR 1283)

`checkpoint.Restore` had the same path-then-open fault and did **not** get the same fix. This half
is the more interesting one.

It renamed and removed at whatever absolute paths the manifest held, without looking at what was
there. Those paths come from `WriteSpec.Paths(args)`, so they originate in tool arguments. The gap
here is not microseconds. Capture happens at the top of a turn, and restore happens whenever
someone types `/undo`. A path has minutes or hours to become a symlink. Then the rename writes
captured content to the link's target. A path captured as absent is undone with `os.Remove`, and
that deletes the target outright.

An `os.Root` handle was the wrong instrument. It answers *containment*, and checkpoint has no root
to be contained by. A checkpointed tool may legitimately write to a cache or temp directory outside
any one tree. A workspace root would break honest callers to stop a dishonest one. Confining paths
is still the **tool's** job, and `ext/files` is the worked example.

Checkpoint got an *integrity* check instead, and that needs no root. The manifest records what
`Add` found: `kindRegular`, `kindAbsent` or `kindUnsupported`. `Add` uses `Lstat`, so it records a
link rather than following it. `Restore` compares the record against what is there now. No caller
wants to restore through something other than what was captured, whatever their layout. So
refusing blocks no legitimate use.

Three consequences worth keeping:

- **Refusals are per path.** One tampered file should not block restoring the rest of the turn. The
  staging phase already gives a clean all-or-nothing abort for real errors.
- **`Restore` returns a `RestoreResult`.** With per-path refusal, a partial restore is a normal
  outcome. `error` alone cannot express it. A caller that saw nil would conclude the turn was fully
  reversed. That is A11's corollary exactly.
- **Through the `Reversal` seam a refusal is an error.** The caller there is the harness, running
  unattended with nowhere to put detail. `/undo` calls `Restore` directly and prints each one.

The capture-side and restore-side `Lstat` catch different cases. Restore-side stops writing through
a link that appeared since. Capture-side stops the blob store from storing a link target's content.
Imagine the link is later replaced by an ordinary file. Restore would see something perfectly
normal and write the wrong content into it.

### Two extensions compose at the wiring layer, not by importing each other (#1275)

`ext/files` is checkpointable and contains no reversal code. `checkpoint.WriteSpec{Tool, Paths}` is
a declaration keyed by **tool name**, and whoever builds the host supplies it. `files.EditPaths` is
deliberately a plain `func(map[string]any) []string` rather than a `checkpoint.WriteSpec`, so
neither module imports the other.

The obvious alternative is an edit tool that declares its own `checkpoint.Reverser`. C4 exists to
prevent that. Checkpoint's API would become implicitly stable for the edit tool's benefit, and no
design decision would say the two must interoperate. `make check-ext-isolation` now enforces this
for `ext/` as well, in CI (#1277). Before that, C4's verifier was a snippet inside
`CONSTRAINTS.md`. It walked two trees, and nothing ran it.

---

## Persistence

`RunStore` is in `agent/runstore.go`, per the A6 corollary above. It uses gRPC-style req/resp per
`stores/STORAGE_SEAMS.md`, and an unknown RunID is app state (`Found=false`), never an error.

**Atomicity is a contract on the interface.** Explicit-ID `CreateRun` and `ForkRun` are
all-or-nothing, and `NewRunID` is a retry idempotency key. Each backend uses its own native
primitive. Redis uses one atomic Lua script, with the meta-write last and the cluster caveat
documented. Gorm uses one transaction, on sqlite and Postgres. `make testpg` boots a container on
**5435**, because the events gorm store uses 5434. Both ForkRun implementations document why each
backend uses its native atomicity primitive and never hand-rolled claim-last ordering.

**Runs are flat copy-on-fork lists.** Messages carry no IDs or parent pointers. `Run.ParentID`
and `ForkPoint` are lineage metadata only, and nothing reconstructs history by walking them.
**`ForkPoint` is a message count, not a timestamp.** The issue called it `ForkedAt`, and we renamed
it because that name was ambiguous. `ForkRunRequest.AtMessage` forks from an earlier point. That
gives the conversation half of checkpoint/rewind. **A partial fork copies no events**, because the
audit stream can't be sliced by message index. Stored blobs are immutable, so #966 offloading will
need no work on fork.

**Stores stamp `Message.Timestamp` on AppendMessages.** A zero timestamp gets the store clock, and
a caller-set one wins. Trace context follows the same caller-preserves rule. The field uses
**`omitzero`, not `omitempty`**, because omitempty does not omit a zero `time.Time`. Providers
never map it into request bodies. The stamp is inside the stored JSON body, so there is no schema
column and forks copy it for free.

Host wiring is `agent/host/persistence.go`: `WithRunStore` plus `AttachRun` (create-or-resume),
`Resume` (must-exist), and `Fork`. It persists at the `RunTurn` append site, and `PersistingEmit`
buffers the event stream into one `AppendEvents` per turn. A failed turn persists its events
(tool calls, the error) but no messages. `RunTurn` rolls the history back, and the message log has
to match it. Before #71 a failed turn persisted nothing. If it had made edits, a restart lost the
record of them. Persistence failures render a warning and never fail the turn.

Retention and GC are still open (#999).

---

## Composition

Design frame: `docs/AGENT_COMPOSITION.md`. Epic 927 is complete.

**The two-axis model.** Multi-agent is not a new engine. It wraps the same stateless Runner.
Context comes in through injection, one seam for server events, memory recall and handoff context.
Control is tools and signals. Observability is the emit stream, which `SubAgentEvent` nests.

### AgentSource

Agent-as-tool: a child `*Runner` exposed as a `ToolSource`. `Call` runs the child over its own
isolated slice, seeded with `{task}`, and returns its final text. Isolation is structural, so
the Runner never changes. **A `MultiSource` of `AgentSource`s gives you supervision** through the
existing aggregation, collision handling, and Selector.

Guards: per-source `MaxDepth` (ctx-threaded, default 3) and an optional ctx-threaded aggregate
`WithAgentCallBudget` across the whole tree. A refused or failed child returns an `IsError` result,
so the parent's turn continues. Only an unknown name is a dispatch error.

**The LLM decides to call a sub-agent**, by the tool's name and description. The parent code
does not route. The Runner is a dumb dispatcher and parallelizes tool calls within one turn, so
N sub-agents invoked in one turn run concurrently with ordered results.

**Structured I/O** (#1033, PR 1159). With `AgentSourceConfig.InputSchema json.RawMessage` set, the
tool advertises that schema instead of `{task}`, and `Call` seeds the child with the raw args
JSON. Structured *output* is orthogonal and needs no new field. If the child ran with a
`RunnerConfig.ResponseSchema`, `result.Structured` is non-empty and `Call` returns it. Otherwise
`Call` returns `result.Text`.

**Colocation is contained, not removed.** The `ToolSource` contract is location-independent, and
`AgentSource` is its in-process implementation. A remote sub-agent is a *sibling* implementation.
It is largely "a sub-agent published as an MCP server, reached via the existing server-tools
ToolSource". In-process, composition metadata (depth, budget, cancellation, nested event stream)
comes along for free. #1035 / #1036 / #1038 cover sending it over the wire. Lifecycle is spread
out rather than centralized. There is no `SubagentManager`, and a real lifecycle owner appears only
in the async/Task form.

### SubAgentEvent

`SubAgentEvent{Scope, Depth, Event}` forwards the child's turn-lifecycle emit stream to the
parent's surface via `AgentSourceConfig.OnEvent`. Scope and depth go on the **envelope**, so
`Event` is still wire-flat (A2). Scope is ctx-threaded, so nested sub-agents compose the path
(`outer/inner`).

This is **not** MCP domain-event subscription. A sub-agent's `subscribe_events` injects into its
own context, never the parent's. The parent sees the child's *activity*, not its *inputs*.

### Async sub-agents (#1035, PR 1161)

`AsyncAgentSource.Call` acks immediately ("sub-agent X started") and runs the child on a
`core.DetachForBackground` goroutine, not `context.WithoutCancel`. The child outlives the turn
*and* calls MCP server tools, so it needs the session push. On finish, `OnComplete(name,
*TurnResult, err)` delivers the result. Depth and budget are checked at spawn.

Host `onAsyncComplete` works like `tasks_bg.go`. It ingests a `subagent.completed`
`IncomingEvent` into the injection policy, and also calls `triggers.OnEvent` and posts a
`HostMessage` notice. The result is then drained into the next turn's context, or it fires a
proactive turn.

**It is not an MCP task.** It has no wire and no model-visible poll or cancel, and the ephemeral
goroutine dies with the process. For poll/cancel/durability use a real `ext/tasks` task. The
Tool-vs-Task-vs-real-Task ladder is in `docs/AGENT_COMPOSITION.md`.

### FanOutSource (#1033, PR 1155)

A leaf ToolSource with one tool. The tool broadcasts a task to N member `*AgentSource`s concurrently
and returns one combined result. Each member gets a goroutine and a per-index result slot, and the
slots are aggregated in member order. Because it reuses AgentSource, per-member `MaxDepth`,
`WithAgentCallBudget`, and scope all apply. A member failure is **isolated** into the aggregate and
marked, never a dispatch error.

This is broadcast-one-task-to-all (ensemble). Map-style distribution of distinct subtasks was
not part of #1033 and remains unfiled. It is the substrate for sampling/vote (#1056).

Host: `Config.FanOut []FanOutGroupConfig` → `registerFanOut`, added under `fanout:<name>`. The
source id bans `_` and the tool name keeps it, so underscores in the id become dashes.

### Consensus: seeded draws with a vote (#202)

`Consensus` runs one turn N times and votes on a key the caller supplies. jaala's translator did
this in its own loop (`frontends/nl/vote.go`), and nothing in it was about Datalog except the key,
so it moved down. It calls `Runner.RunTurn` and nothing else, so it isn't a workflow engine (A8).
Every draw is the same turn, and the caller decides what to do with a split vote.

A few things we found while building it:

- Draw `i` runs with seed `i`, and only when `CapabilitiesOf` says the provider takes a seed.
  Unseeded draws at temperature 0 agree by construction, so the result says `Seeded: false`
  instead of passing 5/5 off as confidence.
- The Runner's `TreeBudget` has to be installed once, before the first draw. `RunTurn` installs
  it only when the context has none, so without that each draw would get a fresh budget and a
  vote could spend N times the cap.
- On llama-server, a seed repeats its draw only on a single-slot server with draws run one at a
  time. With the default four slots, a rerun of the same seeds drew differently in 2 of 8 tries,
  and `Parallel: 2` on one slot in 1 of 8. As far as we can tell, a draw that hits a
  different slot's cache samples differently. The live test skips unless the server was started
  with `-np 1`.

### Team (handoff)

`Team` does not fit agent-as-tool, and no other mode is like it. A handoff transfers control
rather than returning. `NewTeam(TeamConfig{Members, Start, MaxHandoffs, OnHandoff})` builds it.
Each `TeamMember{Name, Config, HandoffTo}` gets `transfer_to_<name>` tools for its allowed targets
only, and terminal agents get none.

`Run` loops. The active agent runs. If a `transfer_to` fired (a per-Run ctx signal), `Run` swaps
the active Runner and continues over the **same shared history**. Otherwise it returns the final
answer. Membership is static, and dynamic model-driven composition is #1038. Ping-pong is bounded by
`MaxHandoffs` (`ErrMaxHandoffs`).

Handoff is inject-context plus schedule. The shared-thread swap is one implementation, and
per-agent-context plus injection (the actor form) is the general one.

`Team.RunTurn(history, active)` is history-aware and returns all-hops messages plus the ending
active agent. `Team.Run` delegates to it.

**Per-agent event tagging** (#1033, PR 1159). `TeamConfig.OnEvent` forwards each member's events
tagged `{Scope: activeAgentName, Depth: 1}`. It **replaces** the raw emit, so nothing fires twice.
The raw emit is the fallback when OnEvent is nil.

**The persistence gotcha.** If attributed events tee back through the render path, they render
twice. So team mode builds a buffer-only `PersistingEmit` with `NewPersistingEmit(store, id, nil)`.
The nil next means persist without rendering. A per-turn `a.teamEventSink` exposes it, and the
`OnEvent` closure feeds that sink.

### AgentPool and runner-control meta-tools (#1166, PR 1169)

`agent.AgentPool` runs named personas in the background on a `core.DetachForBackground`
goroutine and returns a handle. `NewSpawnSource` exposes `spawn_agent` / `await_agent` /
`cancel_agent` / `list_agents`. `AsyncAgentSource` injects results automatically. **The pool adds
pull-based `await_agent`.**

The issue first sketched the pool as host-layer, but it is agent-layer. Background runs need the
unexported `withAgentDepth` / `withAgentScope` / `DetachForBackground` plumbing, so the pool
belongs beside `AgentSource`. The host is thin wiring (`Config.RunnerControl`), and the Runner is
unchanged. In the pool, an agent may know and target its children. A's child, by contrast, is
non-referential. `transfer_to` still belongs to Team and is not duplicated here.

### Mixing models

Each `Runner` / `AgentSource` / `TeamMember` has its own Provider. A heavy supervisor can
delegate to cheap sub-agents with no special machinery. Within one conversation:
`ConnectionRegistry` plus `providerSwitch` (`/provider`), `FailoverProvider` (primary/backup),
and the deferred routing policy (#991).

### Composition axes (design discussion → #1157)

"Who responds to the user" splits into three orthogonal seams, and none is coupled to the agent.
The **scheduler** picks which agent acts next over shared context. Team is the scheduler, and the
user-interaction framing is its co-located projection. The **surface** is the channel or mode
(Observer/HostEvent). The **interaction mediator** orders, batches, rewrites and routes
user-facing asks, and would generalize the `ElicitationCoordinator` FIFO. Team's lasting value is
scheduling who acts next, not owning the user. In the distributed limit it splits into the actor
form (inject and wake).

---

## Providers

### A constraint on the answer, and what the turn-end hooks judge (#201)

`ResponseGuided` puts a grammar (or a regex, or a JSON schema) on the turn's answer. It can't go
on a step that offers tools. On llama.cpp a grammar covers the whole completion, and tool calling
is a grammar of its own. So a step without tools sends the constraint directly. After a step with
tools, the Runner makes one more call with no tools and the constraint. It sends the conversation
as it stood before the free answer. We left the draft out of that call's context on purpose. With
the draft in, the last message is the assistant's own. A chat template reads that as a prefix to
continue, and getting around it needs a made-up user message. The model answers from
the same context it drafted from anyway.

The guided text replaces the draft in the record, rather than sitting beside it. Some APIs refuse
two assistant messages in a row.

`TurnEnd` hooks used to run before `ResponseSchema`'s finalizing call. That meant a hook could
accept prose whose JSON then failed the schema, and the only sign was `StructuredError` after the
turn was over. Both constraints now finalize first and the hooks judge the result, with the free
answer on `TurnEndInfo.Draft`. The downside is that a refused answer has already spent a
finalizing call. Nothing in the repo set `TurnEnd` together with a schema when this changed, so
only outside callers were affected.

### Routing one step to another provider (#185)

A request-scoped `StepStage` picks the provider for one call by setting `StepView.Provider`, and
the next step starts back on `RunnerConfig.Provider`. We kept it to request scope, as with the
tool set. A routing choice is about one call. If a turn stage could make it sticky, you couldn't
tell who answered what. The host's `stepRoutes` is that stage, driven by step ranges. If no route
covers a step, `/provider` still decides it. Richer rules (last call was a tool result, tokens so
far) are easy as custom stages. We stopped short of putting them in config, because a config rule
language starts to look like the workflow engine A8 rules out.

A few things fell out of it that are easy to trip over:

- The structured or guided finalize always uses `RunnerConfig.Provider`, and its tokens go under
  `""` in `TurnResult.UsageByModel`. With a host route configured, unrouted steps are named after
  the active connection, so the same provider can show up under two keys. The totals still add up.
- `EventStep` used to fire only for steps a stage named, so surfaces got no event per step for
  nothing. That changed in #262. A Terminal-Bench trial killed at its time limit never reached
  `turn-done`, so nothing outside the Runner knew what its finished calls had spent. Now every
  step emits one, with `Model` empty when no stage named it. The TUI, the notebook and the web
  timeline skip or shorten an unnamed one, so the terminal looks as it did. The runner tests pin
  exact event sequences (`TestRunnerSingleStepText` and four more), and they now include the
  step events.
- Switching makes the next call cold. The prompt cache misses on the new provider. On the
  way back the Responses cursor (#157) misses too. The history now holds a step its server never
  saw, so it resends in full (`TestRoutedStepMakesTheNextResponsesCallResendInFull`).
  A few long ranges cost less than alternating.
- A routed step gets that connection's capabilities. Only the guided constraint is re-checked.
  Vision and tools are whatever the routed model supports.

### Generation parameters: how they reach a turn (#1239)

`GenerationParams` (`agent/provider.go`) carries `Temperature`, `MaxTokens`, `ToolChoice`, `Seed`
and `Penalties`.
Defaults go on `RunnerConfig.Generation`, and `TurnRequest.Generation` overrides per turn, **field
by field, where a zero field inherits**. The type documents the corollary. A turn cannot un-set a
config default back to the provider's own. It can only set a different value.

`ResponseSchema` is deliberately on `RunnerConfig` and not in the struct. It selects a different
*turn shape* (a finalizing `Generate`). The other fields tune calls the turn already makes.

**The trap in the finalizing call.** `finalizeStructured` offers no tools. If it kept the turn's
`ToolChoice`, it would ask the provider to force a call with no tool to call, and OpenAI rejects
that. The step loop also uses `applyTo`, so the finalize call site drops `ToolChoice`, not
`applyTo`. `TestGenerationToolChoiceDroppedOnFinalize` pins it.

**How this was ever missing.** Before #1239 the step loop built `ProviderRequest{Instructions,
Messages, Tools}` and nothing else. The seam defines four generation parameters, and a turn could
reach none of three. Non-test code set `Temperature` and `ToolChoice` nowhere, and set `MaxTokens`
only as a `failover.go` health probe. Both providers had always rendered all three onto the wire
correctly. The gap was purely loop-side. Anyone checking a provider saw every parameter rendered,
and nothing looked missing. The roadmap's §7 scorecard credited `ToolChoice` as
shipped. #1056 was then sized against that scorecard, on primitives a turn could not actually drive.

**Penalties merge one level deeper (#276).** `SamplingPenalties` is a pointer struct, and its
fields merge one by one. A turn that sets only `Presence` keeps the config's `Repeat`. A merge
that replaced the whole struct would drop the config's repeat penalty with no error.

**Ollama's /v1 reads two of the four penalty kinds.** On Ollama 0.40.13 at temperature 0,
`frequency_penalty` and `presence_penalty` each change the reply. `repeat_penalty` and
`dry_multiplier` leave it unchanged, and the server returns 200. A dropped penalty looks like a
model that ignores it, so the preset refuses those two kinds with `ErrUnsupportedParam`.
`TestOllamaServerReadsFrequencyAndPresenceOnly` pins both halves. llama-server reads all four.
Current builds answer 400 to `repeat_last_n: -1`, though older docs read it as the whole context.

**Unsupported parameters are forwarded, not screened.** A provider sends what it is given. If a
model rejects a parameter, the request fails rather than ignoring it. Current Anthropic models 400
on sampling params. That is deliberate. `ProviderError{StatusCode, Body}` already holds the
vendor's message verbatim (up to 2048 bytes), and the message names the offending field. A
capability layer would duplicate diagnostics that already exist. Screening by model would need a
per-model table, and this project has declined one once already. For the same reason, agentchat
takes `--context-window` rather than inferring one. Revisit only if a *response*-side signal such as
logprobs (#1053) needs capability negotiation. That would degrade to best-effort rather than
failing.

### Tool-name sanitization (PR 1129, `agent/toolname.go`)

OpenAI and Anthropic both constrain a function/tool name to `^[a-zA-Z0-9_-]{1,64}$`. A connected
MCP server can expose a name that violates it, with a dot like `chat.message`, a slash, spaces, or
over 64 chars. Both providers used to send `td.Name` raw. The request 400'd and **aborted the
whole turn**.

`sanitizeToolName` maps invalid runs to `_`, empty to `tool`, and caps at 64.
`toolNameMaps(req.Tools)` builds a deterministic, collision-resolved real↔safe bijection. The
request sends the safe name (tools list, assistant-history `tool_calls`, forced `tool_choice`)
and the response reverses it via `realToolName` so the Runner still dispatches correctly.

**The map is a pure function of `req.Tools`.** The request builder and the response parser each
derive it on their own, so no state is threaded. The streaming structs are the exception. They
outlive the request, so they keep a `nameMap` field. **A valid name maps to itself**, so
the wire shape is unchanged for the common case and existing wire-shape tests were untouched.

### Shared SSE Recv loop (PR 1131, `agent/sse_stream.go`)

The two providers' `Recv()` bodies were byte-identical except for the payload decode. Both had the
subtle handling for a partial event that arrives with EOF. Two copies risked drifting apart. A
shared `sseStream` now owns the loop, parameterized by `decode func(payload string) (deltas []Delta,
done bool, err error)`. Each provider embeds `sseStream` and supplies its own decode. OpenAI handles
the `[DONE]` sentinel and keeps the tool-call `started` index map. Anthropic returns done on
`message_stop` and err on an `error` event.

`decode` returns `[]Delta` rather than appending to a shared queue, so the loop is the sole queue
owner. Per-stream state (the `started` map, `nameMap`, `inputTokens`) is on the concrete
stream, and the decode method captures it. The providers genuinely diverge in `buildBody`, the
chunk decode, and the `Generate` response parse, so those three remain per-provider.

### Reasoning / "thinking" display — three distinct paths

1. **Local reasoning models** (deepseek-r1, qwq via LM Studio/Ollama) emit reasoning inline as
   `<think>…</think>` in the text stream. `ConnectionConfig.ThinkingHint{OpenTag, CloseTag}` plus
   `agent.NewThinkingProvider` (PR 1064) wrap the provider to re-tag that text as
   `DeltaReasoning`. It handles a tag split across deltas. An empty openTag means the stream
   starts in reasoning, and an empty closeTag is inert.
2. **Cloud reasoning APIs** (DeepSeek `deepseek-reasoner`, OpenRouter) return reasoning in a
   `reasoning_content` / `reasoning` delta field. `OpenAIProvider` **already parses** that into
   `DeltaReasoning`. So `{baseUrl:"https://api.deepseek.com", model:"deepseek-reasoner"}` shows
   thinking with no hint configured.
3. **Native Anthropic** parses `thinking_delta` into `DeltaReasoning` but does not enable it on
   the request side.

OpenAI chat models return no reasoning at all, because chat-completions does not surface
o-series reasoning. The Runner's `consumeStream` turns `DeltaReasoning` into thinking events, and
`render.go` streams the reasoning text dimmed under `· thinking:`.

### Anthropic caching and extended thinking: dropped (#953, constraint A9)

Both are **loop-invisible** provider optimizations. The Runner gets the same `Delta` stream with
or without them. Caching is just cheaper, and thinking is `DeltaReasoning`, which we already parse
and render. So they change nothing above the Provider seam.

The issue's own premise went stale, and that is worth remembering.
`thinking:{type:enabled,budget_tokens:N}` now **400s** on current models. The current shape is
`{type:adaptive}` plus `output_config.effort`, and **temperature must be omitted, not "set to
1"**, because sampling params 400 on those models. Full interleaved-thinking-with-tools would
also thread Anthropic *signed thinking blocks* through the neutral `agent.Message` and the
Runner. That violates A2.

The rule (A9) is to get deep provider features by **wrapping the vendor's official SDK behind the
seam**. Growing the no-SDK provider would mean chasing API drift forever. We keep loop-*visible*
capabilities (logprob→routing, grammar→structured output) and expose them as optional
capabilities on the seam.

### Responses API: reasoning continuity without carrying the reasoning (#157)

On a reasoning model, the Chat Completions wire drops the model's reasoning between tool steps,
because there is nowhere to put it. The Responses API offers two ways to keep it: replay the
encrypted reasoning items yourself (`include: ["reasoning.encrypted_content"]`), or let the server
store each response and chain the next request with `previous_response_id`. The first would mean
carrying an opaque blob through `agent.Message`, and A2 rules that out. `ResponsesProvider` takes the
second, so the only state is a response id, and it never leaves the provider.

The provider is stateless per request, so it can't know which conversation a request continues.
It remembers each response it returned (`responses_cursor.go`). The key is a hash of the history
it answered plus the assistant message the Runner will append for it. A request chains only when
everything before its last assistant message hashes to a remembered id. Then it sends just the
messages after that one. Compaction, an edited or forked history, a resume from RunStore, a
provider switch and an evicted entry all produce a miss, and a miss sends full history. So the
loop can't observe the cursor. A9 keeps loop-invisible optimizations off the seam, so
the cursor isn't a capability there.

These bit us, or nearly did:

- The hash has to leave out `Timestamp`. RunStore stamps messages on append, so a key that
  included it would miss on every step.
- `instructions` and `tools` don't carry over from the previous response, so every request
  resends them. Only the input items are chained.
- Transient per-turn context (recall, summaries) is in the request but not in history. If a turn's
  injected context changed, the turn misses and resends full history. That is correct, since the
  stored response holds the old context. It also means `OnFullHistory` can fire once per turn when
  memory recall is on.
- A stored response can expire. The server answers with a 400 or 404 naming
  `previous_response_id`. The provider drops the entry and resends with full history once.

It's a no-SDK provider like `OpenAIProvider`, against the rule above. The wire is small, and
wrapping `openai-go` would add a third direct require to the root module (A10). This provider
needs text, function calls and reasoning summaries from Responses. If it needs much more, the SDK
wrapper should go in a satellite module.

---

## Host wiring

Reusable host application core in `host/`: config loading (providers, servers, policies,
skills), meta-tools, App/REPL wiring. It is surface-agnostic, and a CLI and a web chat both build
on it.

### The Extension contract had no lifecycle hook (#1250 / #1267, PR 1271)

`Extension` shipped five seams and all five are *contributions*: tools, middleware, prompt
sections, commands, context stages. None is a lifecycle hook. The first real extension
(`ext/checkpoint`) needed to know a turn had begun. The only workaround was a `ContextStage` that
returns its input unchanged and exists for its side effect. A producer that produces nothing is a
hook in disguise.

So `Extension` gained `TurnStart`. **Both** turn entry points call it, `RunTurn` in `app.go` and
`runProactiveTurn` in `events.go`. A trigger firing runs the model over history exactly as a user
turn does. An extension that saw only one entry point would scope its state to the wrong thing. It
runs before history is touched, so a failure aborts cleanly. `BaseExtension` no-ops it.

`App` now also keeps its extensions. They used to be only on `appOptions`, and were dropped after
construction.

#1252's non-goals already state the general lesson. A feature that needs a change to how turns run
has found a bug in the seam, not a reason to work around it. The first real extension found one on
first use. So build a real consumer before the 1.0 freeze, not after.

### ContextStage is per-turn; the loop it serves is per-step (#1301)

The prediction above held. Building the first `ContextStage` consumer (`ext/lsp`) found four
more gaps, and the first one changed the design before any code was written.

**Stages run once per turn, at `RunTurn`. A coding agent's edit-check-fix cycle runs across the
tool-call steps inside one turn.** Imagine a model edits at step 3 and keeps working to
step 12. It gets nothing from a stage for nine steps. Inside a turn, the only injection point is a
tool result. So diagnostics ship on two paths rather than one. What history does to a claim
decides which path it goes on:

- A tool result is permanent. That suits "this edit introduced these errors". The claim is still
  true later as a record of the past, and a tool result is the only thing that reaches the model
  mid-turn.
- A transient stage is per-turn. That suits "the file currently has these errors". The claim stops
  being true the moment one is fixed. In history it would pile up one stale block per edit.

In general, **if a context producer's value decays within a turn, it cannot be a
`ContextStage` alone.** Memory recall does not have this problem, because a retrieved memory does
not change. Anything reflecting mutable external state does.

The other three, all recorded on #1240:

1. **No shutdown seam.** `TurnStart` had no counterpart, and `App.Close` never touched extensions.
   Fixed here by adding `Extension.Close`, called in reverse registration order, exactly once even
   if `App.Close` is called twice. Registration order is dependency order, and
   `surfaces.WorkspaceExtensions` relies on that, so close runs in reverse. This was a **breaking
   interface change** for any implementor not embedding `BaseExtension`. That is a reason to do it
   before the freeze.
2. **An extension cannot observe.** `ToolMiddleware`'s doc says a middleware that merely observes
   should be an event subscriber. But `App.Subscribe` is a method on `App`. An `Extension` is
   constructed before the `App` and never receives it. So an extension that wants to watch has no
   sanctioned route. lsp sidesteps it because its middleware genuinely changes the result. The gap
   is still there for the next one.
3. **`weaveBeforeUser` is host-private.** Some out-of-tree stages want the "closest to the user
   message is most salient" placement, and each one re-implements it. lsp does. It is four lines,
   so the duplication is cheap, and exporting it would widen the surface being frozen. But with a
   second copy, the rule now exists in two places.

### Anthropic through the SDK: what the API refuses now (#274)

Imagine chakra on Claude Sonnet 5.5, eight steps into a turn. The no-SDK provider resends the whole conversation at full input price on every step, because A9 keeps caching out of it. `provider/anthropic` wraps Anthropic's Go SDK and sets the top-level `cache_control` on every request. On the live coding-harness exercise, 64% of the input tokens came from the cache.

The provider takes a caller's `*anthropic.Client` and builds none of its own. So `ANTHROPIC_API_KEY`, an `ant auth login` profile, retries, Vertex and Bedrock are all the SDK's business. The binaries reach it through `host.ConnectionTypes`, which overrides one connection Type and leaves the rest to the host. `WithProviderBuilder` replaces everything, and it would drop the Responses full-history warning.

Running the exercise on Claude turned up three refusals the no-SDK path had hidden.
- A forced `tool_choice` answers 400 on Sonnet 5.5 and Opus 5.5. The no-SDK provider forces a synthetic tool for a `ResponseSchema`, so its structured finalize fails there. The SDK provider sends `output_config.format` instead.
- The finalize sends the history ending on the model's free answer. Current models take no prefill and answer 400. The provider adds one user turn asking for that answer as JSON.
- Structured outputs take a subset of JSON Schema. ext/git's review schema has `minimum`, and the API refused it. The provider drops the unsupported keywords and closes every object. The Runner still validates against the full schema and retries, the same split the Python and TypeScript SDKs make.

The SDK refuses a non-streaming call whose `max_tokens` could run past ten minutes, so `Generate` streams and folds the events. It also sends the key as `Authorization: Bearer`, where the no-SDK provider sends `x-api-key`.

### The stuck detector reads the turn's record, not the view (#278)

Imagine jaala's translator on a 14B model. It writes `try⏎<draft>` as a plain reply and makes no
tool call. A TurnEnd hook runs the draft and continues with "matched nothing" as a user message.
The model sends the same draft six times (panyam/jaala#512, x05).

A detector keyed on tool calls never sees this. So `NewStuckDetector` splits `StepInfo.Turn` into
one `StepRecord` per step, and the host's key reads the reply and the continuation. The Runner
appends a continuation to the working messages too, so `view.Messages` holds it as well. But the
view also holds earlier turns, and a compactor can rewrite it. `StepInfo.Turn` holds only this
turn, and compaction never shrinks it.

The detector keeps no state. It looks only at the steps at the end of the turn, so a pattern stops
firing once the model does something new. While the pattern holds, it fires before every call.

`Message` has no error flag, so `DefaultStuckKey` reads the prefixes the Runner puts on a failed
call's text (`toolErrorPrefix` and the two beside it in `runner.go`).

### One server is not the protocol (#1303)

`ext/lsp` shipped after running against gopls and nothing else. We then probed the four other
servers installed on a dev machine, through the package's own client. The diagnostics contract
turned out to be gopls-shaped, and silently wrong for the others.

**`refresh` took the first publication after a change as the answer.** gopls,
typescript-language-server and pyright publish once with what they found, so that works for them.
rust-analyzer publishes an **empty set immediately**, then its real diagnostics about 2.4s later,
once cargo has run. So an edit broke the build, and the model was told "no problems reported". A
false all-clear defeats the entire point of the post-write report.

**And rust-analyzer publishes nothing at all for a `didChange`.** Its cargo check runs on save, and
we never sent one. Opening the document happened to trigger a check, so the first edit to a file
was checked. Every edit after that went unchecked. Writes now send `didChange` and `didSave`. That
is honest, because everything here reads the file from disk after writing it.

The fix for the provisional publication is to wait rather than take the first. We tried two wrong
versions of the wait before the one that shipped:

1. **A fixed quiet period, per server, from a quirk table.** Correct, but it added three seconds
   to every write on that server whether or not anything was wrong.
2. **Wait for the server's work-done progress to finish.** The principled version, and wrong twice
   over. A server reports progress for everything it does. So rust-analyzer's startup indexing
   looks the same as its cargo check. We read "some work finished" as "your recheck finished", and
   returned an empty set as the answer. Next we counted progress *begins* after our change instead
   of *ends*. That narrowed the bug but did not fix it, because startup work overlaps the first
   write. Then we waited for the server to fall quiet. rust-analyzer is never quiet, so every
   write ran to the full eight-second timeout.
3. **Shipped.** An empty result waits a quiet period. The wait gets a *bounded* extension while
   the server reports itself busy. A non-empty result returns immediately.

The two cases are not symmetric, and the shipped version uses that to keep the cost acceptable.
"Here are three errors" is actionable and complete enough. More can only be added, and the next
turn's stage delivers the full set. "Nothing is wrong" is ambiguous, because "I have not looked
yet" looks the same. So only the empty case waits. Measured on a warm
rust-analyzer: a broken edit reports in ~130ms, a clean one in ~1.9s, and servers that publish once
and report no busy work cost ~250ms either way.

The quirk table is gone, and `ServerSpec.SettleDelay` remains as the escape hatch.

Three things worth carrying:

- **`DefaultDiagnosticsTimeout` had to go from 3s to 8s**, and every wrong version above passed its
  unit tests. The stub does exactly what the test tells it to. Only the live probe showed the
  8s-per-write regression and the empty-set-as-answer one. A test that re-checks a file it just
  opened also hides the didSave problem entirely. So a live test now warms the server first and
  then does real write cycles.
- **"Unchanged content means what we hold is current" is only half true.** If the server already has
  the bytes, skipping the re-sync is right, because clangd does not re-publish for an identical
  `didChange`. But right after a `didOpen` the server has the content and has said nothing. The
  first cut of that optimisation brought back the same false all-clear from the other direction.
  We wrote the multi-language live test, and it caught that within a minute. Both halves are needed:
  unchanged **and** already answered.
- **positionEncoding negotiation is not dead.** #1301 recorded that gopls declines it. That
  suggested utf-16 is simply what one gets. rust-analyzer and clangd both answer `utf-8`, so the
  utf-8 branch in `byteColumn` is exercised rather than defensive.

The `SymbolInformation[]` flat shape never appeared. All five servers returned hierarchical
`DocumentSymbol` with a real `selectionRange`. The flat shape is still spec-legal. A flat array
decodes into a zero `selectionRange` and would resolve every symbol to line 0. So `TestServers`
asserts a non-zero range rather than trusting it.

The eval PRs keep teaching the same lesson. If a suite only ever drives one implementation, it
proves only that the code agrees with that implementation. With `TestServers`, the next
server-shaped assumption fails a test run instead of merging as a bug.

### An RPC to a subprocess needs its own deadline (#1308)

The lsp suite hung once in CI and hit the 600-second default `go test` timeout. Every passing run
took 44.5s. We chased it and found a production bug, not a test bug.

`conn.call` waits on three things: the reply, the caller's context, and the read loop ending. The
read loop ends when a server crashes. It does not end when a server is *wedged*. Imagine a server
reads a request, never answers it, and keeps its stdout open. The read loop blocks in `readFrame`
with nothing to fail on, so `done` never closes. Only the caller's context is left.
`source.navigate` passes the Runner's context straight down, and that context frequently has no
deadline. One
misbehaving language server could hold a turn open forever with nothing logged.

Every call is now bounded, and the caller's deadline wins when it is shorter. The bound is a field
on the `conn` rather than a package var, per constraint C3.

In general, **a wait that ends only when the peer dies is not bounded.** Process death is the easy
failure to handle, and the rare one. The common failure is a peer that is still there and no longer
answering.

The CI step also gained an explicit `-timeout 180s`, so the next hang produces a goroutine dump
naming the blocked call instead of a bare line at 600s. That is diagnosis rather than a fix. It is
useful whether or not this was the cause. We never reproduced the hang locally, across repeated
`-race` runs and six concurrent ones. So this closes the class, not a confirmed instance.

### A workspace is a set of roots, not one (#1314)

Every workspace extension took a single `Root`, and that does not match how the tool is used. Few
coding sessions remain inside one repository. The ordinary task changes an API in one repo and
fixes its callers in another, often in another language. Imagine an agent confined to the first
repo. It edits the API, reports success, and never sees what it broke. The caller is outside both
its confinement and its language server.

`files` now takes `Roots []string`. The first root is primary, so a relative path still resolves,
and a single-root workspace behaves exactly as before.

Three things that fell out of it:

- **Results became absolute paths.** Two repositories can both hold `src/main.go`, so a bare
  relative path could name either. The alternative was a new qualifier syntax (`repo:path`). That
  changes every tool's path argument and gives the model a new thing to spell wrong. Absolute
  in, absolute out, no grammar.
- **A common parent is not a substitute for listing the roots.** It silently widens confinement to
  everything else under that parent, and `os.Root` is there to deny exactly that.
  `TestParentOfTheRootsIsNotImplicitlyIncluded` pins it.
- **The listing cap is global, not per root.** A per-root cap would quietly grow every time
  someone added a repository. A listing would get larger without anyone changing a limit.

The confinement mechanism did not weaken. Each root still has one `os.Root` handle, and the handle
resolves at open time. A path now has to resolve inside one of them rather than inside the one. The
symlink-escape test still passes unchanged. We watched that assertion most closely.

**`lsp` followed, and measuring changed its design.** #1314 assumed a language server is rooted. A
session over N repositories would then need an instance per root per language. That means N
servers, N indexes and N cold starts.

We probed instead. Take a file in a `workspaceFolders` entry that is not the `rootUri`. gopls,
rust-analyzer and typescript-language-server **all** answer correctly for it, for both diagnostics
and documentSymbol. So one instance per language covers every root. The change shrank to sending
the folder array and keying state on absolute paths.

That is the second time in this epic that measuring beat the written-down assumption, and both times
the assumption was mine. Worth the ten minutes every time.

**Cross-language references are still out of reach**, and no amount of this fixes them. A Go server
cannot tell you that a TypeScript file calls the endpoint you renamed. That boundary belongs to the
language server, so a cross-language rename is `search_files` and judgement.

### The repo map could not be its own module (#1311)

#1311 specified a new `ext/repomap` drawing symbols from `ext/lsp`. Constraint C4 forbids that
import, and `make check-ext-isolation` enforces it in CI.

The constraint turned out to point at a real cost, not a bureaucratic one. Two modules means two
`pool`s. Two pools means **two language servers over the same tree**. Each indexes it, and both
answer questions about the same files. The map and the diagnostics feature want to share one server.
So the map is a second contribution from the `ext/lsp` extension, gated by `Config.RepoMap`.

Remember this when C4 blocks a split. Find what the two modules would have shared, and check
whether it is expensive. If it is, C4 is describing the design rather than obstructing it.

Two smaller decisions:

- **Edges are lexical, definitions are not.** Symbols come from `documentSymbol`, which is accurate
  and language-agnostic. Edges come from whole-identifier text matching. A `references` request per
  symbol would take thousands of round trips, and only to order a list nobody reads past the top.
  The graph drops names shorter than four characters. `Get`, `New` and `id` collide across
  languages and would wire nearly every file to nearly every other.
- **Indexed once, in the background.** A `PromptSection` is rebuilt every turn, and indexing takes a
  round trip per file. Built inline, it would put a tree scan in front of every turn. The section is
  empty until the index finishes, and the prompt builder drops empty sections. So an early turn
  goes without the map rather than waiting for it.

The mutation run caught something worth recording. `TestRankIgnoresSubstringMatches` tests
`mentions` directly. We replaced the *call site* in `rank` with `strings.Contains`, and the test
still passed. A unit test of a helper does not test its caller. The end-to-end test pins the
behaviour.

### Running a command, and what an allowlist actually constrains (#1312)

`ext/exec` was the first thing in the tree to run a command. We found two things building it, and
they are worth more than the module.

**The approval gate decided the tool shape.** The obvious design is one `run_command` tool. Its
`name` parameter would be an enum over the operator's allowlist. That contains the model exactly as
well as one tool per command does. But it cannot express what an operator wants. `toolHints`
resolves `readOnlyHint` / `destructiveHint` per `ToolDef`. In one tool, every command shares one
`destructiveHint`. So `ModeReversibleAuto` prompts for `go build` and `terraform apply` together, or
for neither. An extension cannot get per-command gating back. Extension middleware runs *before*
the host's permission gate and cannot influence it, and `ApprovalRenderer` only changes the
wording. So the allowlist emits one `ToolDef` per command. The tool list then grows linearly, and
that is #1323.

**The allowlist names which program starts, not what it does.** `make test` runs whatever the
Makefile says, `go test` compiles and runs the repo's test code, `npm run build` runs its scripts.
For any real build command the allowlist states intent, and the sandbox does the containing. We
first framed this around `ext/files`, which can rewrite the Makefile first. That makes it read as a
quirk of running two extensions together. It holds without `ext/files`. Running a build in a
repository runs that repository's code. That is true of a cloned dependency, of a PR branch under
review, and of a file an agent just edited.

**Only a tagged live test checks a sandbox profile, and its first run found two bugs.** The unit
suite checks profile *generation* against a golden, and that half can be tested anywhere. The
`exec_live` suite runs the real `sandbox-exec`. It caught two things the golden could not:

- Profile paths were not symlink-resolved. Seatbelt matches the resolved path, so every rule naming
  `/tmp` or `$TMPDIR` matched nothing on darwin. A read denial was in the profile and looked
  enforced, and the read still went through. That is worse than no denial.
- A real `go build` failed on its build cache before compiling anything, because the write paths
  were spelled unresolved too.

CI is Linux and this backend is darwin-only, so **nothing in CI exercises a real sandbox.** Run
`go test -tags exec_live ./...` before believing a profile change.

**Seatbelt takes the last matching rule.** That reads backwards. The denials come *after* the
blanket allow they carve into, and a denial placed first is present and inert. `sandbox-exec` is
also deprecated, and its profile language is undocumented. The golden pins what we generate.
Nothing pins that Apple keeps honoring it.

**The epic got two things wrong.** First, sandboxed execution does not go in a `ToolSource`
wrapper. Most tools here are in-process Go functions or MCP calls, with no subprocess to confine.
Confining those means confining the agent process, and that is a deployment decision. Second, the
sandbox is not the primary defense. The approval gate is. C6 gained a second sanctioned exception,
for extension-owned subprocesses (`gopls`, and `exec`'s own commands). Both come from operator
config, and a tool argument cannot reach them.

**This was already true, and nobody had connected it.** The Runner re-lists tools **every step**,
not once per turn. `RunnerConfig.Selector` narrows the offered set per step, as a pure function of
history. So a tool source whose set varies by context is already legal and needs nothing new. That
is the cheapest answer to #1323. `FilterSource` is the static version. It filters *calling* as well
as listing, so it is a boundary rather than a presentation hint.

### Sub-agent personas

`Config.SubAgents []SubAgentConfig{Name, Description, Instructions, Allow, MaxDepth}` builds
specialist personas. Each is an `agent.AgentSource` over a child Runner with its own instructions.
The child shares the main provider, and sees the same server tools through a `FilterSource`.

`app.serverTools` is a **server-only snapshot** built in `NewApp`, so a persona never sees the
meta-tools or its siblings and cannot recurse. Sub-agent, fan-out and team-member construction
all use one builder, `personaRunnerConfig` (`agent/host/subagents.go`). So all three see only
serverTools, and are therefore memory-free under A7.

**A latent bug was fixed with it.** `app.serverTools` used to be built only when `SubAgents`
was set, so a fan-out-only or team-only config nil-dereferenced. It is now built when any of
SubAgents / FanOut / Team is present.

**Team mode is a distinct top-level mode.** The main agent's memory, sub-agents and fan-out each
attach to one agent. So `validateTeamExclusive` errors if any of them is set with Team.
Per-member integration is deferred. `a.activeTeamAgent` keeps the active member across user turns.
Control transfer is sticky, and the next turn is not re-routed.

### SystemPromptBuilder (#1091, PR 1132, `agent/host/prompt.go`)

The hard-coded `buildInstructions` became a `SystemPromptBuilder`. It holds an ordered
`[]PromptSection`, and `Build` joins them per turn. It is wired as `RunnerConfig.InstructionsFunc`,
so skills from late-connecting servers still appear live. `PromptSection` is a one-method interface
and `PromptSectionFunc` adapts a plain func, http.HandlerFunc-style.

`NewApp` builds the default with `app.defaultPromptBuilder()`. That is the base `cfg.Instructions`
section plus `skillsSection`, and **the test uses the same function, so the two cannot drift**.
Then `NewApp` applies any `WithSystemPromptBuilder` mutator (reorder, `Prepend` a domain guide,
replace `Sections`). Behavior did not change. The common non-empty-base output is byte-identical,
and an empty base no longer emits leading blank lines.

`InstructionsFunc` remains the low-level Runner hook; the builder is host-layer composition on
top. Richer strategies (profile/domain guides, memory-summary-as-a-section) are additive
`PromptSection`s on this seam. Nothing is missing for them, so #1091 closed rather than remaining
open as a gap.

### Skills consumption hardening (#1182/#1183, PR 1185, `agent/host/skills.go`)

Two host MUSTs from SEP-2640's Security Implications, host-internal with no `ext/skills` change.
The origin label is the **host-assigned `serverID`, never `serverInfo.name`**.

- **Origin tagging**: every MCP-served skill body carries its origin before it enters context.
  `load_skill` results get an untrusted-origin banner (`wrapSkillOrigin`); eager and catalog
  injected blocks get a per-server origin header (`originHeader` / `withOriginHeader`). The header
  frames them as untrusted server data rather than higher-authority instruction.
- **Per-origin name resolution**: `load_skill(name, server?)` no longer takes the first match
  across all catalogs. That let one origin silently shadow another. `resolveCatalogSkill` resolves
  within a per-origin namespace. If more than one origin serves a bare name, it returns a
  disambiguation prompt rather than silently picking one. `HostSessionWarn` reports collisions
  (`detectSkillCollisionsLocked`).

### Two-tier skills loading (#910, PR 1073)

Eager full-body injection bloats the system prompt at scale (50 skills × ~100 tok, resent every
call), so loading is progressive. **The catalog decision is per-server, not per-skill.**
`ServerConfig.SkillsMode` (`"eager"` / `"catalog"`) marks a whole skills server. `""` means
catalog. SEP-2640's 08-21 revision made fetching ahead of need a MUST NOT. So chakra#50 removed
the old auto rule (eager below ten skills).

Eager injects `skills.InstructionsBlock` (full bodies). Catalog injects `skills.CatalogBlock`
(one `- name: desc` line per entry, roughly a tenth of the tokens) and collects entries.

**One** `load_skill(name)` FuncSource tool covers all catalog-mode servers. It finds the entry,
calls `ReadAndVerify(url, digest)`, and returns the body as a **tool result**. The body goes into
tool history and is not re-injected into the prompt, because changing the prompt breaks the prompt
cache. Laziness never
bypasses digest verification. An unknown name is app state, not an error.

We chose per-server mode over a connect-time `SkillsPolicy` hook, which would be embedder-first.
The two can coexist. Byte-threshold, body caching, and the policy hook are deferred.

### Server-advertised agents are read through the cache, not the index

`host/server_agents.go` reads every roster and definition through mcpkit's `agentsclient.Cache`,
one per server. Three things about the wiring were not obvious from the cache's own docs.

**`WatchListChanged` does not apply here.** It opens a `subscriptions/listen` stream. That stream
exists only on the 2026-07-28 stateless wire, and chakra's clients use the session wire with a GET
stream. The notification still arrives there, through `client.WithNotificationCallback`. So the
callback is installed when each client is built. The cache it invalidates does not exist until
discovery runs, so the callback looks up the server's handler in `agentWatchers` by id.

**The aggregate memoizes the delegate list, so a roster TTL running out changes nothing by itself.**
`MultiSource` asks `serverAgentSource.Tools` again only after it drops its index. It drops the index
on `notifications/agents/list_changed`, on `agent_not_found` during a delegation, on any server's
`tools/list_changed`, and on a call miss. Imagine a server that neither declares `listChanged` nor
removes agents through `agent_not_found`. It keeps its boot-time delegates until one of those fires.
That is mostly fine, because definitions (`agents/get`) do honor their TTL on every delegation.

**A child is rebuilt only when its definition changes.** Building one per delegation would have
been less code. But every delegation would get an `agents.resolve` span, and that breaks the
existing assertions that a warm delegation is not spanned. So the source keeps the definition it
built each child from, and compares. The first resolution of an agent spans the `agents/get`. A
rebuild after an edit spans only the build. By the time we know the definition changed, the get
has already happened.

### Connections

`ConnectionsConfig` is data, `ConnectionRegistry` is runtime (`agent/host/connections.go`).
The config holds named provider connections in the `llm.json` shape. The registry builds and
caches providers lazily, and holds `SetActive`.

`Type` maps to a base-URL preset. lmstudio / openai / ollama / gemini / openrouter / litellm are
all OpenAI-wire. **`anthropic` is the exception.** `DefaultProviderBuilder` branches on it and
builds the native `AnthropicProvider` (x-api-key, `/v1/messages`). So a config-selected Anthropic
uses the purpose-built provider rather than a compat shim. openrouter and litellm are
router-gateway presets, and litellm defaults to the proxy's local `:4000/v1`.

**The embedder is a config role, like `Active` is for chat.** `ConnectionsConfig.Embedder` names
an embedding connection and `ConnectionConfig.Dim` is its vector width.
`host.BuildEmbedder(conn, tp)` builds an OpenAI-wire `agent.Embedder`. **Both `BuildEmbedder` and
registry validation reject `anthropic`, because it has no embeddings API.** With an
embedding connection set, semantic memory runs against a cloud embedder with just an API key and no
local model.

**Agent roles are a third assignment over the same set.** A server-advertised agent can hint at a
`small`, `medium` or `large` role. `ConnectionsConfig.AgentRoles` maps each role to a connection
name, and `ConnectionConfig.Vision` marks the connections that take images. The host feeds them to
mcpkit's `agentsclient.RoleTable`. The table moves up a size when it has to, and never down. It
holds connection *names* rather than providers, so a role connection is built only when an agent
first resolves to it. The table's default is the empty name, meaning the main provider. So
`/provider` switching still applies to every agent that does not hint elsewhere.

### Commands, events, and observers

`CommandRegistry` plus `App.Dispatch` (`agent/host/commands.go`). Slash commands are a registry
of `Command{Name, Aliases, Help, Run, Complete}`, and each returns a `CmdResult` tagged union.
`App.Dispatch(ctx, line)` routes a `/cmd` line and returns `ErrUnknownCommand` for misses.
`Complete` powers Tab autocompletion. This is host-layer and deliberately not in the CLI.

**`HostEvent` plus `Observer` fan-out, not "UIEvent".** The host emits domain and lifecycle
events (turn done/failed, session changed, skills loaded, task status). OTel or a logger can use
them with no renderer attached, so they are Host events rather than UI events.
`WithObserver(o)` fans one-to-many and suppresses the default renderer.
`NewTerminalRenderer(w)` is one Observer.

**`UIPrompt` was dropped.** A prompt is request/response (an `AskFunc` or elicitation `Confirm`),
not fire-and-forget, so it never belonged in the event stream.

### A process's output belongs to the process, not the call (#214, PR 219)

`exec_command` hands the model a process's output only when the model collects it. A dev server
prints mostly while no tool call is open, and often between turns. So the live view goes over a
host event, `HostProcessOutput`, and not a runner `agent.Event` scoped to a call. The optional
`ProcessReporter` feeds it, and it is keyed by the process id the model was given. agentweb
attaches it to the first tool row whose result names that `session_id`. Anything that arrives
before the result waits until then.

- **The copy is taken at the write, and never moves the model's read position** (`p.read`).
  `TestProcessStreamingLeavesResultsUnchanged` runs the same script with and without a listener;
  making the stream advance `p.read` fails it.
- **A per-process budget beside `MaxEventLogRetention`, not instead of it.** Retention caps the
  whole log by count and evicts the oldest events of any kind. Imagine there is no RunStore and a
  process is chatty. It can push the conversation's own turn events out of a reload.
  `ProcessConfig.StreamBudget` (4 MiB, negative for no limit) stops that at the source. Non-runner
  host events are never written to the RunStore, so none of this is persisted.
- **`done` means the output is all in** (#218). A tty's copier closes `proc.drained`, and
  `proc.wait` holds `done` until then, for at most `drainWait` (2s). The bound is for a background
  child that keeps the terminal open. Before this, `collect` and the stream each slept 20ms. On
  Linux that lost the end of a burst written right at exit, because the master was closed while
  the terminal still held output. A pipe needs no signal, since `cmd.Wait` waits for its copy.
- **A tty process is drawn, not printed.** Its events carry `TTY`, `Cols` and `Rows`, and
  agentweb writes the raw bytes into xterm.js in order. The store keeps a running byte count
  beside the 64 KiB tail, so the view writes only what's new, and a reload replays to the same
  screen. A tty starts at 120x30, since a 0x0 terminal breaks some programs.
- **The user drives a process through `ProcessController`, the reverse of `ProcessReporter`**
  (#249). Resizing and typing come from a surface and skip the approval gate, because the user is
  the one acting. What is typed never enters the model's context. The model sees the effect when
  it next reads the output.

### Session listing and paging

Redis SCAN is unordered, and gorm returns newest-first via a correlated subquery. **Pagination is
done at the store layer.** `App.SessionsPage(ctx, cursor)` threads the cursor. The App remembers
the last `NextCursor`, so `/sessions more` advances host-side and the opaque cursor never reaches
the surface. `App.SearchSessions(query)` is a bounded id-substring walk
(`maxSessionSearchScan`) with no store-side filter, because content search needs an index.

### oauth login (#1116/#907, PR 1120)

agentchat's interactive authorization-code `"oauth"` auth type is in `agent/host/auth.go`.
`authOption` builds an `ext/auth.OAuthTokenSource`. With no `clientIdEnv` it uses DCR, and
otherwise a pinned client. It returns the source as a `loginSource`, tracked in
`App.oauthSources[id]`.

**`App.LoginServer(id)` is `src.Invalidate()` plus `ReconnectServer(id)`.** It drops the cached
token to force a *fresh* auth, and the next connect re-runs the PKCE browser flow. This reuses
`Group.Reconnect` and adds no new connection primitive.

The servers view carries a **host-layer `ServerStatus{client.MemberStatus; CanLogin}`**, so
`CmdResult.Servers` is `[]ServerStatus` rather than `[]client.MemberStatus`. `CanLogin` is a
config fact the client layer does not have. So the host wraps the status rather than pushing auth
awareness down. Scopes are empty by default, so acquisition follows the server's 401
`WWW-Authenticate` challenge.

Retrofitting a token source onto a server that was not configured for oauth at boot is
unsupported. It would need its client rebuilt.

**The `WithBrowserOpener` seam** (#1123, PR 1136) sets how the oauth type opens the authorization
URL. Nil means the platform browser. A test passes its own opener function, which gets the URL
in place of a browser, so the whole login path runs in CI with no Docker and no real browser
(`agent/host/oauth_login_e2e_test.go`). **Gotcha.** The
oneauth test AS accepts an arbitrary public client for PKCE but does **not** serve DCR. So pin
`clientIdEnv` rather than relying on the DCR default. The real-Keycloak variant is #1137.

### OTel metrics (#1023, PR 1141)

These metrics sit beside the SEP-414 spans. They use the existing `core.MeterProvider` seam, so
there is no new seam. `RunnerConfig.MeterProvider` opts in. Nil means `NoopMeterProvider`, which
is branch-free and has zero overhead. `NewRunner` builds the instruments once
(`agent/metrics.go`), and they record at the same points as the spans.

- **Turn end**: `agent.turns` by `agent.finish_reason`, `agent.turn.duration`, `agent.steps`,
  `agent.tokens` by `direction`.
- **Each tool call**: `agent.tool.calls` by `tool` and `status`, `agent.tool.duration` by `tool`.

`status` ∈ {ok, error, tool_error, denied, cancelled, unavailable}. Each outcome path sets it, and
a **single `defer` in `callTool`** emits it, so no path double-counts or misses.

The host passes it to the main Runner and every sub-agent persona through
`host.WithMeterProvider`. The `mcpkit — agent` Grafana dashboard (uid `mcpkit-agent`) charts turn
rate, latency, token throughput, and tool failure ratio off Mimir. OTel's Prometheus export renames
metrics. Dots become underscores, counters gain `_total`, and histograms gain a unit suffix. So
`agent.turn.duration` reads as `agent_turn_duration_seconds_bucket`.

Deferred: a memory-tracks metrics seam (embed/recall/compaction counters) and exemplars.

### Critique gate (#1061)

`NewCritiqueGate` asks a model whether a proposed tool call is acceptable under stated principles,
and denies it if not. Three decisions matter most:

**It is middleware, not a new Runner hook.** The issue proposed a dedicated pre-dispatch gate in the
Runner. There already is one, and `toolmiddleware.go` says so. ToolMiddleware is *the single
interception seam*. That doc exists to prevent a second mechanism at the same point. A critique pass
changes whether a call happens, and deciding that is middleware's job.

**It runs after extensions and before the approval gate.** Like the gate, it must judge the
arguments that will actually execute. So it cannot run outside middleware that rewrites them.
Unlike the gate, it is automated policy. So it runs first, and a call it refuses never reaches the
human. Refusal is `DenyTool`, so the Runner emits `EventToolDenied`, tells the model why, and the
turn continues.

**It fails closed.** `AllowOnError` defaults false, so if a critique cannot complete, the call is
denied. A safety gate that disappears when its provider is down is not a safety gate. A provider
outage is not evidence a call is safe. We made the zero value the safe one on purpose.

The critic sees the proposed call inside the **same untrusted fence Spotlight uses**
(`delimitMark` + a per-call `newMarker`). An attacker controls the arguments. Imagine an injected
instruction reaches the agent through a tool result. The agent can echo it straight back out as an
argument. If the critic got that as prose, the attacker would get a second prompt. The fence
narrows the surface without closing it. That is the honest claim.

**The critic's output is fenced too, and that half is less obvious.** Fencing the input and
leaving the return path open protects nothing. The critic is a model, and it has just read
attacker-controlled arguments. So an attacker can steer it into writing the attacker's text as its
reason. That reason reaches the agent as `tool call not permitted: ...`. A denial arrives in the
*policy layer's voice*. The agent has every reason to trust that voice more than the tool result
the text came from. So an unfenced refusal is an injection channel, and it promotes the payload as
well. The attacker gives up one blocked call to aim a payload at the next one.

`fenceCritiqueReason` mints a **fresh** marker rather than reusing the one `critiquePrompt` used.
The critic saw that one, and if it echoed it, it would close the fence from inside.
`fenceCritiqueReason` also does not reuse `delimitMark`. That sentence names a tool and says the
content was fetched from outside, and neither is true of a verdict written in-process. #1273 is
about a fence whose explanation is false.

Imagine pushing that fence through to the surface as the denial reason. A fence is several lines,
and `host` renders a denial as one truncated line against the call. So the user saw boilerplate,
and the actual reason was cut off. We added `ToolDeniedError.ModelReason` for this. Surfaces show
`Reason`, a legible attributed line. The model is told `ModelReason`. If `ModelReason` is empty,
the two are the same, and that is right for every middleware whose reason is its own prose. The
two audiences had shared one string since the type was written. Nothing noticed until a reason
needed to be untrusted. `TestCritiqueConfigRefusesARealCall` caught it, because it asserts on
rendered output.

We did not build critique of the **final answer**. The issue mentions it. It does not fit
ToolMiddleware, because there is no tool call. It needs a seam that does not exist. #1288 is a
cautionary tale about inventing one speculatively. Whoever has a caller can build it.

We have not audited whether other middleware echo attacker-controlled argument text into their
reasons. The three `DenyTool` sites in `approval.go` are host-authored constants. So critique is
the first denial whose reason is not the host's own words. A third-party middleware could be
another.

---

## Eval

`eval/`: multi-turn `Scenario` plus `RunScenario`, which threads history and a shared
`MemorySource` across turns. **One Runner is reused.** It is stateless over history, and
only the store persists.

`eval/longmemeval` `SmokeScenarios()` are **hand-authored, not the LongMemEval dataset**:
short turns, not ~100k-token histories. The real dataset loader is #1014.

`MemCase.NewCompactor` and `Scenario.NewMemoryStore` are factories, so the harness can grade
different implementations through the same scenarios.

A tool denial emits `EventToolDenied` with `Event.Reason`. The reason goes back to the model as
text, and **the turn continues**. It is deliberately not an error, so `eval.NoError` ignores it and
`eval.NotDenied` is the separate check.

### AgentDojo is a framework, not a dataset (#1060)

#1060 asked for "an adapter that loads AgentDojo-style cases", which assumes a corpus. There is not
one. A user task is a Python class. Its `utility(output, pre_env, post_env)` inspects environment
state before and after the run. An injection task pairs a GOAL with a `security(...)` check. The
environments are stateful Python objects, and their tools mutate them. The scoring **is** code run
against live state, so nothing exports as data.

So `eval/agentdojo` rebuilds the *shape* in Go. `longmemeval`'s `SmokeScenarios` does the same with
LongMemEval's categories, without its corpus. The numbers are ours and not comparable to published
ones, and the package doc says so. #1298 is the bridge that would make them comparable.

That suite has to get three things right, and a single pass/fail would lose all three:

- **The injection goes in tool output, never in the prompt.** The attacker does not control the
  user's request. A payload delivered through instructions measures instruction-following.
- **Two axes, scored independently.** Utility-pass with security-fail is the dangerous cell. The
  agent did the job *and* paid the attacker. Any single verdict reads that as clean success. The
  opposite corner matters too. An agent that refuses everything has perfect security and no use.
- **Scorers read post-run state, not the transcript.** Imagine an agent says it will not pay the
  attacker, then calls `pay`. It has resisted nothing, and only the state shows that.

#1015 deferred the per-dimension rollup to here, and `SuiteReport.Dimensions()` is it. It is a
**method over data `CaseReport` already had**, not new fields. When we deferred, we bet the
existing data would be enough, and it was. Inversion is the caller's job. An attack-success rate
is `1-Rate` of a resistance dimension. If `Rate` meant opposite things depending on the scorer
name, that would be worse than the arithmetic.

**The CI tests do not prove the guardrail works.** A stub provider cannot show that spotlighting
changes a real model's behaviour. `TestGuardrailChangesTheMeasurement` verifies that the
*apparatus* tells defended runs from undefended ones. It would pass even if the guardrail were a
no-op on real models. The
effect itself is a live-run measurement.

### The adapter seam (#1015)

External suites are **data sources**, not harnesses. `Adapter.Load` yields `[]SuiteCase`, and
adding LoCoMo / BFCL / tau-bench must not touch the Runner, the scorers, or `Suite`.

Three things had to change for that. In each case a real suite needed the change, and we did not
anticipate it:

- **Scorers are per-case.** `Suite.Scorers` was one list applied to every case. One list can express
  only properties true of every case ("did not error"). It can never express a case's ground truth,
  and a benchmark needs that. `longmemeval/live_test.go` showed the problem. `Suite` could not take
  it, so it hand-rolled its own copy of `Suite.Run`. That file is now the harness call it should
  always have been.
- **`Scenario.History` seeds without running.** A memory benchmark supplies a long prior
  conversation and asks one question about it. Replaying that as `Turns` would call the model once
  per historical message and grade its own invented replies. History is data the agent is told it
  had, and Turns are turns the agent takes.
- **`SuiteCase.Configure` returns a config rather than mutating one**, so a compaction case's
  `Compactor` cannot leak into the next case. `TestSuiteCaseConfigureIsPerCase` observes the
  handed-in config through `Configure` rather than through `Result`. `Result.Case` has only the
  turn's name and input. An assertion there compares two zero values and passes either way.

**Ungradeable is a failure, not a pass.** `Suite.Run` already failed a case with no scorers. A
subtler version bit immediately. A `Rubric` grades the abstention smoke case, and its only
deterministic scorer is a `MustNot`. *Any* answer satisfies that `MustNot`. The adapter is untagged,
and `live_test.go` never was. So the case was reachable from the default build for the first time,
and it passed against a stub answering "stub answer". So `appendRubric` now yields
`eval.Ungradeable` without the `eval_llm` tag. A case graded only by checks that cannot fail is not
graded.

**Aggregation is single-axis on purpose.** `SuiteReport.Passed/Failed` counts whole cases. That
is right for one question per case. It is wrong for a suite that reports several independent
rates. A security suite wants utility and attack-success counted separately, with one of them
inverted. We deliberately did not design for that yet. Per-scorer verdicts keep their names, so a
per-dimension rollup adds to existing data and needs no re-plumbing. #1060 will actually stress
it, and reshaping costs nothing while the agent track is unreleased.

### Setup, cost, and the official judge (#246, PR 251)

jaala's LongMemEval arm ingests each question's haystack into its own fact store, then answers with
tools over that store. `Configure` couldn't host that, because it sees no instance and returns no
usage. So `SuiteCase.Setup` runs once before the turns and returns tools, instructions, history and
what it spent. `DatasetAdapter.Arm` wires it per instance. The report keeps setup spend apart from
the turns' spend, since a memory layer is judged on both.

- **An unpriced model makes the cost unknown.** `Prices.Cost` reports false when any model that
  spent tokens has no price. A total that left one model out would read as cheaper than the run was.
- **The base provider needs a name.** The Runner counts steps that no stage named under `""` in
  `UsageByModel`, and `agent.Provider` has no name. `Suite.ModelName` relabels them so they can be
  priced.
- **CI never runs `eval_llm`.** So keep a judge's logic untagged and test it against a stub. Only
  the choice to call a model to grade goes behind the tag (`appendOfficial` beside `appendRubric`).
  The default build still never grades with a model.
- **The official prompts are checked against upstream.** `testdata/evaluate_qa.py` is a verbatim
  copy of upstream's `get_anscheck_prompt`, and a test parses its templates and compares them with
  `official.go`. A copied prompt then can't drift without a failing test.
- **`Ungradeable` made one adapter check dead (#243).** A rubric-only case now yields one scorer, so
  "every case yields a scorer without a provider" could no longer fail. The intent moved to
  `TestDeterministicScorers` at the `MemCase` level. A new always-failing scorer can quietly satisfy
  an older count check, so look for counts like that when one is added.

---

## Tracking mcpkit main (PR 56, PR 62)

Every module `replace`s each mcpkit module it uses with a relative path into
`~/newstack/mcpkit/main`, so chakra builds against mcpkit's working tree rather than a tag. The
first consumer was the agents-wg#22 wire change. It merged in mcpkit the same day chakra needed
it.

- **Relative, not absolute.** `go.mod` expands neither `~` nor environment variables, so an
  absolute path would name one home directory and break CI and every other clone. The relative
  path works for any checkout at `<dir>/chakra/<branch>` beside `<dir>/mcpkit/main`. So the repo
  moved to the `chakra/<branch>` layout, and CI recreates it.
- **Not go.work.** A workspace file solves the same problem, but its `use` lines are paths too,
  so each worktree needs its own copy. The replace is cheaper, as long as the layout rule holds.
- **Invisible to importers.** Go ignores `replace` outside the main module. Anyone importing
  chakra gets the required tag. But chakra's code may already use newer API, as the agents port
  did. Tag mcpkit and repoint the requires before tagging chakra.
- **A sibling session's uncommitted edits look the same as mcpkit main moving.** During PR 219,
  another session was releasing from `~/newstack/mcpkit/main`. Its go.mod files were modified, and
  v0.8.16 was not yet tagged. Every module here wanted `go mod tidy`. Leave that checkout alone,
  build with `GOFLAGS=-mod=mod`, and `git checkout` the go.mod and go.sum files before committing.
- **Dependabot PRs against the old pins go stale fast.** PR 55 bumped modules unevenly against
  v0.5.2, and it failed. `check_dep_consistency` caught the split, and `otel/log` moved ahead of
  the `otelslog` bridge. Re-tidying every module against mcpkit main superseded it.

## Testing

- **Run agent tests with `-race`.** The concurrency-shaped tests (`signal_test.go`,
  `agent_pool_test.go`, `interruptible_test.go`) exist to catch races. `-race` is the default in
  `GOTESTFLAGS` rather than a per-module opt-in. Those three tests are in the root module, and the
  root module was the one place CI ran without `-race`. The whole tree costs about 100s under
  `-race`.
- **Host behavioral tests race on a shared sequential StubProvider** when the main turn and a
  background child both pull from it. So host tests assert *wiring*. Agent-layer tests
  check the *behavior*, with isolated per-child providers. `blockingProvider{}` in
  `runner_test.go` is the "child that blocks until cancelled".
- **Don't assert what a subprocess printed inside a short yield.** The `ext/exec` helper process is
  the test binary re-executed. On a loaded CI runner it took over 100ms to print its first line.
  PR 219's first CI run failed on that, in a check the test didn't need. Give a process a second
  to start. Put the line that must arrive later a couple of seconds after that. Assert ordering,
  not what arrived inside the yield.
- **A pty test can pass on macOS and fail on Linux.** A BSD tty drains pending output when the
  last slave fd closes, so a process can't finish exiting until its output is read. Linux keeps
  the output in the terminal after exit. #218's drain test passed on macOS with the bug present,
  and failed in a Linux container. CI is Linux. Run `ext/exec` on Linux before believing a
  pty change (the command is in CLAUDE.md Traps).
- **The Playwright checks share one server** (`make test-web-e2e`, `surfaces/web/internal/e2eserver`).
  The picker check switches chat's connection, and a later check saw it. A check must not assert
  state another check changes. They also run against the committed bundle, so rebuild it with
  `npm run build` after a frontend change.
- `make test` covers every module and `make test-examples` the examples. CI runs those same two
  targets, and overrides `GOTESTFLAGS` only to add `-v`. So the module list is in the `Makefile`
  and nowhere else. **Adding a module is one edit.** Before the split it was three edits, in three
  files. A module missing from the workflow builds fine locally and never runs in CI, so the gap
  reports itself as a pass.

### Reading a mutation run (learned on #1275, #1284)

Red-before-green here means mutating the implementation and naming which tests fail.

**A surviving mutation is a signal to diagnose, not a verdict.** Three quite different things
produce the same "no test failed". All three happened across #1275 and #1284:

- **A real test gap.** The one you want to find.
- **An unreachable mutation.** Reverting `edit_file`'s rename to an unconfined `os.Rename` changed
  nothing, because the earlier `Stat` through the root already refused. That is defence in depth
  working. Tell it apart by mutating the whole path at once and seeing whether the behaviour moves.
- **A broken or lying harness.** Two "survivors" were really `declared and not used` and
  `missing return` in the mutation itself. A build error produces no `--- FAIL` lines, so it reads
  exactly like a gap.

So the harness needs two guards of its own, and neither is optional:

- **Assert the mutation applied.** `shasum` the file before and after. If it is unchanged, the
  pattern did not match and the result is meaningless.
- **Detect build failures separately**, before parsing for `--- FAIL`.

Even with both, do not trust a surprising survivor. On #1284 a run reported that the
binary-detection mutation survived. We ran the same mutation by hand three times, with and without
the `gofmt` step, and the test failed on it exactly as intended. We never explained the
discrepancy. The tests were right and the tooling around them was not. So trust the tests over the
tooling. Hand-verify a survivor before you write a test to close a gap that may not exist.

### A green suite is not evidence of the property you think it is

Mutation testing exists to catch one failure. It turned up four times in one session
(2026-08-10), in four different disguises. Each test looked like coverage of a class and exercised
only the member that already worked:

- **`ext/files` symlink containment.** The test used a symlinked *parent directory*, and
  parent-resolution catches exactly that. A symlink as the *final* component escaped the root. The
  suite was green, and the property did not hold.
- **Malformed `edit_file` arguments.** Every case was an *entirely* malformed list. Dropping a bad
  element left zero hunks, so the call still failed, via `errNoHunks`, for the wrong reason. A
  list with one good edit and one bad one would have applied the good one and reported success.
- **`checkpoint` capture through a symlink.** `TestSymlinkAtCaptureIsNotFollowed` leaves the link in
  place at restore time, so the restore-side guard refuses either way. Swapping `Lstat` for `Stat`
  in `capture` survived the entire suite.
- **`write_file`'s type and boundary checks.** Both run before `os.Root`, and `os.Root` refuses
  either way. So deleting them broke nothing any test asserted. This one resolves differently from
  the other three. See below.

The shape is the same each time. **A second guard downstream makes the test pass regardless of the
behaviour it names.** So the fix is usually the same too. Reach for it before adding another case
beside the existing ones. Construct the scenario so the downstream guard is absent. That usually
means letting the interfering condition resolve before the assertion. Replace the symlink with a
real file, or mix a valid element in with the invalid one. If you cannot remove the second guard,
the test cannot tell the two implementations apart, and it is not testing what its name says.

### Sometimes the guard exists for the message, not the behaviour (#1284)

`write_file` refuses a non-regular target. It also tells a path that escapes the workspace apart
from one that merely does not exist. Remove either check, and `os.Root` still refuses the write. No
safety property changes, and no outcome-based test can see the difference. The obvious conclusion is
that the guards are redundant. They are not.

They change **what the model is told**. Without the type check, a write to a directory reports
*"already exists, pass expect_hash to replace it"*. That advice can never work. The model follows
it and is refused again. It has wasted a turn finding out that the tool misdescribed the problem.
Without the boundary branch, a symlink pointing out of the workspace reads as an ordinary write
failure, not a containment refusal. That sends debugging in the wrong direction.

For a model-facing tool the message *is* part of the contract, because the model computes its next
action from it. So those two tests assert the wording rather than the outcome, and a doc comment
says so. Without the comment, the next reader sees an assertion on a string, decides it is
brittle, and deletes it.

---

### Sandbox tests that pass for the wrong reason (#203, #155)

Two from mission #162, both the second-guard shape above in a different costume.

- **Escalation is a no-op when exec is unconfined** (#203). CI is Linux, and exec is always
  unconfined on Linux. So a test that expects an escalation ask passes on a Mac and sees no ask in
  CI. The
  coding-harness exercise solves it with `standInSandbox`, which supplies a fake confinement on
  Linux so the ask path still runs. Reach for the same thing rather than skipping on Linux.
- **`t.TempDir()` is writable from inside the sandbox**, because the sandbox lets commands write the
  temp directory. Imagine a live test checks "the sandbox refused a write outside the workspace"
  against a `t.TempDir()` path. It passes for no reason. Set `WritePaths: []string{}` and use a path
  outside every allowed root (the `workspace(t)` helper in `ext/exec` builds one).

---

## Examples

Agent examples are under `examples/agents/`: `agent-async`, `multi-agent`, `kitchen-sink`,
`deep-agent-supervisor`. Skills and tasks-v2 examples remain with their SEP.

Each example imports the shared recipes in `examples/agents/common.{just,mk}` (`agent` / `demo` /
`test`). `demo.sh` resolves model and endpoint from `llm.json` when `MODEL` / `BASE_URL`
are unset.

**`examples/agents/llm.json`** holds named connections in the `ConnectionsConfig` shape. It has **no
keys, only `apiKeyEnv` names**, and the code reads those with `os.Getenv` at runtime. The active
connection is a local model so `demo` works offline. A model router (OpenRouter/LiteLLM) is just a
connection with `baseURL` at the gateway. It ships a per-model menu across providers, plus dedicated
embedder connections and an `"embedder"` role. A copy of the file gets the full switchable set.
`llm.local.json` is gitignored and overrides per machine.

### kitchen-sink

The "every feature wired at once" harness. It reuses the chat surface rather than adding a binary.
Variables drive it (`SESSION_STORE`, `EMBED_*`, `OFFLOAD_THRESHOLD`, `COMPACT_TOKENS`, `EXPORTER`,
`ACTIVE`). `allup`/`alldown` span `docker/backends` and `docker/observability`. `preflight.sh`
probes postgres/OTLP/embedder and prints bring-up commands. A small demo MCP server has its own
go.mod, with `greet`, a large-output `report` that trips offloading, and `analyze` for a
sub-agent.

`kitchen-sink.json` wires signals, interruptible turns, and the runner-control pool together
(`analyst` has `canSignal:true`; top-level `signalPolicy:"inject"`, `interruptible:true`,
`runnerControl:true`), plus a `review_team` fan-out and an async `deep_researcher`.
`kitchen-sink-team.json` is the triage → billing/technical team config. Team mode has no memory,
so it runs through a `CONFIG` parameter.

**Two operational gotchas bit hard here.** `surfaces/chat/NOTES.md` covers both too:

1. **Server readiness must be a TCP probe, not `curl GET /mcp`.** A GET to an MCP endpoint opens
   the server's SSE stream and never returns. So a curl-based check hangs the launch forever. Use
   `(exec 3<>/dev/tcp/host/port)`. `scripts/playground.sh` still has the hanging-curl form.
2. **Backgrounded demo-server logs must be redirected off the terminal** (`>$LOG 2>&1`). They,
   and mcpkit's per-connection SSE logging, clobber the inline TUI's input region.

**Never commit compiled binaries.** The root `CLAUDE.md` says so, and CI now enforces it.
