# agent/ Design

The `` sub-module is mcpkit's host layer. It sits above `core/`, `server/`, and `client/` and runs the agentic loop. An application embeds it to become an MCP host. The module then connects MCP servers, hands their tools to a model, dispatches the model's tool calls, routes elicitation to a user interface, and streams the whole turn as typed events.

The module's tickets build against this document as their contract. Their tracking epic is mcpkit issue 895. The document pins decisions for later milestones to inherit, and implementation details live with their tickets.

## Scope

What the module is:

- An LLM provider abstraction with streaming and tool-call support
- The multi-step tool loop (the Runner)
- Tool aggregation across MCP servers and host-local functions (ToolSource)
- Elicitation routing to a pluggable UI callback
- Policy seams for context injection and event-initiated turns

What the module is not (deliberately):

- Not a chat service. Applications built on the module own sessions, wire transports (WebSocket/SSE), reconnect cursors, and persistence. See Surfaces below for the promotion path.
- Not a memory or multi-agent framework. No episodic memory, no supervision trees, no A2A. If a need for one comes up, it gets its own design pass.
- Not a protocol extension. Nothing here changes what travels between host and server beyond spec-legal vendor `_meta` keys.

`` depends on `core/` and `client/`, and may depend on other sub-modules such as `ext/ui`. Nothing in the root module or other sub-modules may depend on ``, and LLM-provider dependencies exist only here. `CONSTRAINTS.md` makes this dependency rule checkable.

## The four seams

### Provider

```go
// As built (issue 884).
type Provider interface {
    // Stream runs one model call. Deltas: text, reasoning, tool-call
    // start/args (no end marker; the Accumulator folds), finish, usage.
    Stream(ctx context.Context, req ProviderRequest) (Stream, error)
    // Generate is the non-streaming call used for utility work; with
    // ProviderRequest.ResponseSchema set it requests structured output.
    Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error)
}
```

The first production implementation to ship is OpenAI-compatible chat completions. It covers lmstudio, vllm, LiteLLM-style proxies, and gateways. It uses net/http plus servicekit's WHATWG-conformant SSE reader, with no SDK dependency. A `StubProvider` plays back scripted turns for deterministic tests. Failover is a Provider that fronts a main and a backup, so it wraps the seam and the Runner never knows about it.

We chose chat completions over the Responses-style API for two reasons. First, chat completions is the lingua franca of the compat zoo, where Responses support is partial, experimental, or a translation layer. Second, our architecture depends on its statelessness. The host owns conversation history, and that ownership is the module's whole premise. Tools come from MCP rather than the model platform. Failover only works when any endpoint can accept a full request cold. The one known exception trendline is gpt-oss/harmony reasoning fidelity on vllm, where Responses is native. If that matters someday, it becomes a second implementation behind the same interface.

### Runner

As built (issue 885):

```go
func (r *Runner) Run(ctx context.Context, history []Message, emit func(Event)) (*TurnResult, error)
```

The loop streams the model and dispatches its tool calls in parallel goroutines. Emission is serialized, and RoleTool messages are appended in call order. The loop feeds results back and repeats until a response has no tool calls, bounded by MaxSteps (default 8) and ctx cancellation. A tool call can fail as an unknown tool, a transport error, bad args, or an IsError result. Each failure goes back to the model as model-visible text and never aborts the loop. Only cancellation, provider failure, or the step cap end a turn early. `TurnResult.Messages` holds exactly the appended entries, so callers thread history with one append. A step-capped turn returns its partial `TurnResult` alongside `ErrMaxSteps`. The Runner trims that result to its last complete tool round, so a caller can keep the work the turn got through (issue 109).

The event kinds are turn-begin, thinking-begin/-delta/-end, text-delta, tool-begin, tool-end (includes IsError results), tool-error (dispatch failures only), turn-end, and error. Events carry no turn or session identity. In process, the emit closure is already turn-scoped. Wire layers wrap events in their own envelope with a session id, turn id and sequence. That keeps the module out of the ID-scheme business and keeps the event stream deterministic. Elicitation deliberately does NOT appear in the taxonomy. The event stream is one-way, and elicitation is request/response, so elicitation uses the ElicitationUI callback seam instead (see Elicitation below). Wire surfaces project that seam with their own request/response framing rather than faking it over events plus correlation IDs.

### ToolSource

```go
// Sketch, finalized in the tool-source ticket (issue 886).
type ToolSource interface {
    Tools(ctx context.Context) ([]ToolDef, error)
    Call(ctx context.Context, name string, args json.RawMessage) (core.ToolResponse, error)
}
```

There are three adapters. `ClientSource` wraps a single `client.Client` and makes MRTR-aware calls with a pluggable InputHandler. `FuncSource` serves host-local typed functions via `core.GenerateSchema`. `MultiSource` aggregates sources. **Issue 886 decided to lift the registry pattern and not depend on `ext/ui`.** ServerRegistry's value is client lifecycle plus apps-bridge management. ToolSource only needs the index shape. Aggregating ToolSources is also strictly more general, since it composes Func, Client, and any future registry adapter. Collisions work as they do in the registry. All claimants are kept, and a resolver callback handles ambiguous bare-name calls. The model-facing list shows colliding tools only in qualified `sourceID_name` form, so every tool is still reachable and no two names repeat. A thin `RegistrySource` adapter over `ext/ui` ships with the apps integration, since the dependency belongs there.

### Tool routing

Issues 901 and 902 added two narrowing mechanisms with distinct roles.

- `FilterSource` wraps any ToolSource with a static predicate. It has the per-profile allowlist shape, and it is a real capability boundary, since filtered tools are neither listed nor callable.
- `RunnerConfig.Selector` narrows per step with full history in hand. It is the context-aware routing seam, and keyword, embedding and scored selection plug in here. Selection is presentation to the model, not a security boundary, and the underlying source still routes calls.

Selectors are pure functions of history and the freshly listed tools. This purity rule keeps the lifecycle single-sourced. Invalidation has exactly one channel, notifications/tools/list_changed -> client.WithToolsListChangedHandler -> MultiSource.Invalidate. After that, per-step re-listing picks up the change. A caching selector keys on tool-list content, never on time or notifications. Searchable/deferred tools remain future work. There, a search_tools meta-tool would expose schemas on demand.

### Event pipeline primitives and gocurrent

The generic stages are Stage[E], Filter, Transform and Window. They are deliberately synchronous, caller-driven, and clock-injected. The policies drain at turn boundaries under the host's own locking, and every windowing test runs on a fake clock. gocurrent already ships the asynchronous complement, with Pipeline[T] channel components, Mapper, Broadcast filters, and Reducer with FlushPeriod. The two compose without adaptation. For example, async pre-processing can feed EventInjectionPolicy.Ingest from a gocurrent component's output channel. We settled the pushdown position on 2026-07-16. At some point a second consumer of the synchronous stages may appear. Then the stages graduate INTO gocurrent as a new surface, never replacing the asynchronous one, and the agent module re-imports them. They get a new name there, because gocurrent.Pipeline already means the channel component. Until then they live here, to avoid cross-repo release ordering for a single consumer.

### Async control plane

Events and tasks are asynchronous operations. Two primitives let the model steer them through conversation rather than only through config.

- `ProviderRequest.ToolChoice` biases tool calling for a request. Its values are auto, required, none, or a forced tool. `RunnerConfig.Selector` narrows the offered set. Together the two can steer a proactive or injected turn toward *acting*. For example, a trigger that fires "send an email" can offer only the email tool and force a call. `ToolChoice` renders to the OpenAI-compatible `tool_choice`, and a server that ignores it degrades to auto.
- `TriggerPolicy.Add` / `Remove` / `Bindings` mutate bindings at runtime. A `create_trigger` host meta-tool uses this seam, so the model can install a standing behavior mid-conversation, such as "when users are created, send an email". Task completions are already `task.completed` events (see background tasks). So the same runtime binding serves "notify on build done" and "email on user_created" alike.

Host meta-tools such as `subscribe_events`, `create_trigger`, `list_tasks` and `cancel_task` expose these to the model. They are surface-level `FuncSource` registrations, not agent API. The model's control plane is host-local tools over the client mechanisms and these agent policies.

### Policy hooks

Two seams, both no-op by default:

- The **injection policy** consumes events from connected servers through the experimental events extension. It decides what enters model context and how, using priority under a context budget, aggregation windows, template rendering, and sensitivity gating (issue 893).
- The **trigger policy** holds declarative bindings. Each binding names an event, a filter and an instruction template, and starts a Runner turn without a user message. Rate-limit slots, consent, and budget caps mediate those turns (issue 894).

The permission gate (`TieredApproval`) is a third. It runs last among the tool middleware, so every other entry has already rewritten the arguments by the time the gate decides. Its ask is yes or no (`AskFunc`), with one way to answer more finely. A `ChooseFunc` can claim a call. It then presents its own form and may approve part of the call, and the gate runs the narrowed call. We settled on that shape for per-hunk approval of edits (chakra#39) over three others. Widening `AskFunc` to return a modified call would have changed every implementer of a signature due to freeze at 1.0. Letting an `ApprovalRenderer` decide would have mixed "describe this call" with "change this call". Asking inside the tool would have put an interactive prompt in a tool implementation. When a chooser claims a call, it replaces the yes/no ask, so the user is asked once. It can change only the arguments, never the tool or call ID. A partial approval is never remembered. A `Choice.Note` goes back with the tool result, so the model can tell the user what was left out.

## Surfaces: how UIs consume the module

Three interface families build on the module: CLIs, web frontends, and desktop/native apps. They reduce to two consumption modes, and the module defines one canonical contract that serves both:

| Surface | Mode | What it uses |
|---|---|---|
| CLI (`surfaces/chat`) | in-process | Import the module, subscribe to the event stream, implement the elicitation callback in-terminal |
| Web | wire | A host application embeds the module and maps the event stream 1:1 onto WebSocket or SSE; user input, cancel, and elicitation responses come back as requests |
| Desktop/native | either | Go-native shells (Wails, Fyne) consume in-process like the CLI; webview shells consume the wire like web |

The in-process contract is the canonical one. It consists of the Runner input API (submit turn, cancel, history access), the typed event stream, and the elicitation callback. The wire is a projection of it, never a second design.

Two rules keep that projection one-to-one:

1. **Every Runner event type is wire-serializable from day one.** JSON tags, a stable `kind` discriminator, no Go-only payloads (channels, funcs, interfaces without concrete marshaling) in event fields. Enforced by a round-trip test (see `CONSTRAINTS.md`).
2. **The module leaves out the wire layer until the wire layer has two consumers.** The first web host builds the WebSocket mapping in its own package. When a second application needs the mapping, it moves as-is into `` or a sibling package. This mirrors the way the fire-and-forget-then-subscribe chat shape should work. Submit returns an id immediately, and the event stream carries the turn.

## Elicitation: one UI seam, two protocol inlets

A surface implements exactly one callback:

```go
type ElicitationUI func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error)
```

`ElicitationCoordinator` wraps it with strict-FIFO serialization. It presents one request at a time per user session, so parallel tool calls and multiple servers never stack dialogs. When a waiter's ctx ends, the waiter leaves the queue cleanly. The client already unifies the wires, so registration happens at a single point. `client.WithElicitationHandler(coord.Handler())` covers legacy-wire server-initiated elicitation. When `client.DefaultInputHandler(c)` is the ClientSource input handler, it routes stateless-wire and task input_required rounds through the same registered handler.

**So the agent does not care whether a server is stateful, stateless, or legacy.** The client layer absorbs wire differences. Per-server wire mode (client.WithClientMode) is host configuration, not agent code. Decline and cancel are results, not errors. URL-mode requests pass through the same UI when the client opts in via WithElicitationURLSupport.

## Vendor `_meta` prefix

All vendor-namespaced `_meta` keys emitted or consumed by this module use the prefix:

```
io.github.panyam.mcpkit/
```

MCP reserves `io.modelcontextprotocol/` for spec-defined fields, and vendor prefixes should be reverse-DNS of something the vendor controls. `panyam.github.io` qualifies. The first planned keys are `io.github.panyam.mcpkit/context-hint`, for injection hints on event definitions, and `io.github.panyam.mcpkit/trigger`, for trigger bindings. If we ever own a shorter domain, the rename is a one-time migration with a reading shim. That cost is acceptable while the keys are pre-standardization.

## Testing strategy

Testing is built on the `StubProvider`. Its scripted turns of text, tool-call sequences and errors make the Runner fully deterministic in CI with no network and no model. Unit tests run the loop behaviors against it, covering multi-step, parallel dispatch, cancellation, step caps, and error recovery. Integration tests drive agentchat against in-repo example servers (skills, tasks-v2, auth) behind a build tag or make target, mirroring how conformance suites run today. The module's sanity test also guards the sub-module wiring itself, meaning the replace directive and go.sum drift. Broken wiring is the documented failure mode for mcpkit sub-modules.
