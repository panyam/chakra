# Agent server state: async, graceful-degrade connection

Status: design agreed, phased implementation. Root `CONSTRAINTS.md` **C6** decouples the
MCP server lifecycle from the agent. This doc establishes the async graceful-degrade half
of C6.

## Problem

`NewApp` today connects to every configured server synchronously and **fail-fast**. The
loop in `host/app.go` does `client.NewClient(sc.URL)` + `c.Connect()`, and any connect
error tears the App down and returns. So one server that is down, unreachable or needs auth
aborts the whole agent boot. The system prompt is also baked once (see below). So when a
server is absent at boot and comes up later, it has no path to integrate.

Every real MCP client (Claude Code, Cursor) boots regardless of individual server health and
shows each server as `connected` / `failed` / `needs-login`. That is the target.

### Why eager skills are the hard part

`runner.go` documents `RunnerConfig.Instructions` as "the system prompt sent on every
step". `NewApp` assembles it **once**, at construction. It takes `cfg.Instructions` and
appends each eager server's skill block. The Runner replays that same system prompt on
every step and never recomputes it. Picture a server whose eager skills belong in the
system prompt. It cannot integrate after boot unless the system prompt is produced
differently.

## Goals

- Boot even when some servers are down; usable immediately with whatever connected.
- Per-server state observable and surfaced (`/servers`, a status-line counter, live events).
- Tools / catalog skills / events register as connections reach `Ready`, de-register when they drop.
- Background reconnect with backoff; a returning server wires itself in with **no restart**.
- Per-server **`required`**: a user can make boot wait for an essential server.

## Non-goals (deferred)

- Host-managed server *spawning* (`ServerConfig.command` + stdio) — separate follow-up.
- Full interactive OAuth re-login flow — phase 3.

## Decisions (locked)

1. **Eager skills → dynamic system prompt.** `RunnerConfig` gains an `InstructionsFunc`,
   recomputed at the top of each turn. The host uses it to assemble `cfg.Instructions` +
   eager blocks from the currently-`Ready` servers. Eager skills remain in the top-level
   system prompt with full authority, and the prompt tracks the Ready-set. The alternative
   was to demote eager skills to an injected `RoleSystem` message. The dynamic system prompt
   won because it is a generally useful primitive, for a dynamic date or context and not
   only for skills.
2. **`client.Group` from the start.** The connection lifecycle covers async connect,
   per-server state and backoff reconnect. A script, a dashboard or `cmd/testclient` would
   want it too. The agent-vs-client layering rule puts it in the client layer, so it lives
   in `client/`, not the host. The host consumes it and owns only the registration
   *reaction*.
3. **Fail-fast escape is per-server `required`.** `required: true` blocks boot until that
   server is `Ready`. A timeout bounds the wait, and on timeout boot fails and lists the
   laggards. Non-required servers connect in the background and never block boot.

## Core model — per-server state machine

Each configured server gets a `serverConn`: `{id, cfg, client, state, lastErr, srcHandle, retry}`.

```
Disabled                          (config off)
Connecting → Ready                (connected + capabilities negotiated → register)
Connecting → Failed               (connect/handshake error; backoff → Connecting)
Connecting → NeedsLogin           (401/403; park, don't hammer)
Failed/NeedsLogin → Connecting    (Group.Reconnect: wake backoff / un-park post-login)
Ready      → Failed               (live connection dropped → de-register, retry)
```

**`Group.Reconnect(id)` forces a stuck member to retry now** (issue 1095). A `Failed`
member is looping in backoff, and Reconnect cuts the sleep short. A `NeedsLogin` member's
`run` loop **parks on a buffered wake channel** and does not return the goroutine. So when
the user logs in, a Reconnect resumes it with no relaunch race. `Reconnect` is a no-op for
`Ready`, since live-drop reconnect is the client's own concern. It is also a no-op for
`Connecting` and for unknown ids. The agentchat `/mcp` overlay's reconnect action routes
through `App.ReconnectServer` to `Reconnect`, and so does `/servers reconnect <name>`.

**Login re-runs the flow (issues 1116/907).** Some servers are configured with the
interactive authorization-code auth type (`"auth": {"type": "oauth"}`), and each of them
carries an `ext/auth.OAuthTokenSource`. When one hits `NeedsLogin`, the overlay's login
action calls `App.LoginServer(id)`, and so does `/servers login <name>`. `LoginServer`
drops the cached token (`OAuthTokenSource.Invalidate`) and then calls
`ReconnectServer(id)`. So the next connect re-runs the PKCE browser flow as a *fresh*
login. A plain reconnect differs, because it may reuse a cached token. Only oauth-typed
servers are loginable. The server status carries a host-layer `CanLogin` flag, so a
surface knows when to offer login.

Each transition emits a **`HostServerStateChanged`** event so surfaces update live. On
`→ Ready` the host calls `multi.Add(id, src)`, registers that server's catalog skills +
`load_skill`, and calls `startEventStreams` for it. In phase 2 it also recomputes the eager
system-prompt block.

**When a server drops mid-conversation (`→ Failed`), the host keeps the tool defs and
fails calls gracefully.** The tool set does not shrink mid-turn. The model still sees the
tools it knew about, so nothing surprises it. The next section covers calls made while the
server is down.

## Tool calls to a non-Ready server (SHIPPED, reactive)

Tool defs remain registered, so the model can call a tool on an unreachable server. The
implementation is **reactive**, not a proactive pre-call gate, and that is deliberate.
`client.Client` **auto-reconnects transparently** (`retryWithReconnect`) and exposes no
disconnect signal. So the host can't cleanly observe a live drop before a call. A blip
heals itself. Only a persistent failure surfaces, and it surfaces at the point that
matters, when a call is actually made.

- **Model-visible miss.** The host wraps each server's source in `availabilitySource`.
  Suppose a call returns a **transient (network-level) error**, meaning the client tried to
  reconnect and could not. The wrapper maps it to **`agent.ErrNotAvailableNow`**, with a
  message naming the server and tool ("tool `create_issue` is temporarily unavailable —
  server `atlassian` is not reachable right now; retry, use another approach, or tell the
  user"). The Runner recognizes the sentinel and emits a distinct
  **`EventToolUnavailable`**. The event carries `Reason`, NOT `Error`, so error-keyed eval
  scorers don't miscount. `EventToolDenied` and `EventToolCancelled` take the same care.
  The Runner then feeds the message back. The turn continues, and the model adapts.
  Approval-denial and cancellation belong to the same "non-fatal tool outcome →
  model-visible → continue" family. Two kinds of error are not transient and pass through
  unchanged. One is a tool that *ran* and failed (an `IsError` result). The other is a
  server-side JSON-RPC / 4xx error.

**Blocking the turn indefinitely (queue-and-wait) was rejected.** It couples turn latency
to reconnect timing, and it hangs on servers that may never come up.

**Two things are deferred, since nothing needs them yet.** One is a proactive health poll
that marks a server down *before* a call hits it, so `/servers` shows a drop eagerly. The
other is the bounded-wait-for-`Connecting` window. Both compose additively on top of the
reactive path. A `Group` health-poll option would feed the same status + retry, and
`availabilitySource` would not change.

## `client.Group` (client-layer primitive)

`client.Group` holds N clients and connects them concurrently. It exposes per-connection
state and a change stream, and runs backoff reconnect. Here is a sketch.

```go
type ConnState int // Connecting, Ready, Failed, NeedsLogin, Disabled

type Group struct { ... }

func NewGroup(opts ...GroupOption) *Group
func (g *Group) Add(id string, c *Client, required bool)
func (g *Group) Start(ctx context.Context)             // kick off async connects
func (g *Group) WaitRequired(ctx context.Context) error // block until all required are Ready (or timeout)
func (g *Group) State(id string) (ConnState, error)     // per-server snapshot
func (g *Group) Events() <-chan StateChange             // Ready/Failed/... transitions
func (g *Group) Close() error
```

`NewApp` blocks on `WaitRequired` for required servers. Optional servers keep connecting in
the background while the agent is already usable.

## Dynamic system prompt (`RunnerConfig.InstructionsFunc`)

```go
// InstructionsFunc, when set, is called at the top of each turn to compute the
// system prompt for that turn (overriding the static Instructions). It lets the
// system prompt track dynamic state — currently-Ready eager-skill servers, the
// date, injected context — instead of being frozen at construction.
InstructionsFunc func(context.Context) string
```

It is recomputed **per turn**, not per step. So the prompt is stable within a turn, and
the provider cache breaks only when the value actually changes. That happens when the
Ready-set changes, which is rare after startup. The host sets it to assemble
`cfg.Instructions` + the eager blocks of currently-`Ready` servers.

## Status surface

- **`/servers`** command → a `CmdServers` result (id, url, state, tool count, skills,
  lastErr), rendered like Claude Code's server list.
- The status line gains a `servers N/M` counter, built on the status-line machinery from
  the #1074 work.
- `HostServerStateChanged` drives live updates on both surfaces.

## Reconnect & auth

- Each `serverConn` retries with backoff when its server never connected or failed. The
  client already reconnects a *live* dropped connection, so initial-failure retry is the
  new part.
- `NeedsLogin` is terminal until the user acts. Surface it, and don't hammer it with
  retries. Phase 3 adds a `/login <server>` that drives the ext/auth OAuth flow.

## Phasing

1. **Async connect + state + `/servers` + graceful boot + per-server `required`.** SHIPPED
   (`client.Group`). It moves C6's noted target toward done.
2. **Seamless late integration.** SHIPPED. It added `RunnerConfig.InstructionsFunc`, and
   the host now recomputes eager/catalog blocks from the ready-set for skills. Events use a
   ready-observer subscription. A call to an unreachable server gets the reactive
   `ErrNotAvailableNow` miss. A returning server wires its tools/skills/events back in with
   no restart.
3. **Interactive re-auth** (`NeedsLogin` → `/login`). Not started.

Two additive items are deferred, and neither blocks anything. One is a proactive `Group`
health poll, for eager `/servers` drop status and bounded-wait-for-`Connecting`. The other
is `SystemPromptBuilder` (issue 1091).

## Resolved

- **`WaitRequired` times out after 30s by default.** `0` waits indefinitely, for the "hang
  until this server is up, however long it takes" case. A non-zero value fails and names
  the required servers that didn't reach `Ready` in time.
- **When a `Ready` server drops, it keeps its tool defs, and calls fail gracefully.** The
  tool set doesn't shrink mid-turn. The tool-call section above covers this.
