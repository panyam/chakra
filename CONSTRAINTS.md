# chakra Constraints

Enforceable rules for this repository. Two conventions came over from mcpkit and still hold wherever
this code touches the protocol layer. Code there uses typed contexts rather than bare
`context.Context` values, and consolidated entry structs rather than long positional parameter
lists.

## A1: the dependency runs one way, and providers live in the root module

chakra depends on mcpkit. mcpkit must never depend on chakra.

This used to need a grep. Both trees lived in one repository, so an upward import would have
compiled, and only a grep recipe stood in its way. Nobody ran that recipe. The repository split now
enforces the rule structurally. mcpkit has no requirement on this module, and adding one would be a
deliberate, reviewable act rather than an accidental import line.

One internal rule still needs enforcing. LLM-provider integrations belong in the root module. No
sub-module may take a dependency on an LLM-provider SDK, and that covers `host/`, `surfaces/`,
`store/` and `ext/*`. The providers here speak raw HTTP and carry no vendor SDK at all. That is a
stronger form of the same rule, and A10's requires recipe below checks it.

**Verify:** from mcpkit, `grep -rn "panyam/chakra" --include='go.mod' .` returns nothing.

## A2: Runner events are wire-serializable

The Runner emits events, and every event type carries JSON tags, a stable `kind` discriminator, and no Go-only payloads (channels, funcs, non-marshalable interfaces). Web surfaces use a wire projection of these events. That projection must be a 1:1 mapping, never a translation layer.

**Verify:** the event round-trip test in this module marshals and unmarshals every event kind through encoding/json and compares.

## A3: One vendor `_meta` prefix

This module reads and writes vendor-namespaced `_meta` keys, and all of them use `io.github.panyam.mcpkit/`. `docs/AGENT_DESIGN.md` pins that prefix. No ad-hoc prefixes.

**Verify:** `grep -rn '_meta\|Meta\[' ./ --include='*.go' | grep -i 'io\.github\|dev\.\|com\.'` shows only the pinned prefix. Manual; read the hits.

## A4: The loop never owns the user interface or process-global output

The Runner exposes callbacks and event streams; it never prints, prompts, or renders. Logging follows the same rule. Agent code logs only through an injected *slog.Logger, and a nil logger discards. It never logs through fmt, os.Stdout/Stderr, log, or slog.Default. Anything user-facing lives in surfaces, such as agentchat and the web hosts, which build on the module.

**Verify:** `grep -rn "fmt.Print\|os.Stdout\|os.Stdin\|slog.Default\|log.Print" ./ --include='*.go' | grep -v _test.go | grep -vE '(^|/)(surfaces|examples)/'` returns nothing. The exclusions are part of the constraint, not a fudge. A surface exists to print, and an example is a surface with a `main`. Without the exclusions the recipe returns 37 legitimate hits and gets ignored.

The anchoring in the exclusion matters, and it only started to matter at the repository split. The exclusion used to read `'/surfaces/'`, which matched while the tree lived at `agent/surfaces/`. At the root the path is `surfaces/chat/...` with no leading slash. So the old exclusion stopped matching, and the recipe reported every legitimate print in the tree. That failure looked like diligence, and so it was harder to notice.

## A5: core.RawJSON for JSON-valued public fields

JSON-valued fields in this module's public types use `core.RawJSON`, never bare `json.RawMessage`. `core.RawJSON` is wire-transparent, parses once, and has a typed Bind. Deltas carry streamed argument pieces in JSON-fragment fields, and those fields are still strings. Fragments become a RawJSON value at the Accumulator's fold, which is the promotion boundary.

**Verify:** `grep -n "json.RawMessage" *.go | grep -v _test | grep -v "NewRawJSON\|toolCallArgsFromJSON"` shows no field on an **exported** struct. Read the hits; four are expected and none is a violation. Three are conversion sites (`anthropic_provider.go:149`, `elicitation.go:67`, `stages.go:231`). The fourth is the field `anthropicContentBlock.Input` (`anthropic_provider.go:340`), but its struct is unexported. That struct is an internal DTO for Anthropic's wire format, and A5 governs the public surface.

The recipe is deliberately root-only. The host reads some config structs from a user's JSON file. Their fields keep `json.RawMessage`, and the host wraps them at the seam where it hands them to this module. `host/subagents.go` does this for both `InputSchema` and `ResponseSchema`. So a hit in `host/` would show the pattern working, not a violation.

Two sites broke this rule until #35. `AgentSourceConfig.InputSchema` and `AsyncAgentSourceConfig.InputSchema` both held a whole JSON schema document as `json.RawMessage`, while `ResponseSchema` on the same structs was already `core.RawJSON`. The recipe had been unrunnable since panyam/mcpkit#1290 moved the tree. A checkpoint corrected the recipe's path, and the corrected recipe found both sites.

## A6: Mechanisms in the client, policy in the agent

A primitive belongs in `client/` (or an events/skills SDK) if any non-agent consumer would want it. That consumer might be a script, a service, a poller, or `cmd/testclient`. A primitive belongs in `chakra` only if it requires a model and a turn to make sense. The decidable tell is the natural return type. A function is client-layer if it returns a protocol object, such as `core.DetailedTask`, `events.Event` or `core.InputResponses`. It is agent-layer if it returns a model-facing object, such as `core.ToolResult`, injected context, or a proactive turn. Check this first when adding a helper to agent code. Task polling, `BackgroundTask`, and event stream consumption all started out over-kept in the agent, and all moved to `client/`.

**Verify:** no `chakra` exported type or function returns a value that a non-agent caller could use standalone without also depending on the Runner/policies; conversely, agent public API that returns `core.ToolResult` / injected context belongs here.

## A7: Sub-agents get no ambient parent state (memory is not shared)

A sub-agent receives only the task arguments and injected context, which cross the parent-to-child boundary explicitly. It gets no working memory and no shared handle to the parent's stores. A child's location is not guaranteed. The in-process `AgentSource` is the degenerate co-located case. In the general case the child runs on another host, provider, or model. Shared parent memory would assume co-location, and A2 wire-serializability forbids that, because a store pointer can't cross a wire. A child that needs memory owns its own store, never a namespace into the parent's store. The child's own Runner configures that store, and the store is opaque to the parent, like a stateful MCP tool's database. Hierarchy means parent recall across children. It needs a prefix/hierarchical namespace query, and the `MemoryStore` seam does not have one yet (exact-match today), so hierarchy waits on that query. Issue 1151 and `docs/AGENT_COMPOSITION.md` § Sub-agents and memory hold the rationale and the full decision.

**Verify:** host personas are built over the server-only `serverTools`, never the memory-bearing aggregate. `TestSubAgentCannotReachParentMemory` in `host` guards this. In that test a persona calls `remember`, the call hits an unknown tool, and the parent store is still empty.

## A8: No in-repo workflow engine; orchestration is model-driven or integrated

mcpkit does not ship a code-driven workflow / state-machine engine. Orchestration is either **model-driven** or **delegated**. Model-driven orchestration uses the agent Runner loop, sub-agents, and the async control plane (triggers / injection / events). Delegated orchestration hands the work to a dedicated external engine, such as Temporal or Step Functions, and the application integrates that engine. A workflow engine has no AI in it. It is a commodity state machine, the dual of the agent loop rather than an extension of it. The canonical workflow patterns already build on shipped primitives. Those patterns are prompt chaining, routing, parallelization, orchestrator-workers and evaluator-optimizer, and the primitives are `AgentSource` / `Team` / `FanOut` + `TriggerPolicy` / injection. Parity is not a reason to build one, so do not accept "Mastra / LangGraph / Eino ships one". Require a concrete use case, and check that the shipped primitives cannot express it. Even then, prefer integration over reimplementation. The decision dates from 2026-08-04. Its full rationale is in the former Phase 4 epic, issue 928, which was closed not-planned, and in `docs/AGENT_SDK_ROADMAP.md` § Phase 4.

**Verify:** no `workflow/` engine module exists. `grep -rn '^package workflow' --include='*.go' .` returns nothing.

## A9: The provider seam exposes loop-visible capabilities; loop-invisible optimizations are left out

To sort any provider-specific behavior, ask **does the agent loop act on it?**

- **Loop-invisible optimizations** include prompt caching, extended/interleaved thinking with signed-block preservation, fast mode, and task budgets. The Runner receives the same `Delta` stream either way. Caching only makes a call cheaper, and thinking is reasoning we already parse. So these change nothing above the seam. They are **not** SDK concerns. The no-SDK native providers do not implement them. Those providers are `agent/anthropic_provider.go` and `agent/openai_provider.go`. If a concrete need ever appears, wrap the vendor's **official SDK** behind the `Provider` seam. Hand-maintained no-SDK wire code would chase the vendor's API drift forever.
- **Loop-visible capabilities** include token-confidence / logprobs and grammar-constrained decoding. Logprobs feed routing, abstention, and confidence-gated cascades. Grammar-constrained decoding gives a stronger structured-output guarantee, and the loop consumes it. These feed agent decisions and **are** agent-relevant. Expose them **capability-optionally** through the `Provider` seam. A nil or zero value means the provider does not support the capability, and the loop degrades gracefully. Don't reimplement a vendor's full API surface to get them, and don't gate them on any single vendor. Logprobs, for instance, are an OpenAI-wire / local-inference capability, and Anthropic does not expose them. A per-request seed is on this side too, since a vote over drafts depends on it. Suppose a caller sets `ProviderRequest.Seed` or `ProviderRequest.Guided` explicitly, and the provider cannot honor it. The provider then refuses it with `ErrUnsupportedParam` before sending. A dropped seed or grammar would change what the loop gets back without saying so, which is worse than an error.
- **Server-specific extras** go through `OpenAIConfig.ExtraBody` when the caller asks for them. The provider merges that body in without interpreting it. llama.cpp's `cache_prompt` is the standing case. It is a loop-invisible optimization, so chakra models no caching field of its own, and the caller turns it on.
- **A provider says what it can send** through the optional `CapabilityReporter` (`CapabilitiesOf`). It reports which guided kinds it can send and whether it can send a seed. It's loop-visible, since a caller decides from it whether to ask for a grammar, and it describes the wire, not the model's skill. A provider without it reports nothing. The caller then asks for no seed or grammar, so the provider has nothing to refuse. The local-server presets are configurations of `OpenAIProvider` and add no wire code. They are `NewLlamaCppProvider` and `NewOllamaProvider`.
- **Reporting what a cache served is accounting, and it is allowed.** `Usage.CachedInputTokens` tells a caller how much of `InputTokens` came from the provider's prompt cache, so cost and eval reports can use it. It's read from the response and never sent, so it controls nothing.

The first such wrapper is `provider/anthropic` (#274). It wraps Anthropic's Go SDK for prompt caching, in its own module per A10. The caller hands it an SDK client, so the SDK owns auth, retries and the platform. agentchat, agentweb and the coding profile route `"anthropic"` connections to it through `host.ConnectionTypes`. The root's `AnthropicProvider` stays SDK-free and sends no caching.

Never thread provider-specific opaque state, such as Anthropic signed thinking blocks, through the neutral `agent.Message` or the Runner. That violates A2 wire-serializability and the provider-agnostic core, whether or not the loop acts on the feature. The SDK's job is the Provider *seam*, not provider parity. The rationale dates from 2026-08-04. Issue 953 covered caching and thinking, which are loop-invisible, and was closed not-planned. Logprob #1053 was kept as a loop-visible capability. See `docs/AGENT_SDK_ROADMAP.md` § Phase 5.

**Verify:** `agent.Message` carries no provider-specific reasoning/signature field; the no-SDK providers send no `cache_control`, `thinking`, fast-mode, or task-budget request fields. (Loop-visible capabilities like logprobs are permitted as capability-optional seam additions.)

## A10: Dependency weight decides module boundaries; seam interfaces live with the Runner

Two rules. People break the second one by accident.

**Seam interfaces live in `chakra`, with the Runner that consumes them.** `chakra` defines `Provider`, `ToolSource`, `MemoryStore`, `RunStore`, `ToolResultStore`, `Embedder`, and `Compactor` where they are used. That follows the Go convention that an interface belongs to its consumer rather than its implementors. There is no `api` or interface-only package, and there should not be. Such a package would separate every seam from the only code that depends on it. Implementations don't need one either, since they satisfy these interfaces structurally without importing anything.

**A satellite module is for dependency weight, not for layering.** An implementation lives beside its interface in `chakra` when it costs no third-party dependency, and moves to its own module when it needs a driver or SDK. So the dependency-free implementations sit next to their interfaces. Those are `InMemoryRunStore`, `InMemoryMemoryStore`, `InMemorySemanticStore`, `FileToolResultStore`, and the no-SDK `OpenAIProvider` / `AnthropicProvider`. The redis, gorm, and pgvector implementations live in `store/redis` and `store/gorm`, each with its own `go.mod`.

This rule keeps `chakra` cheap to embed. It pulls mcpkit's own modules and nothing else. So depending on the agent SDK never drags in a database driver or a vendor SDK, and a consumer gets one only by importing it. A1 governs the *outward* direction. Under A1, nothing outside `chakra` may import an LLM SDK or this module. A10 governs the inward direction. A9 depends on A10 too. When A9 says "wrap the vendor's official SDK behind the seam", the wrapper goes in a satellite module, not here.

Adding a dependency-free implementation beside its interface is always fine. Adding one that carries a driver is the violation, and the fix is a new satellite module rather than a new direct require.

**Verify:** `go mod edit -json | jq -r '.Require[] | select(.Indirect|not) | .Path'` lists only `github.com/panyam/mcpkit` and `github.com/panyam/servicekit`. Any other entry means someone added a dependency to the core agent module. Either justify it as genuinely dep-free infrastructure, or move the code to a satellite module.

## A11: Only a true inverse runs unattended

Anything that reverses a side effect falls into one of two kinds, and only one of them may be
invoked without a human saying yes.

- **A restore** returns local state to what was captured. It is a genuine inverse, order-independent,
  idempotent, unaffected by intervening work, and near-certain to succeed. The harness runs it.
- **A compensation** is a *new* action that partially offsets an old one, such as deleting an issue
  that was created or refunding a charge. It is not an inverse, because notifications fired and
  webhooks ran. It is order-dependent, and it can fail on permissions beyond those of the original
  call. It also breaks once something has come to depend on the effect. It is surfaced to a human
  and never auto-invoked. The same applies to a model-proposed offset, which is additionally a
  guess.

In code, `checkpoint.Reversal.Reversible()` requires a `Restore`, and a `Compensate` alone must not
satisfy it. If it counted a `Compensate`, a tool could auto-approve under `ModeReversibleAuto` based
on an unverified offset. An approval path may key on reversibility. If it does, it reads that method
rather than re-deriving the answer.

Chaining compensations automatically would need ordering, partial-failure recovery and retries. That
is a saga orchestrator, and **A8** already rules it out. This constraint is the operational edge of
A8 rather than a separate policy.

One corollary is easy to lose. A reversal path must **report what it could not reverse**. Imagine a
turn wrote 3 files and created an issue. The restore reports "3 files restored" and never mentions
the issue. That safety net has a hole nobody reported, and once a hole goes unreported, nobody
checks it again.

**Verify:** `grep -n "func (r Reversal) Reversible" ./ext/checkpoint/reverser.go` returns a body
testing `Restore` only; `TestCompensateAloneIsNotReversible` and `TestProposalNeverRunsWithoutApproval`
pin both halves.

## A12: MCP server lifecycle is decoupled from the agent

The agent is a **pure MCP client**. Here the agent means `host/` and the surfaces built on it, e.g. `surfaces/chat/`. It connects to servers by URL and does not own their process lifecycle. It talks to MCP servers, and it MUST NOT spawn, supervise, restart, or kill them. Bringing servers up and down is an operator/launcher concern, handled by a `just servers-up` recipe, docker, or systemd. The agent process does not do it as a side effect of starting or stopping.

The rule prevents two failure modes:

- **Lifetime coupling** happens when the launcher boots the servers as children of the agent and trap-kills them on exit. Restarting the chat then kills the servers, and vice versa. With the two decoupled, servers survive chat restarts. You can reconnect a fresh agent to servers that are already running. Every real MCP client, such as Claude Code or Cursor, treats remote servers this way.
- **Boot coupling** means one unreachable server takes the whole agent down, and it should not. The target is for the agent to connect asynchronously and degrade per server, so a down server shows as failed/paused/needs-login. The agent should not abort boot fail-fast.

mcpkit's `examples/agents/kitchen-sink` is the reference decoupling, and it remained in mcpkit when this tree was extracted. There, `servers.sh` owns the server processes through `just servers-up` / `servers-down` / `servers`. `run.sh` only *checks* the ports, and if any are down it points at `servers-up`. It never boots or kills them.

One exception is sanctioned. The client's stdio transport, `client.CommandTransport`, owns the subprocess it speaks to. Every MCP client has this standard, opt-in, per-connection ownership for `command`-style servers (the `.mcp.json` shape). It lives in mcpkit's `client/`, is chosen explicitly, and is not the host spawning servers behind the user's back. The host wires it up through `ServerConfig.Command` (#161). The client's transport starts and ends the process, and the host only configures it. The command comes from operator configuration, never from a tool argument.

A second exception is sanctioned on the same reasoning. **An extension may spawn the subprocess it
owns.** `ext/lsp` starts a language server from `ServerSpec.Command`, and `agent/ext/exec` runs the
commands in `Config.Commands`. Both commands come from operator configuration, and no tool argument
can reach either one. So no model can name the process that starts, and neither can an instruction
injected into what a model read. The exception depends on that property. The same property is the
whole reason `exec` refuses to let the model compose a command line. Suppose an extension took a
binary path from a tool argument. That would violate this constraint, whatever the extension called
itself.

It follows that extension-owned subprocesses run outside the exec extension's sandbox. An extension
can spawn a process for itself, and a wrapper around a `ToolSource` never sees that process. A
convention could ask every extension to route its spawns through a shared helper, but that would be
enforcement in name only. Issue 1312 records the decision, and
`experimental/agent/ext/exec/README.md` has the rationale.

The async graceful-degrade half of this constraint is a target and is not yet implemented. `NewApp` today still connects synchronously and fails fast. The enforced half says the host does not manage server *processes*.

**Verify:** the host must not spawn or kill processes. This must print nothing:

```bash
grep -rn 'os/exec\|exec\.Command\|\.Process\b\|syscall\.\(Kill\|Exec\)\|StartProcess' experimental/agent/host/ --include='*.go' | grep -v '_test.go'
```

*Carried over from mcpkit's project-wide `CONSTRAINTS.md` (C6) when the agent SDK was extracted. It
constrains the host, so it followed the host.*
