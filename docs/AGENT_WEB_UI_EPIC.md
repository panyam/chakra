# Epic — Web UI surface for the agent host (issue 1193)

Status: proposed. Owner: agents workstream. In `AGENT_SDK_ROADMAP.md` terms this is a
surface/observability epic, not an SDK-parity phase. It runs beside Phases 4–7, not inside them.

## Motivation

There are two reasons, and the second is stronger.

1. **A second live surface.** The host layer is `host`. It was built surface-agnostic on purpose,
   so a web chat could reuse all of it and swap only stdin/stdout for a socket. `surfaces/chat` is a
   thin terminal surface over it. A browser surface should be another thin surface over the same
   `App`, and a user should be able to view it at the same time as the TUI.

2. **Observability the TUI cannot linearize.** Much of what the agent does doesn't fit a linear
   scrollback. Examples are sub-agent trees and fan-out concurrency, memory injection vs recall vs
   compaction, tool-result offloading stubs and the blobs behind them, token and tree-budget
   consumption, upward signals and preempt grants, and team handoffs. A dockable-panel web UI can
   give each of these its own movable panel. That shows a running agent far better than scrollback
   can. Each of these already has a distinct `HostEvent` kind, so the panels map onto an existing
   taxonomy.

## Non-goals

- **Not a Grafana/OTel dashboard.** The agent already emits SEP-414 spans + metrics and ships a
  Grafana dashboard off Mimir/Tempo. That dashboard is ops-grade, aggregate and multi-session. This
  surface is live introspection of one session, for development and use. Draw the boundary
  explicitly.
- **Not a rewrite of the host.** No web framework or proto types leak into `host`. The same
  discipline keeps charm/lipgloss out of it. The web surface owns all of that.

## `gocurrent.Queue` as the per-session event log

`gocurrent.Queue[T]` is already an indirect dep. It is an append-only log with offset-based reads,
per-subscriber wake-up channels, N-consumer fan-out, late-subscriber replay from offset 0, and
`AppendBarrier`/`Resolve`/`AwaitResolution` barrier semantics. It fits this epic almost exactly.

| Design need | Queue mechanism |
|---|---|
| Stream host events to N surfaces (TUI + every browser tab) | `Append(frame)` + per-subscriber `Notify`/`ReadFrom`, each surface tracks its own offset |
| A tab joining a live session sees full history | late-subscriber replay from offset 0, no separate replay path |
| Multi-surface ask: all get it, first responder wins, others auto-dismiss | `AppendBarrier(ask)` blocks the coordinator; any surface `Resolve(offset, resp)`; first writer wins (`ErrAlreadyResolved`); the resolution is committed as a log entry, and every other surface reads it and retracts |

Today the host sends host→surface events through `Observer`, which fans out to renderers. It merges
the inbound MCP `events/stream` with `gocurrent.FanIn`. Nothing uses `Queue`. This epic puts a
per-session `Queue` at the center. `emit` records every event on it, so the Queue is the retained
source of truth. Local observers read it synchronously, and the web streamer subscribes to it
asynchronously.

### Do we still need `Observer`?

The Queue is the source of truth, and `Observer` is the synchronous local view of it. A pending ask
**cannot** be an `Observer`. `On(HostEvent)` returns nothing, and an ask needs a resolution channel
(`AppendBarrier`/`Resolve`). So asks go through the Queue regardless. Once events and asks both go
through it, the Queue is the one substrate. It provides retention, replay and the barrier, and a
remote surface subscribes to it.

Implementing E1 settled *how* local observers read it. The first cut gave every observer an async
drain goroutine, and that **races the terminal contract**. The built-in renderer writes to a
caller-visible `io.Writer`. Several existing `-race` tests read that buffer synchronously right
after an emit, and so does the plain REPL's own prompt ordering. Nothing needs the async isolation
yet. The only remote consumer is the web streamer, and it doesn't exist until E3. So in E1 `emit`
is still a **synchronous local fan-out**, with unchanged behavior and `emitMu` retained, and it
**also appends to the retained log**. A remote surface does want the async subscriber adapter, to
isolate it from backpressure, so the adapter ships with that surface in **E3**. The first cut kept
retention unbounded (Option A). The bounded window with deep replay from the `RunStore` **shipped**
in issue 1200, and the E1 retention note below describes it.

### Three wire categories

Naming these three makes the whole protocol clearer. Each one maps onto a Queue op.

- **Host events** are pushed fire-and-forget. They form the observability stream and map to
  `Append`.
- **Pending asks** are pushed too, but each one resolves as a correlated request and response
  (elicitation, approval). They map to `AppendBarrier` / `Resolve`. This keeps the earlier decision
  that "a prompt is request/response, not a fire-and-forget event". An ask is still
  request/response. It just has N delivery endpoints and a collector where the first answer wins.
- **Queries / commands** are pulled, as unary request/response. They cover `App.Dispatch` →
  `CmdResult` and the host data methods `SessionsPage`, `ServerTools`, `ListMemories` and
  `ServerStatus`. They aren't on the Queue, since each one is a point-to-point read of host state.

## Frontend stack (reuse Agni)

Agni's `web/` + `cmd/agni serve` pattern ports over almost whole.

- One `servicekit/http` listener serves a goapplib/templar server-rendered shell at `/`, an esbuild
  bundle under `/static/`, and Connect handlers under their proto service paths.
- `dockview-core` provides the dockable layout. Solid islands mount into server-rendered "holes"
  through the park-container adopt/dispose pattern. `@panyam/tsappkit-solid` provides the island
  shell.
- Connect-Web typed clients are generated from proto (buf) and use a same-origin transport.
- Agni's **saved-layout reconcile** comes over directly. SavedLayout stores the panel-id registry
  beside the dockview layout. A newly added panel appears in the menu without auto-opening, and a
  panel the user closed isn't reopened. An agent UI gains panels over time, and the reconcile
  handles that with no extra work.

Agni is request/response only, so the **live stream** is the one new piece over Agni. A Connect
server-streaming RPC (`Watch`) is the idiomatic fit, and the Queue subscription produces its
messages.

## Layering

`surfaces/web` is a new submodule with its own go.mod, and it imports `host`. It holds the goapplib
shell, the Connect bridge and the Solid/DockView frontend assets. The bridge maps the Queue
subscription to the `Watch` stream, `Dispatch` and queries to unary calls, and the barrier to
`RespondToAsk`. A thin `cmd/agentweb serve` runs over the submodule, the same way `surfaces/chat` is
thin over the host.

## Open design decisions (resolve during E1/E3, not now)

- **Frame envelope shape.** `HostEvent` and `CmdResult` are Go tagged unions that keep changing.
  The taxonomy keeps gaining kinds, so a full proto `oneof` over it would drift. Lean toward an
  envelope with a stable `kind` enum. Settled kinds get a structured payload, and the long tail gets
  a JSON/`Any` escape hatch. Decide by looking at the actual `HostEvent` surface.
- **Observer role (resolved in E1, see above).** The Queue is the one retained substrate. `emit`
  appends to it and delivers synchronously to local observers. The terminal renderer writes a
  caller-visible `io.Writer`, so async local delivery races callers. The async subscriber adapter
  for a remote surface ships in E3, where the isolation is actually needed.
- **Auth.** This is a local dev tool first. If the surface is ever exposed beyond localhost it
  needs auth. Auth is out of scope for the first cut, but note it.

## Sub-tickets (dependency-ordered)

### E1 (1194) — Session event log on `gocurrent.Queue` (`host`) — SHIPPED
Make a per-session `gocurrent.Queue[HostEvent]` the retained event substrate. `emit` appends every
event to it *and* still delivers synchronously to the local observers, with `emitMu` retained. So
the terminal rendering contract doesn't change. The E3 web surface subscribes to the log, replaying
from offset 0 and then following live, and E2 builds its barrier seam on it. The async subscriber
adapter moved to E3. "Do we still need Observer?" explains why async local delivery races the
terminal contract.
- Accept: a subscriber attached after events were emitted replays them from offset 0 and is notified of
  live ones; `emit` still delivers to local observers synchronously and in order; `just test-agent`
  green with `-race`; no TUI behavior change.

#### E1 retention follow-up (1200) — bounded event log + `RunStore` deep replay — SHIPPED
The first cut (Option A) was unbounded. The log is now bounded, on gocurrent v0.1.2's
`NewBoundedQueue`. Some turns are evicted from the log after they were persisted, and
`App.Subscribe` deep-replays those from the `RunStore`. So a subscriber that attaches after an
eviction still sees the full history.
- `Config.MaxEventLogRetention` sets the retention. 0 means unbounded, which is today's behavior
  for embedders, and a positive value caps the retained window. `NewApp` builds
  `NewBoundedQueue(n)` when it's set. agentchat `--max-event-log` defaults to a generous 100000, so
  a single turn never exceeds the window. A config value overrides that default, and
  `--max-event-log 0` opts back into unbounded.
- `App.Subscribe(ctx) <-chan HostEvent` stitches replay together from two sources, and they meet
  exactly at `persistedOffset`. Events before `persistedOffset` are in the store. The turn-end
  persist site advances `persistedOffset` once `AppendEvents` succeeds. The replay comes in three
  parts. (a) The run's persisted `agent.Event`s come from the `RunStore`, each wrapped as
  `HostEvent{Kind: HostRunnerEvent}`. They are the deep history, and the Queue may have evicted
  them. (b) The Queue tail from `persistedOffset` forward holds the unpersisted in-progress and
  recent events, which are still in the retained window. (c) Live events follow via `Notify`. The
  store covers `[0, persistedOffset)` and the drain starts AT `persistedOffset`, so there's no dup
  and no gap. Non-runner `HostEvent`s (server-state, skills) before `persistedOffset` are ephemeral
  and intentionally not replayed, because the store only holds the runner stream.
- **The snapshot is consistent across the persist race.** The snapshot has three steps: the Queue
  `Subscribe`, reading `persistedOffset`, and reading the `RunStore`. All three run under `turnMu`.
  The persist site also holds `turnMu` while it advances `persistedOffset` and writes
  `AppendEvents` together. So a turn-end persist can never happen in the middle of the snapshot,
  and `RunStore` contents and `persistedOffset` are always read as one consistent pair. A two-step
  read would race into a dup, with the store ahead of the offset, or into a gap, with the offset
  ahead of the store. The pair read rules out both. The subscribe happens before `turnMu` is
  released, so no live `Append` is missed. Delivery then happens off-lock in a goroutine, so a slow
  subscriber never blocks a turn. `Resume`, `Fork` and `AttachRun` already use the same
  store-under-`turnMu` shape.
- Accept: with a small retention the log evicts and caps; a subscriber attaching after eviction (with a
  `RunStore`) replays the full conversation with no dup and no missing turn; without a `RunStore` it
  gets the retained window then live; `persistedOffset` advances at turn end; `just test-agent` green
  with `-race`.

### E2 (1195) — Pending-ask barrier (`host`) — SHIPPED
The coordinator drives the local `ElicitationUI`, and `barrierElicit` wraps that UI. It lives in
`host/ask.go`. It emits a `HostElicitRequest{Elicit}`, which the log appends at offset `off`. It
runs the local UI as one responder and races it against `RespondToAsk(off, result, by)` from any
other surface. **Resolution uses the event log's own barrier.** Local and remote responders both
call `eventLog.Resolve(off, ...)`, so the first writer wins, and `RespondToAsk` gets
`ErrAlreadyResolved` / `ErrOffsetOutOfRange` with no extra code. The loser's context is cancelled.
A `HostElicitResolved{AskID: off, By}` tells every surface to retract. With no other surface
attached the local UI always wins, so single-surface behavior is unchanged.

**The ask's id is its log offset**, not a minted id. A surface reads that offset from its `Watch`
frame, so the request event needs no id field. A separate pending-ask registry would be more
complex, and the offset reuses the log's tested first-writer-wins barrier. A late-joining surface
can even call `eventLog.Resolution(off)` to see that an ask was already answered. There are two
small costs. `AwaitResolution` is not ctx-cancellable, so a cancelled turn resolves the ask itself
to unblock the awaiter. And `emit` now returns the append offset.

**There is at most one pending input at a time.** The per-offset barrier could handle N concurrent
pending asks, but there is never more than one. The `ElicitationCoordinator` FIFO-serializes
*every* ask, elicitations and approvals alike, onto one UI through a single baton. `barrierElicit`
runs *inside* that baton. Picture a second ask, even one from a parallel tool call in the same
turn. It blocks in the coordinator, and its `HostElicitRequest` isn't emitted until the first ask
resolves. So the log always reads `… req_A … resolved_A … req_B …`, never two live asks. The
consumer side never has a queue of pending inputs to reconcile. The FIFO stream also never blocks
consumers. They render the ask and keep draining, and only the producer's tool call blocks. Only
multi-user or multi-surface routing would create concurrent inputs, with different asks going to
different users. That case belongs to the issue-1157 mediator. The per-offset barrier already
handles it with no change. So keying resolution on the log offset is simpler now and also the right
bet later. The one-at-a-time guarantee comes from the coordinator. The barrier does not depend on
it.
- Accept: two responders on one ask, first wins, loser cancelled, resolved event names the winner;
  out-of-range / already-answered `RespondToAsk` returns the log's error; `just test-agent` green with
  `-race`.

### E3 (1196) — `surfaces/web` submodule + Connect bridge — SHIPPED
E3 adds a new submodule, `surfaces/web`, with its own go.mod and the module path
`github.com/panyam/mcpkit/surfaces/web`. It also adds a thin `cmd/agentweb` serve binary inside the
module. The binary is thin over the host in the same way `surfaces/chat` is.
**The transport is Connect + buf.** The proto `chakra.web.v1.HostService` lives in
`surfaces/web/protos/`, and its generated Go + Connect code is committed under
`surfaces/web/gen/go/`. The service has a server-streaming `Watch`, which drains the E1 log through
the new `App.Subscribe` seam. It has a unary `Submit` for a turn, `Dispatch` for a command
(→ `CmdResult`), and `RespondToAsk` for the E2 offset barrier. It also has two trivial queries,
`ListSessions` and `GetStatus`. `servicekit/http` serves the placeholder shell, `/static` and the
Connect handlers on one listener.

- **`App.Subscribe(ctx) <-chan HostEvent`** in `host/subscribe.go` is the async subscriber adapter.
  E1 deferred it to here. It replays the retained log from offset 0, then follows `Notify()`, on a
  drain goroutine scoped to ctx. A slow consumer cannot block `emit`. `emit` only `Append`s, which
  doesn't block, and fans out to local observers. The drain reads the retained log at its own pace.
  So back-pressure is confined to the one subscriber, and nothing is lost. Local observers are
  still synchronous, as before. `Subscribe` also stamps the **ask id** onto the delivered
  `HostElicitRequest` copy. The ask id is E2's event-log offset, and the stored entry is emitted
  without it. A remote surface reads the id off its Watch frame and answers via
  `RespondToAsk(offset, …)`. The frame's `ask_id` is that offset, cast at the RPC boundary.
- **The frame envelope is consistent with A2.** A `Frame{kind, payload bytes}` has
  `payload = json.Marshal(HostEvent)`. That makes it a 1:1 projection, with no per-kind proto
  schema to drift. `Dispatch` reuses the same `{kind, json}` shape for `CmdResult`. Some kinds carry
  pointers: `TaskStatus`, `Task`, and the failover inside a command result (issue 994). On a marshal
  failure they fall back to a minimal `{kind}` payload, so one un-serializable event never stalls
  the stream.
- **E3 deviates from the brief in three ways.** (1) `Watch` is an idiomatic **Connect
  server-streaming RPC**, not a `@panyam/massrelay` room. massrelay is the E4 *frontend*
  consumption transport, with WebSocket fan-out as in diffpp. Its `AppMessage{Kind, Payload}` shape
  is exactly this `Frame`, so E4 bridges Frame → room 1:1. The server side is still a plain Connect
  stream, so a Go Connect client can test the whole bridge in-process, with no WebSocket and no
  browser. (2) `Watch` sends an empty-`kind` ready sentinel as its first frame. The Connect
  protocol flushes response headers on the first `Send`. Without the sentinel, a client attaching
  to an idle session would block on `Watch` until the first event. The sentinel opens the stream
  promptly. Clients skip an empty-kind frame. (3) The shell is a minimal inline HTML page, not a
  templar template. The real templar/DockView shell is E4.
- Accept (met): a Connect client `Watch`es a live session and `Submit`s a turn. A local Observer and
  a Watch client on the same `App` see the same stream, including replay-from-offset-0. While the
  local UI blocks on an ask, a `RespondToAsk` over the wire wins it.
  `surfaces/web/host_bridge_test.go` covers these and runs with `-race`. The serve binary serves
  the shell, `/static`, and the Connect endpoints. `GetStatus` returns the model label, and
  `ListSessions` returns `failed_precondition` when there's no RunStore.

### E4 (1197) — Frontend shell (DockView + Solid islands) — SHIPPED
E4 follows **`~/projects/diffpp/main/web`**. It uses `dockview-core` v4, `tsappkit-solid` islands,
Connect-Web clients generated from the E3 proto, and **`MobileOverlays` for the mobile mode**,
which switches between dockview and mobile. Saved-layout reconcile comes over too. The frontend
lives in `surfaces/web/web/`. The built esbuild bundle is committed under `surfaces/web/static/`.
Go embeds it, so `just test-agent` needs no Node step.

**The plan changed, and massrelay is deferred.** The browser reads the E3 Connect `Watch`
server-stream **directly** through connect-web. `web/src/watch.ts` decodes each `Frame` payload to
a `HostEvent` and reconnects with backoff. Replay from offset 0 makes a re-subscription safe. A
shared multi-tab room would bring massrelay back, as a later follow-up. The single-surface live
stream doesn't need it.

E4 shipped these pieces. A server-rendered shell (`shell.go`) replaces the placeholder shell. It
uses stdlib `html/template` with island holes + `data-layout`, and takes no goapplib/templar dep for
one static page. One **Conversation** panel streams the live turn off `Watch`. It has a prompt box
that `Submit`s, and an approval prompt on `HostElicitRequest` that it answers via
`RespondToAsk(AskID, …)`. A framework-neutral panel registry + saved-layout reconcile
(`web/src/dock.ts`) is ready for E5 to add panels. The DockView desktop layout and the
mobile-overlay layout share one store. A `--demo` mode on `cmd/agentweb` (an offline streaming
provider) and `surfaces/web/run.sh` give a runnable proof. Unit tests (vitest) cover
Frame-decode/dispatch, the event fold, and the dock reconcile.
- Accept (met): the browser renders live turns from the same `App`; the layout persists across reload
  (localStorage); the mobile mode presents the same panel as an overlay.

### E5 (1198) — Observability panels — SHIPPED
E5 adds five Solid islands. Each one is a projection of the one Watch/`HostEvent` stream. They join
the DockView registry (`web/src/dock.ts`) through the saved-layout reconcile, so they appear in the
Panels menu.
- The **sub-agent tree** assembles `HostSubAgentEvent` scope/depth into a nested pre-order tree,
  with per-node status + tool-call count (`web/src/subagents.ts` + `SubAgentPanel.tsx`).
- The **activity timeline** shows the whole `HostEvent` stream as a bounded ledger that can be
  filtered by kind (`timeline.ts` + `TimelinePanel.tsx`).
- The **memory inspector** shows compaction events off the runner stream, plus an on-demand
  `Dispatch("/memory")` read (`memory.ts` + `MemoryPanel.tsx`). Recall and injection are transient
  pre-turn transforms with no event, so compaction is the event-driven half.
- The **tool-call & offload inspector** matches the tool lifecycle begin to end by call id, and
  shows an offload stub's ref (`tools.ts` + `ToolsPanel.tsx`). The blob itself is fetched
  internally via `read_tool_result`, and the web bridge doesn't expose it.
- The **budget / token gauges** accumulate per-turn provider usage + steps across turns, with an
  input/output split bar (`budget.ts` + `BudgetPanel.tsx`).

One `WatchStream` now feeds a `PanelStores` bundle (`stores.ts`). Its `ingest` fans each event out
to every projection. The mobile overlay gains one launcher tile per panel. New panels register in
`DOCK_PANELS`, so they show in the menu and in a fresh default layout. They are gated out of
`RECONCILE_AUTO_OPEN`, so they don't crowd a user's saved #1197 arrangement. Vitest covers the
trickier projections: sub-agent tree assembly, timeline reduce, tool matching and offload
detection, and the budget fold. The `--demo` provider now delegates to two personas, so a single
demo turn populates every panel.
- Accept: during a demo (or `kitchen-sink`) run each panel reflects live agent state; adding a panel does
  not re-crowd existing users' saved layouts (reconcile). ✓

### E6 (1199) — Multi-surface elicitation UX + symmetric submission — SHIPPED
Wire E2's barrier end to end across web (`RespondToAsk`) and TUI. A real elicitation or approval
then goes out to all surfaces. The first answer wins, and the others dismiss with "answered by
<surface>". Any surface can submit a turn, since turnMu already serializes turns, and every surface
watches it stream.

On the web side, `web/src/conversation.ts` projects the one Watch stream.
- **Retraction with a receipt.** A `HostElicitResolved{AskID, By}` retracts the shown prompt. If
  another surface answered, it also shows a receipt. The terminal responder resolves as `local`,
  which shows `answered on terminal`. A peer web surface resolves as `web`, which shows
  `answered in another browser tab`. A self-answer just retracts, with no receipt. Suppose this tab
  clicked, but another surface won first. The reducer keeps `activeAskId` after an optimistic
  retract and tracks the offsets this tab answered, so it still shows the correct receipt. `By`
  names the winner, not the clicker.
- **Symmetric submission.** Picture a `turn-begin` arriving with no local submit pending. That turn
  came from elsewhere, and the transcript tags it (`· from another surface`) on the streaming bubble
  and on the committed turn. Local turns are untagged. The origin badge is best-effort. Two surfaces
  submitting in the same instant can mis-attribute one cosmetic badge. Serializing control is the
  ask barrier's job, not the badge's.
- Vitest covers both reducers (`conversation.test.ts`): request → resolved-by-other → retracted +
  receipt; the local-answer path; the this-tab-clicked-but-terminal-won race; stale-receipt clearing;
  and the remote-vs-local origin tagging.
- `--demo` gates the `researcher` delegate behind an approval `ask`. It also stands in a scripted
  terminal responder, `demoElicitUI`, which auto-accepts after 8s. So a single browser shows the
  prompt and the cross-surface "answered on terminal" retraction, with no second surface to script.
- Accept: an approval-ladder prompt answered in the browser dismisses the TUI prompt and vice versa;
  two near-simultaneous turn submissions serialize cleanly and both surfaces see both turns.

## Suggested slice order

E1 → E2 → E3 → E4 gives one end-to-end proof, with the TUI and a browser on one session and real
multi-surface asks. E5 then builds out the panels incrementally. E6 polishes multi-surface
elicitation. It can merge alongside E4/E5 once E2's barrier exists.
