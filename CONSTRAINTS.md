# chakra Constraints

Enforceable rules for this repository. Two conventions carried over from mcpkit still hold
wherever this code touches the protocol layer: typed contexts rather than bare `context.Context`
values, and consolidated entry structs rather than long positional parameter lists.

## A1: the dependency runs one way, and providers stay in the root module

chakra depends on mcpkit. mcpkit must never depend on chakra.

This used to need a grep. Both trees lived in one repository, so an upward import would have
compiled and the only thing stopping it was a recipe nobody ran. The repository split enforces it
structurally: mcpkit has no requirement on this module, and adding one would be a deliberate,
reviewable act rather than an accidental import line.

What still needs enforcing is internal. LLM-provider integrations belong in the root module. No
sub-module (`host/`, `surfaces/`, `store/`, `ext/*`) may take a dependency on an LLM-provider SDK.
The providers here speak raw HTTP and carry no vendor SDK at all, which is the stronger form of the
same rule and is checked by A9's requires recipe below.

**Verify:** from mcpkit, `grep -rn "panyam/chakra" --include='go.mod' .` returns nothing.

## A2: Runner events are wire-serializable

Every event type the Runner emits carries JSON tags, a stable `kind` discriminator, and no Go-only payloads (channels, funcs, non-marshalable interfaces). The wire projection used by web surfaces must be a 1:1 mapping, never a translation layer.

**Verify:** the event round-trip test in this module marshals and unmarshals every event kind through encoding/json and compares.

## A3: One vendor `_meta` prefix

All vendor-namespaced `_meta` keys this module reads or writes use `io.github.panyam.mcpkit/` (pinned in `docs/AGENT_DESIGN.md`). No ad-hoc prefixes.

**Verify:** `grep -rn '_meta\|Meta\[' ./ --include='*.go' | grep -i 'io\.github\|dev\.\|com\.'` shows only the pinned prefix. Manual; read the hits.

## A4: The loop never owns the user interface or process-global output

The Runner exposes callbacks and event streams; it never prints, prompts, or renders. Logging is the same: agent code logs only through an injected *slog.Logger (nil discards), never fmt, os.Stdout/Stderr, log, or slog.Default. Anything user-facing lives in surfaces (agentchat, web hosts) built on the module.

**Verify:** `grep -rn "fmt.Print\|os.Stdout\|os.Stdin\|slog.Default\|log.Print" ./ --include='*.go' | grep -v _test.go | grep -vE '(^|/)(surfaces|examples)/'` returns nothing. The exclusions are the constraint, not a fudge: printing is what a surface is for, and an example is a surface with a `main`. Without them the recipe returns 37 legitimate hits and gets ignored.

The anchoring is load-bearing and was not, until the repository split. The exclusion read `'/surfaces/'` while the tree lived at `agent/surfaces/`, and at the root the path is `surfaces/chat/...` with no leading slash, so it stopped matching and the recipe reported every legitimate print in the tree. It failed in the direction that looks like diligence, which is the harder one to notice.

## A5: core.RawJSON for JSON-valued public fields

JSON-valued fields in this module's public types use `core.RawJSON` (wire-transparent, parse-once, typed Bind), never bare `json.RawMessage`. JSON-fragment fields (streamed argument pieces in Deltas) stay strings; the Accumulator's fold is the promotion boundary where fragments become a RawJSON value.

**Verify:** `grep -n "json.RawMessage" *.go | grep -v _test | grep -v NewRawJSON` shows only conversion sites, no struct fields. **This currently fails, at two sites rather than the one first reported**: `AgentSourceConfig.InputSchema` (`agent_source.go:67`) and `AsyncAgentSourceConfig.InputSchema` (`async_agent_source.go:31`) are both public struct fields holding a whole JSON document, which is what A5 says should be `core.RawJSON`. Found when the path was corrected during a checkpoint, having been unrunnable since panyam/mcpkit#1290 moved the tree. Tracked in #35.

## A6: Mechanisms in the client, policy in the agent

A primitive belongs in `client/` (or an events/skills SDK) if any non-agent consumer would want it (a script, a service, a poller, `cmd/testclient`); it belongs in `chakra` only if it requires a model and a turn to make sense. The decidable tell is the natural return type: functions returning protocol objects (`core.DetailedTask`, `events.Event`, `core.InputResponses`) are client-layer; functions returning model-facing objects (`core.ToolResult`, injected context, a proactive turn) are agent-layer. When adding a helper to agent code, check this first — task polling, `BackgroundTask`, and event stream consumption were all initially over-kept in the agent and moved to `client/`.

**Verify:** no `chakra` exported type or function returns a value that a non-agent caller could use standalone without also depending on the Runner/policies; conversely, agent public API that returns `core.ToolResult` / injected context stays here.

## A7: Sub-agents get no ambient parent state (memory is not shared)

A sub-agent receives only what crosses the parent-to-child boundary explicitly: the task arguments and injected context. It gets no working memory and no shared handle to the parent's stores. A child's location is not guaranteed — the in-process `AgentSource` is the degenerate co-located case; the general case is a child on another host, provider, or model — so shared parent memory would assume a co-location that A2 wire-serializability forbids (a store pointer can't cross a wire). A child that needs memory owns its own (configured on its own Runner, opaque to the parent, like a stateful MCP tool's database), never a namespace into the parent's store. Hierarchy (parent recall across children) waits on a prefix/hierarchical namespace query the `MemoryStore` seam does not have (exact-match today). Rationale and the full decision: issue 1151, `docs/AGENT_COMPOSITION.md` § Sub-agents and memory.

**Verify:** host personas are built over the server-only `serverTools`, never the memory-bearing aggregate; guarded by `TestSubAgentCannotReachParentMemory` in `host` (a persona's `remember` hits an unknown tool and the parent store stays empty).

## A8: No in-repo workflow engine — orchestration is model-driven or integrated

mcpkit does not ship a code-driven workflow / state-machine engine. Orchestration is either **model-driven** (the agent Runner loop, sub-agents, the async control plane: triggers / injection / events) or **delegated** to a dedicated external engine (Temporal, Step Functions, and the like) that the application integrates. A workflow engine has no AI in it — it is a commodity state machine, the dual of the agent loop, not an extension of it. The canonical workflow patterns (prompt chaining, routing, parallelization, orchestrator-workers, evaluator-optimizer) already build on shipped primitives (`AgentSource` / `Team` / `FanOut` + `TriggerPolicy` / injection). Do not accept "Mastra / LangGraph / Eino ships one" (parity) as a reason to build one; require a concrete use case the shipped primitives cannot express, and even then prefer integration over reimplementation. Decision + full rationale (2026-08-04): the former Phase 4 epic (issue 928, closed not-planned) and `docs/AGENT_SDK_ROADMAP.md` § Phase 4.

**Verify:** no `workflow/` engine module exists — `grep -rn '^package workflow' --include='*.go' .` returns nothing.

## A9: The provider seam exposes loop-visible capabilities; loop-invisible optimizations stay out

The dividing test for any provider-specific behavior is **does the agent loop act on it?**

- **Loop-invisible optimizations** — prompt caching, extended/interleaved thinking with signed-block preservation, fast mode, task budgets. The Runner receives the same `Delta` stream regardless (caching = cheaper, thinking = reasoning we already parse), so these change nothing above the seam. They are **not** SDK concerns. The no-SDK native providers (`agent/anthropic_provider.go`, `agent/openai_provider.go`) do not implement them; if a concrete need ever appears, wrap the vendor's **official SDK** behind the `Provider` seam rather than hand-maintaining no-SDK wire code that chases the vendor's API drift forever.
- **Loop-visible capabilities** — token-confidence / logprobs (feeds routing, abstention, confidence-gated cascades) and grammar-constrained decoding (a stronger structured-output guarantee the loop consumes). These feed agent decisions and **are** agent-relevant. Expose them **capability-optionally** through the `Provider` seam (nil / zero value = the provider does not support it, loop degrades gracefully) — not by reimplementing a vendor's full API surface, and not gated on any single vendor (logprobs are an OpenAI-wire / local-inference capability; Anthropic does not expose them).

Never thread provider-specific opaque state (e.g. Anthropic signed thinking blocks) through the neutral `agent.Message` or the Runner — that violates A2 wire-serializability and the provider-agnostic core, regardless of which side of the test the feature falls on. The SDK's job is the Provider *seam*, not provider parity. Rationale (2026-08-04): issue 953 (caching/thinking, loop-invisible) closed not-planned; logprob #1053 kept as a loop-visible capability. See `docs/AGENT_SDK_ROADMAP.md` § Phase 5.

**Verify:** `agent.Message` carries no provider-specific reasoning/signature field; the no-SDK providers send no `cache_control`, `thinking`, fast-mode, or task-budget request fields. (Loop-visible capabilities like logprobs are permitted as capability-optional seam additions.)

## A10: Dependency weight decides module boundaries; seam interfaces stay with the Runner

Two rules, and the second is the one that gets broken by accident.

**Seam interfaces live in `chakra`, with the Runner that consumes them.** `Provider`, `ToolSource`, `MemoryStore`, `RunStore`, `ToolResultStore`, `Embedder`, and `Compactor` are all defined where they are used, per the Go convention that an interface belongs to its consumer rather than its implementors. There is no `api` or interface-only package, and there should not be: it would separate every seam from the only code that depends on it, and implementations satisfy these structurally without importing anything.

**A satellite module is for dependency weight, not for layering.** An implementation lives beside its interface in `chakra` when it costs no third-party dependency, and moves to its own module when it needs a driver or SDK. That is why `InMemoryRunStore`, `InMemoryMemoryStore`, `InMemorySemanticStore`, `FileToolResultStore`, and the no-SDK `OpenAIProvider` / `AnthropicProvider` all sit next to their interfaces, while redis, gorm, and pgvector implementations are `store/redis` and `store/gorm` with their own `go.mod`.

The property this protects is that `chakra` is cheap to embed: it pulls mcpkit's own modules and nothing else, so depending on the agent SDK never drags in a database driver or a vendor SDK a consumer does not use. A1 governs the *outward* direction (nothing outside `chakra` may import an LLM SDK or this module); this governs the inward one. A9 depends on it too — "wrap the vendor's official SDK behind the seam" means in a satellite module, not here.

Adding a dependency-free implementation beside its interface is always fine. Adding one that carries a driver is the violation, and the fix is a new satellite module rather than a new direct require.

**Verify:** `go mod edit -json | jq -r '.Require[] | select(.Indirect|not) | .Path'` lists only `github.com/panyam/mcpkit` and `github.com/panyam/servicekit`. Any other entry means a dependency landed in the core agent module: justify it as genuinely dep-free infrastructure, or move the code to a satellite module.

## A11: Only a true inverse runs unattended

Anything that reverses a side effect falls into one of two kinds, and only one of them may be
invoked without a human saying yes.

- **A restore** returns local state to what was captured. It is a genuine inverse, order-independent,
  idempotent, unaffected by intervening work, and near-certain to succeed. The harness runs it.
- **A compensation** is a *new* action that partially offsets an old one — deleting an issue that was
  created, refunding a charge. It is not an inverse (notifications fired, webhooks ran), it is
  order-dependent, it can fail on permissions the original call never needed, and it breaks once
  something has come to depend on the effect. It is surfaced to a human and never auto-invoked. The
  same applies to a model-proposed offset, which is additionally a guess.

Concretely: `checkpoint.Reversal.Reversible()` requires a `Restore`, and a `Compensate` alone must not
satisfy it. Counting one would let a tool auto-approve under `ModeReversibleAuto` on the strength of an
offset nobody verified. Any approval path that keys on reversibility reads that method rather than
re-deriving the answer.

Chaining compensations automatically — ordering, partial-failure recovery, retries — is a saga
orchestrator, which **A8** already rules out. This constraint is the operational edge of A8 rather than
a separate policy.

A corollary that is easy to lose: a reversal path must **report what it could not reverse**. A restore
that says "3 files restored" while a created issue goes unmentioned is a safety net with an unreported
hole, and an unreported hole stops being checked.

**Verify:** `grep -n "func (r Reversal) Reversible" ./ext/checkpoint/reverser.go` returns a body
testing `Restore` only; `TestCompensateAloneIsNotReversible` and `TestProposalNeverRunsWithoutApproval`
pin both halves.

## A12: MCP server lifecycle is decoupled from the agent

The agent (`host/` and the surfaces built on it, e.g. `surfaces/chat/`) is a **pure MCP client**. It connects to servers by URL and does not own their process lifecycle: it MUST NOT spawn, supervise, restart, or kill the MCP servers it talks to. Bringing servers up and down is an operator/launcher concern — a `just servers-up` recipe, docker, systemd — not something the agent process does as a side effect of starting or stopping.

The rule prevents two failure modes:

- **Lifetime coupling**: if the launcher boots the servers as children of the agent (and traps-kills them on exit), restarting the chat kills the servers and vice versa. Decoupled, servers survive chat restarts — you can reconnect a fresh agent to already-running servers, which is also how every real MCP client (Claude Code, Cursor) treats remote servers.
- **Boot coupling**: one unreachable server should not take the whole agent down. The target is that the agent connects asynchronously and degrades per-server (a down server shows as failed/paused/needs-login), rather than fail-fast aborting boot.

The reference decoupling is mcpkit's `examples/agents/kitchen-sink`, which stayed there when this tree was extracted: `servers.sh` (`just servers-up` / `servers-down` / `servers`) owns the server processes; `run.sh` only *checks* the ports and points at `servers-up` if any are down — it never boots or kills them.

Sanctioned exception: the client's stdio transport (`client.CommandTransport`) owns the subprocess it speaks to — that is the standard, opt-in, per-connection ownership every MCP client has for `command`-style servers (the `.mcp.json` shape). It lives in mcpkit's `client/`, is chosen explicitly, and is not the host spawning servers behind the user's back. The host wiring for it (a `ServerConfig.command` surface) is a deferred follow-up, not a violation.

Second sanctioned exception, on the same reasoning: **an extension may spawn the subprocess it
owns.** `ext/lsp` starts a language server from `ServerSpec.Command`, and `agent/ext/exec` runs
the commands in `Config.Commands`. Both come from operator configuration and neither is reachable
from a tool argument, so no model, and no instruction injected into content a model read, can name
the process that starts. That property is what the exception rests on, and it is the whole reason
`exec` refuses to let the model compose a command line. An extension that took a binary path from a
tool argument would be a violation of this constraint whatever it called itself.

The corollary is that extension-owned subprocesses are outside whatever sandbox the exec extension
applies: a wrapper around a `ToolSource` never sees a process an extension spawned for itself, and
a convention asking every extension to route its spawns through a shared helper would be
enforcement in name only. Decided and recorded on issue 1312, with the rationale in
`experimental/agent/ext/exec/README.md`.

**Note:** the async graceful-degrade half of this constraint is a target, not yet implemented — `NewApp` today still connects synchronously and fail-fast. The enforced half is: the host does not manage server *processes*.

**Verify:** the host must not spawn or kill processes. Must print nothing:

```bash
grep -rn 'os/exec\|exec\.Command\|\.Process\b\|syscall\.\(Kill\|Exec\)\|StartProcess' experimental/agent/host/ --include='*.go' | grep -v '_test.go'
```

*Carried over from mcpkit's project-wide `CONSTRAINTS.md` (C6) when the agent SDK was extracted. It
constrains the host, so it followed the host.*
