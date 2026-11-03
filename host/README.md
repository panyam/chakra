# host

The reusable host application core for the chakra agent SDK. It takes the
pieces of `chakra` (Provider, Runner, ToolSource, elicitation, injection/trigger
policies) and assembles them into a runnable host. It commits to no particular
UI. A terminal CLI and a future web-chat surface both build on it, and they
differ only in the reader/writer you hand it (stdin/stdout versus a socket).

It is a nested Go module (`github.com/panyam/chakra/host`) under `chakra`. Its
dependencies are heavier (ext/auth, ext/skills, ext/tasks, events client,
gocurrent), and nesting keeps them out of the lean `chakra` module.

## What lives here

- **Config loading** (`config.go`): `Config`, `LoadConfig`, and the provider /
  server / auth / event / trigger sub-configs. This is the bulk of the package.
  It covers where and how to load providers, servers (with per-server auth),
  tool allowlists, skills, events, and triggers.
- **The App** (`app.go`): `NewApp(cfg, out, in, opts...)` builds the Runner, the
  aggregated tool sources, the FanIn event consumption loop, the injection and
  trigger policies, and background-task bookkeeping. `RunTurn` drives one turn;
  `REPL` drives an interactive loop; `Close` tears down.
- **Meta-tools** (`metatools.go`): host-local tools that let the model manage
  its own async work (`subscribe_events`, `create_trigger`, `list_tasks`,
  `cancel_task`, and friends), installed under source id `host`.
- **Surface seams**: terminal elicitation UI (`elicit.go`) and rendering
  (`render.go`). Swap these for a different surface.
- **Subscriptions and background tasks** (`subscription.go`, `tasks_bg.go`):
  event-stream lifecycle and detach-after-grace task execution.

## Options

- `WithProvider(p)` — supply the LLM Provider (e.g. `StubProvider` for tests,
  `OpenAIProvider` for a real model).
- `WithTracerProvider(tp)` — SEP-414 tracing.
- `WithLogger(l)` — structured logging.
- `WithRunStore(store)` — session persistence. When a turn completes, its
  messages and event stream are appended to a run in the store.
  `agent.NewInMemoryRunStore` serves in-process resume/fork. For sessions that
  survive a restart, use `store/redis` or `store/gorm` (Postgres, or a
  serverless SQLite file). `App.AttachRun` names or resumes a session at
  startup; `App.Resume` and `App.Fork` switch runs mid-session (`/resume <id>`,
  `/fork [id]`, `/session` in the REPL). A failed or cancelled turn persists
  nothing. If persisting fails, the host renders a warning and the turn still
  succeeds.
- `WithToolResultStore(store)` — backing store for tool-result offloading
  (`Config.Offload`). A result over the threshold is stored out of band, and
  the model gets a compact stub plus a `read_tool_result` tool. If you omit the
  option, offloading uses an in-memory store. Pass a durable store if blobs
  should survive restarts. The durable options are `store/redis`,
  `store/gorm`, and the dependency-free `agent.NewFileToolResultStore(dir)`.
  The file store needs no server, and its blobs are files the agent can also
  read directly. `Config.Offload` sets the byte threshold, preview length, and
  per-tool overrides, and a nil `Config.Offload` turns offloading off.

## Approval prompts

Before a gated tool call runs, the host renders a question for the user. By
default it reads:

```
Allow tool call "edit_file" with {"path":"main.go","edits":[{"old":"func Old…?
```

That format suits a tool whose arguments are an opaque payload. It is wrong for
a tool whose arguments *are* the thing under review. The default trims the
arguments to 200 characters. A real edit doesn't fit in that, so the user can't
review it and is answering a question they cannot evaluate.

An extension supplies its own rendering for the tools it owns:

```go
func (e *Extension) ApprovalRenderers() []host.ApprovalRenderer {
    return []host.ApprovalRenderer{func(ctx context.Context, info agent.ToolCallInfo) (string, bool) {
        if info.Call.Name != "edit_file" {
            return "", false          // not mine — next renderer, then the default
        }
        return renderDiff(info), true
    }}
}
```

The host consults renderers in registration order, and the first claim wins.
If no renderer claims the call, the host falls back to the built-in format, so
a renderer only has to handle the tools it knows. The renderer now decides
whether to truncate, and moving that decision out of the host was the point.

`ext/files` is the worked example. It renders an edit as a diff and a
whole-file write as a capped preview.

The `info` carries the call **as it will execute**. Every middleware has
already rewritten the arguments by then, so the user approves exactly the call
that runs. If a renderer returns an empty string, the host treats it as having
declined, because an empty prompt would ask the user to confirm nothing.

### Approving part of a call

A renderer changes the question, but the answer is still yes or no. Some calls
have parts worth judging separately, such as the hunks of an edit. For those,
an extension can supply an `ApprovalChooser` instead. When a chooser claims a
call, it replaces the yes/no ask. The host hands the chooser a `present` UI,
which is the same serialized elicitation seam, so the chooser's form reaches
every surface. The chooser shows its own form through `present` and returns an
`agent.Choice`. The choice says whether to run the call and with what narrowed
arguments, and it carries a note for the model about what was left out. The
host consults choosers in registration order, the first claim wins, and the
plain ask is the fallback.

Some surfaces only show accept and decline. Such a surface answers the form
with no fields, and a chooser should read that as approving the whole call.

## Sampling requests from servers

A connected MCP server can ask the host's model for a completion
(`sampling/createMessage`). The host only advertises that it can when the config
turns it on:

```json
"sampling": {"enabled": true, "connection": "openai", "maxTokens": 1000, "reviewResponse": false}
```

Unless the approval mode is `allow`, the host puts each request to the user
through the same queue as approval asks. The ask names the server and shows the
request's system prompt and messages. A headless `agentchat exec` answers no. A
declined request comes back to the server as an error and never reaches the
model.

- `connection` picks the model connection. Empty uses the session's chat
  provider. The request's model preferences are hints and don't choose it.
- `maxTokens` caps the reply, whatever the request asks for.
- `reviewResponse` shows the reply before it goes back, so nothing the model
  wrote reaches a server unseen.
- Only text content is supported; a message with an image or audio is refused
  with an error naming it.

Each answered request posts a line with its token use and the session's running
total. The request isn't charged to a turn's tree budget. It arrives on the
server's connection, outside any turn, so the host has no turn to attribute it
to.

Sampling is marked deprecated by SEP-2577 in mcpkit (removal deferred), so this
covers servers that still use it.

## Input transforms

An extension can rewrite what the user typed before the turn is built. For
example, a transform could expand `@main.go` to the file's contents:

```go
func (e *Extension) InputTransforms() []host.InputTransform {
    return []host.InputTransform{func(ctx context.Context, input string) (string, error) {
        return expandFileRefs(ctx, input) // an error aborts the turn
    }}
}
```

Transforms run in registration order, each over the previous one's output, so
when two extensions both claim `@` the earlier one sees it first. The expanded
text becomes the user message. History keeps it, the session store persists it,
and the model reads it. An error fails the turn before anything is touched. No
history entry is written, no run is created and no model is called. The user
asked for that reference and didn't get it, so the turn stops.

Surfaces still show what the user typed rather than the expansion (#106).

## Skills trust model

Skills are data, not code (`ext/skills` is enforced no-exec). So a skills
server can poison the context, but it can't cause side effects.
`ServerConfig.SkillsMode` sets how far the host trusts a skills server.

The default is `"catalog"`. In that mode a skill enters context only when the
model calls `load_skill`. `load_skill` is an ordinary tool, so it goes through
the approval ladder. To confirm each activation, set
`Approval.Rules["load_skill"] = "ask"`, and the prompt names the requested
skill. The fetched body goes into tool history rather than the system prompt,
so it has less authority.

`"eager"` splices full skill bodies into the system prompt at connect time with
no per-skill gate. SEP-2640 says a host MUST NOT fetch a skill ahead of need.
Eager mode accepts breaking that rule, so it is opt-in, for servers you trust.

`Approval.Remember` remembers a `load_skill` approval for that skill's exact
files. When you approve, the server has listed a uri and digest for each file,
and the remembered approval covers exactly that list. When the server changes a
file it announces `resources/list_changed`, the host re-reads its skills, and
the next load asks again. If a skill's files are `"dynamic"`, the host asks
about it every time. See the `SkillsMode` godoc in `config.go`.

### Skills from local folders

`Config.SkillDirs` (or `WithSkillDirs`) adds folders of skills to the same
catalog. Each skill sits at `<folder>/<name>/SKILL.md`. Each folder has a
label, and the label acts as a server id. It names the source in the catalog,
and it goes in `load_skill`'s `server` argument when two sources share a skill
name. It can't equal a configured server's id.

- If a skill's frontmatter is malformed (no description, or a name that isn't
  hyphen-case or doesn't match its folder), the host skips it with a
  `skill-skipped` event and loads the rest of the folder. A folder that doesn't
  exist is skipped quietly.
- Every file in a skill is listed with a `sha256:` digest, as a server would
  list it, so a remembered approval covers exactly those files. An edited
  SKILL.md is refused when loaded, the folder is re-read, and the next load
  asks again.
- A folder marked `Own` is introduced as the user's own. Any other folder is
  repository content, followed for work there and ranked below the host's
  instructions.
- `allowed-tools` frontmatter grants tools for the rest of the turn once the
  skill loads (#187). `Bash(git tag *)` grants `shell` calls whose argv starts
  `git tag`. `Read`, `Write`, `Edit`, `Glob` and `Grep` grant chakra's file
  tools, and a chakra tool name grants that tool. Other entries, path-scoped
  ones included, are shown in the ask and ignored.
- A project or server skill grants only after a person approves the load. The
  ask lists the grants, and the load asks even where `load_skill` would
  otherwise run unasked. A skill from an `Own` folder grants whenever it loads.
  An approval covers the skill's exact files, so an edited skill asks again.
- A grant only replaces an ask. A deny rule, a forbidden command and an
  escalation out of the sandbox still win. Under `allow` mode a grant changes
  nothing.

Surfaces fill these in from the workspace; see `surfaces.WorkspaceSkillDirs`.

### Workspace roots

`Config.Roots` (or `WithRoots`) lists directories, and the host declares them
to MCP servers as roots. With at least one root, every client advertises the
`roots` capability and answers `roots/list` with `file://` URIs. A server can
then find the project it works on, such as a language server behind MCP or a
repo tool. With no roots, the capability isn't advertised at all.

- On the stateless wire a server asks inside a tool call (SEP-2322 input), and
  the host answers like any other mid-call input. On the session wire it can
  also ask on its own after `notifications/roots/list_changed`.
- `App.SetRoots` swaps the list and sends that notification to every connected
  session-wire server. The stateless wire has no such notification, and its
  servers see the new list on their next ask. A host that started with no roots
  can't add them later, since capabilities are fixed at initialize.
- agentchat, agentweb and the coding harness confine `ext/files` and `ext/exec`
  to a set of roots, and they pass those same roots here.

### Servers over stdio

Some servers ship only as a stdio binary. Configure one with a `command`
instead of a `url`, in the `.mcp.json` shape:

```json
{"id": "repo", "command": "npx", "args": ["-y", "some-mcp-server"],
 "envPass": ["GITHUB_TOKEN"], "env": {"LOG_LEVEL": "warn"},
 "startupTimeoutSec": 20, "callTimeoutSec": 60}
```

It gets everything a URL server does (state in `/servers`, tools, skills,
instructions, events, roots), with a few differences.

- The process starts with an empty environment rather than the agent's. It
  gets `PATH` and `HOME`, the `envPass` names that the agent has set, and
  `env`. A server sees no credential unless it was given one.
- mcpkit's stdio transport owns the process (A12's sanctioned exception). It
  starts on connect and ends when the App closes, with SIGTERM and then SIGKILL.
- If the server crashes, the call in flight fails as not available now, and the
  model can retry or route around it. That call isn't re-sent, since it may
  have run already. The next call starts a fresh process.
- `startupTimeoutSec` bounds the start and `initialize`. A server that never
  answers fails and retries in the background, and boot goes on without it.
  `callTimeoutSec` works on URL servers too.
- Its stderr goes to the host logger a line at a time, tagged with the id.
- A crash shows up in `/servers` only once a reconnect fails. mcpkit's
  connection group doesn't notice a live drop yet.

A server can return `instructions` in its initialize result. They go into the
system prompt too, in their own section between `Config.Instructions` and the
skill blocks, in config order. Each section has the same untrusted-origin
header as a skill block. The header names the server by its config id, not by
the name the server reports. A server's instructions are included even with
`ServerConfig.Skills` off, since a server can send instructions without serving
skills, and a server that sends none adds nothing.

## Testing

The tests run fully offline against real in-process mcpkit servers, a scripted
`StubProvider`, and a canned reader. See `examples/agent-async` for a
golden-transcript scenario and `surfaces/chat` for the terminal CLI built on
this package.

Run the module's tests with `cd host && go test -race ./...`, or `make test` from the repo root
to test every module.
