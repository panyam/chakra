# web — Connect bridge over the agent host

`agent/host.App` is the surface-agnostic host, and the terminal `cmd/agentchat`
drives it. `web` exposes the same `App` over a Connect RPC bridge with a live
event stream, so a browser can be another surface on the same running agent.
`web` is a thin projection. It holds only an `*App` and maps each RPC to an App
method. No web or proto type leaks back into `host` (agent/CONSTRAINTS A4/A6).

This is E3 of the web-UI epic (issue 1196, epic 1193). E4 (issue 1197) adds a
DockView + Solid frontend, and that frontend consumes this bridge. "Frontend"
below describes it.

## Surface

The proto is `chakra.web.v1.HostService` in `protos/`, with generated Go +
Connect under `gen/go/`.

| RPC | App method | Shape |
|-----|-----------|-------|
| `Watch` (server stream) | `App.Subscribe` | the retained event log replayed from offset 0, then live |
| `Submit` | `App.RunTurn` | run one turn |
| `Dispatch` | `App.Dispatch` | run a slash command → `CmdResult` |
| `RespondToAsk` | `App.RespondToAsk` | answer a pending elicitation (first responder wins) |
| `ListSessions` | `App.SessionsPage` | one page of an App's persisted RunStore runs |
| `GetStatus` | `App.ModelLabel` / `App.RunID` | status-line read |
| `CreateSession` | `SessionManager.Create` | mint a fresh conversation (App) → its `session_id` |
| `ListWebSessions` | `SessionManager.List` | the roster of live conversations (App ids) |
| `CloseSession` | `SessionManager.Close` | close one conversation and drop it |
| `ResizeProcess` | `App.ResizeProcess` | fit a tty process's terminal to the Tools panel's terminal view (#218) |
| `WriteProcess` | `App.WriteProcess` | send what the user types in a terminal view to the process; not gated, not shown to the model |

### Multi-session (epic 1193)

One agentweb server hosts many concurrent conversations, each with its own
live `host.App`. Every request carries a `session_id` routing field on its
envelope, and that field selects which App handles the request. A
`SessionManager` in `sessions.go` owns the Apps, keyed by id. It builds new ones
on demand from a factory, which makes a fresh App from the server's config.
`HostService` routes each RPC through it, and an unknown `session_id` gets
`CodeNotFound`. An **empty** `session_id` resolves to a default session created
at startup. Some clients predate multi-session and send no `session_id`, and the
current frontend is one of them. Those clients keep working unchanged.

`session_id` is a routing field on the request, and never part of an event
payload. So the `Frame` is still event-flat (A2). `web.HandlerWithSessions(mgr)`
serves the multi-session mux. `web.Handler(app)` is still the single-App
shortcut. It wraps `app` as the default session with no factory, so
`CreateSession` is unavailable.

`ListSessions` and `ListWebSessions` are distinct. `ListSessions` pages an
App's persisted RunStore runs, which is history inside one conversation.
`ListWebSessions` lists the roster of concurrent conversations themselves.

A browser session-switcher UI (open / list / switch conversations) is a
follow-up; this slice is the backend + wire + tests.

### Frame envelope (A2)

`Watch` streams `Frame{kind, payload bytes}`, where
`payload = json.Marshal(HostEvent)`. The payload is the event's own A2 wire JSON,
a 1:1 projection. It is never a per-kind proto schema, because A2 forbids a
translation layer. The client decodes `payload` as JSON keyed by
`kind`. `Dispatch` uses the same `{kind, json}` shape for `CmdResult`.

The first `Watch` frame is a ready sentinel with an empty `kind`. The Connect
protocol flushes response headers on the first `Send`, so the sentinel opens the
stream promptly even on an idle session. Clients skip an empty-kind frame.

A few `HostEvent` kinds still carry live pointers, and `json.Marshal` can reject
them. They are `TaskStatus`, `Task`, and the `Failover` inside a command result,
and the already-filed issue 994 tracks them. Until they get serializable
snapshots, their frame carries a minimal `{kind}` payload so one un-serializable
event never stalls the stream.

## Serve

`web.Handler(app)` builds the mux. The mux serves HostService Connect handlers
under the proto service path, the frontend bundle under `/static/`, and the
server-rendered shell at `/`. The shell is `shell.go`, which uses stdlib
`html/template` with island holes + `data-layout`. `cmd/agentweb` is the thin
serve binary over the mux, the same way `cmd/agentchat` is thin over the host.

```
go run ./cmd/agentweb --addr :8090 --config path/to/host-config.json
```

agentweb has no terminal, so an approval ask waits until some surface answers
it through `RespondToAsk`, or until the turn is cancelled. A turn with no tab
open just waits at the ask (issue 121). Before that, the empty stdin declined
every ask on the spot.

The browser answers an ask from its `requestedSchema`, since the host reads
specific fields out of the answer. Approve on a plain approval sends
`confirm: true`, and a form with boolean fields (an edit's per-hunk approval)
shows one checkbox per field and sends each one's value. Until #124 Approve sent
no fields at all, which the host reads as a refusal. Fields that aren't boolean
aren't rendered yet. The answers are pinned by `testdata/asks/`, which both the
vitest and the Go tests read.

`Watch` streams over the Connect protocol, which supports server streams over
HTTP/1.1. So local dev needs no h2c.

The API only answers pages served from localhost, and requests with no `Origin`
only when their `Host` is localhost, so another site open in the browser cannot
drive the agent. To use agentweb from a phone on the LAN, allow the address the
phone opens:

```
go run ./cmd/agentweb --addr :8090 --allow-origin 192.168.1.5 --config ...
```

### An assistant on another page: `<chakra-chat>`

Say you have a web app of your own and want an assistant in a side panel. Load
the element from agentweb and drop it in, with no build step and no framework:

```html
<script type="module" src="http://localhost:8090/static/chakra-chat.js"></script>
<chakra-chat server-url="http://localhost:8090" profile="chat"></chakra-chat>
```

The element creates a session and streams it over `Watch`. It shows the
transcript, thinking, tool calls and pending asks, and it answers asks with
`RespondToAsk`. When it creates a session, it sets its own `session-id`
attribute and fires a `chakra-session` event with `{ sessionId }`. Put that id
back on the element after a reload, and the element resumes the session with
its history. Add `token` when agentweb runs with `--access-token`. It works the
same inside a React or Solid app, as a plain JSX element (#314).

The page runs on another origin, so the browser asks agentweb for CORS
permission first. agentweb grants it to the origins `--allow-origin` lists,
and to localhost on any port. The element bundles its own copy of Solid's
reactive core, to share the transcript logic with agentweb's page. It never
touches the page's own Solid or React. The bundle is about 140 KB minified.

### The coding agent at /code/

`--coding <dir>` also serves chakra's coding agent (`surfaces/coding`, #178), built from `dir`'s
`config.json` and `workspace.json`:

```
go run ./cmd/agentweb --config chat.json \
  --coding ../../examples/coding-harness --coding-fixture ../../examples/coding-harness/testdata/fixture
```

`/code/` is the same page, marked with the `coding` profile. When the browser has no coding session,
the page calls `CreateSession` with `profile: "coding"`, and the server builds one. Chat and coding
sessions share one `SessionManager`, so the Sessions panel lists both. Choosing a session of the
other kind opens its page. The origin check and the access token cover `/code/` too.

A coding session's id starts with `coding-`. `ListWebSessions` returns each session's profile and
page beside its id, and the page adds the sessions it hasn't seen to its Sessions panel, so a second
browser finds a coding session on the right page (#244). Sessions this browser closed stay out of
the list.

Each coding session works in `--coding-workspace` or in a fresh git copy of `--coding-fixture`.
Its file snapshots, and its fixture copy, go to a folder named by its session id under
`--coding-state` (default: `agentweb-coding` in the system temp dir), outside the workspace. With
`--session-store`, coding sessions persist like chat ones, and a config that sets `offload` keeps
offloaded tool results in the same backend. After a restart the next request
rebuilds the session on the same folder, so its fixture copy keeps the agent's edits. A session
whose folder is gone can't be resumed, and the page starts a fresh one. Without a store, closing a
session, or stopping the server, removes its folder.

`--coding-active`, `--coding-model` and `--coding-instructions` pick its starting connection, model
and instruction preset. The header's model picker lists the other connections in the coding
config, and switching takes effect on the next model call. `--coding` can't be
used with `--byok`, because the coding config holds the server's own keys. It's meant for local use.
Commands run with the full environment, and Linux has no exec sandbox yet (#32), so `/code/` is no
place for a hosted deployment.

Ctrl-C cancels every open `Watch` and running turn, waits up to 3s for handlers
to return, then closes what is left. There is no write timeout, because `Watch`
lasts as long as a tab is open and `Submit` as long as a turn.

## Deploy (App Engine)

`make deploy-web` from the repo root deploys to the `vibestitch` App Engine
project (`GCP_PROJECT=...` to change it), at
https://vibestitch.uc.r.appspot.com. It runs the web tests and rebuilds the
bundle. Then `deploy.sh` vendors the module, because the module's go.mod
replaces chakra and mcpkit with paths that don't exist once it's uploaded alone.
`deploy.sh` then adds the access token to a temporary copy of `app.yaml`,
deploys, and removes both. `make weblogs` tails the logs.

The first deploy writes a random access token to `surfaces/web/.agentweb-token`
(gitignored). Paste it into the Sessions panel once per browser.

`app.yaml` sets the following, each for the reason given.

- **One instance, basic scaling.** Sessions live in that instance's memory, so a
  second instance wouldn't have them, and basic scaling allows long requests.
- **`AGENTWEB_BYOK`** (`--byok`). The server holds no keys. The browser keeps
  its providers and keys in localStorage (the Sessions panel) and sends one key
  with the `CreateSession` that starts a session. The key then lives in that
  session's provider, in memory, until the session closes or the instance
  restarts. There's no default session, `ListWebSessions` is refused, and an
  empty `session_id` is NotFound. So a session is reachable only by its random
  id, and only the browser that created it has that id. The demo is one of the
  provider types, so the page is usable with no key at all.
- **`AGENTWEB_ACCESS_TOKEN`** (`--access-token`). Every API call needs it in
  `X-Agentweb-Token`. The origin check only stops other sites' pages; a script
  can send any Origin, so a public server needs this as well.
- **`AGENTWEB_WATCH_WINDOW=3s`.** App Engine standard buffers each response
  until it ends, so an open `Watch` never reaches the browser. With a window the
  server ends each `Watch` after 3s and the client reconnects, passing
  `resume_after` (the number of events it has applied) so the replay isn't
  applied twice. Replies arrive in batches up to a few seconds late. The
  Conversation panel shows a waiting cell from submit until the turn's done
  event arrives. Live streaming is #165.
- **`AGENTWEB_ALLOW_ORIGIN`.** This variable replaces `--allow-origin`, and
  `$PORT` replaces `--addr`.

Switching sessions reloads the page, so every panel and the Watch stream start
over against the new session. The transcript comes back whole, because the host
announces each turn's input as a `user-input` event.

## Frontend (E4, issue 1197)

The frontend lives in `web/` and follows the diffpp/Agni pattern. A
server-rendered shell has island "holes", and SolidJS islands mount into them.
DockView provides the dockable desktop layout. Connect-Web clients are generated
from the same proto. The frontend reads E3's `Watch` server-stream **directly**
through connect-web, with no massrelay. massrelay is a later follow-up.

This slice ships one **Conversation** panel. It streams the live turn off
`Watch`, has a prompt box that `Submit`s, and shows an approval prompt on
`HostElicitRequest` that it answers via `RespondToAsk`. The panel works in both a
DockView desktop layout and a mobile-overlay layout. The observability panels
are E5 (issue 1198). They extend the panel registry in `web/src/dock.ts` and
reconcile into existing saved layouts.

Layout map (`web/src/`):

| File | Role |
|------|------|
| `api.ts` | connect-web `HostService` client (same-origin transport) |
| `watch.ts` | `Watch` subscription: decode each `Frame` payload → `HostEvent`, reconnecting |
| `hostevent.ts` | wire types mirroring `agent/host/hostevent.go` |
| `conversation.ts` | shared store: folds `HostEvent`s, drives `Submit` / `RespondToAsk` |
| `asks.ts` | builds an ask's answer from its `requestedSchema`: the checkboxes, then the result |
| `ConversationPanel.tsx` | the Conversation Solid island (transcript, ask prompt, compose box) |
| `dock.ts` | framework-neutral panel registry + saved-layout persistence + reconcile |
| `DockviewWorkspace.ts` | dockview-core glue: adopt island hosts into panels |
| `MobileOverlays.tsx` | phone layout: the chat as the hero, a bottom bar (Chat, Agents, Activity, Tools) and a More drawer (Memory, Budget) whose panels open over the tab with a back bar. Every panel remains mounted, only hidden |
| `mobilenav.ts` | the phone layout's navigation state and unread dots, with no DOM, so it's unit-tested |
| `StatusLine.tsx`, `status.ts` | the topbar status island (model and session), and the model picker that runs `/provider` when the session has connections to choose from |
| `page.ts` | tsappkit `IslandPage`: the island registry and the shared stores and `Watch` |
| `main.ts` | boot: starts the page once the DOM is ready |
| `settings.ts` | what the browser keeps in localStorage: access token, providers and their keys, its sessions |
| `sessions.ts` | the session lifecycle: create with a provider, switch, close, and why there's nothing to show |
| `SessionsPanel.tsx` | the Sessions panel over it (in the dock, and first in the phone layout's More drawer) |

`shell.go` writes a page spec into `#page-spec`. The spec names the islands for
the chosen layout and their `[data-slot]`s, which are `status` plus `dock` or
`mobile`. The registry in `page.ts` builds each island. The dock is a lazy
entry, so dockview-core loads only on the desktop layout. The dock's own panels
are still hand-mounted `SolidIsland`s. Dockview builds them in detached hosts,
not in slots, so the spec can't name them. Dockview 8 ships no CSS file, so
`build.mjs` extracts the stylesheet from its UMD build into
`static/dockview.css`.

### Run it (offline demo)

```
./run.sh          # builds the bundle, serves --demo on :8090
```

`--demo` wires in an offline, inexhaustible streaming provider, so it needs no
model or MCP server. Open `http://localhost:8090/` and send a message, and a live
turn streams into the Conversation panel. `?layout=mobile` (or the topbar Layout
select) switches to the phone layout. With no `?layout=`, a phone's user agent
gets it by default.

### Build / test the frontend

```
cd web
npm install
npm run gen      # regenerate TS clients from ../protos (buf)
npm run build    # esbuild bundle → ../static (committed; Go embeds it)
npm run typecheck
npm run test     # vitest: Frame-decode, event fold, dock reconcile, status line
npm run e2e      # Playwright: drives the served page (needs Go; make test-web-e2e)
```

The Playwright checks in `web/e2e` run against the committed bundle in
`static/`, so rebuild it before running them after a frontend change.
`internal/e2eserver` serves the page over two stub connections, and
Playwright starts it with `go run`.

The built bundle is committed under `static/`, so `go build` and
`just test-agent` need no Node step. Rebuild it after a frontend change.
Suppose a rebase or merge conflicts in `static/`. Don't pick a side. Take either
copy, run `npm ci` and `npm run build` on the merged sources, and stage the
result. A fresh worktree has no `node_modules`, so `npm ci` comes first. The
chunk names are content hashes, so a rebuild also renames chunks.

## Regenerating proto code

```
cd protos && buf generate          # Go + Connect (gen/go)
cd web && npm run gen              # TypeScript clients (web/src/gen)
```
