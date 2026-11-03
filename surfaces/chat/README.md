# agentchat

agentchat is a terminal chat harness over any set of MCP servers. Point it at a
config (or a server URL) plus an OpenAI-compatible model, and converse with
live tool calls, streamed output, and in-terminal elicitation prompts. It is
the reference in-process surface for the `chakra` module (see
`docs/AGENT_DESIGN.md`, Surfaces).

## Quick start

Against a local model (lmstudio, vllm, any OpenAI-compatible endpoint) and
one MCP server:

```bash
go run . --model qwen2.5-7b-instruct \
  --base-url http://localhost:1234/v1 \
  --url http://localhost:8080/mcp
```

Or with a config file:

```bash
go run . --config agentchat.json
```

```json
{
  "model": {
    "baseUrl": "http://localhost:1234/v1",
    "model": "qwen2.5-7b-instruct",
    "apiKeyEnv": "MODEL_API_KEY"
  },
  "instructions": "You are a helpful assistant with access to tools.",
  "servers": [
    { "id": "skills", "url": "http://localhost:18099/mcp" },
    { "id": "internal", "url": "https://tools.example.com/mcp",
      "auth": { "type": "bearer", "tokenEnv": "INTERNAL_MCP_TOKEN" },
      "allow": ["search", "lookup"] },
    { "id": "svc", "url": "https://svc.example.com/mcp",
      "auth": { "type": "client-credentials",
                "clientIdEnv": "SVC_CLIENT_ID",
                "clientSecretEnv": "SVC_CLIENT_SECRET",
                "scopes": ["mcp:basic"] } }
  ]
}
```

Secrets are env-indirected (`apiKeyEnv`, `tokenEnv`, `clientIdEnv`,
`clientSecretEnv` name variables, never values), and validation fails at
startup when a named variable is unset. A per-server `allow` list is a
capability boundary (a FilterSource). Tools outside it are neither offered to
the model nor callable.

## Workspace file tools

`--workspace <dir>` gives the model a confined file tool set (`read_file`,
`edit_file`, `write_file`, `list_files`, `search_files`) plus the checkpoint
that guards their writes:

```bash
go run . --config chat.json --workspace ~/code/project
```

Edits render as a diff at the approval prompt rather than truncated JSON.
`/undo` restores files to the start of the last turn, and repeating it steps
back one turn at a time. `/checkpoints` lists restore points.

There is no default directory, and omitting the flag leaves the tools off
entirely. That is deliberate rather than cautious. A model's instructions can
arrive in content it read. Suppose the editor were rooted somewhere nobody
named. Any injected instruction would then become a write under that root. So
turning these tools on means naming a directory.

`list_files` and `search_files` skip some directories, and `--workspace-exclude`
overrides that list. `--no-checkpoint` drops the snapshot, and with it `/undo`.

### `--workspace-config`: commands, language servers, repo map

`--workspace-config <file.json>` adds an allowlist of commands for the model to
run, language servers, and the repo map. No flag can express these. It only
applies alongside `--workspace`. The file deliberately has no roots. The
directory still comes from the flag, and the commands run in the same roots as
the file tools and write to them.

```bash
go run . --config chat.json --workspace ~/code/project \
  --workspace-config workspace.json
```

```json
{
  "repoMap": true,
  "languageServers": [
    { "command": ["gopls"], "extensions": [".go"], "languageId": "go" }
  ],
  "exec": {
    "sandbox": "platform",
    "commands": [
      {
        "name": "test",
        "argv": ["go", "test", "-race"],
        "description": "Run the Go tests for the named package paths, e.g. ./host/...",
        "args": { "max": 3, "match": "\\./[\\w./-]*(\\.\\.\\.)?", "paths": true },
        "readOnly": true,
        "timeout": "5m"
      }
    ]
  }
}
```

- **`exec.commands`**: each entry becomes one tool, named `run_<name>`. The
  model picks a tool; it never composes a command line. `argv` is an argv, not
  a shell string, and `argv[0]` resolves on PATH when agentchat starts, so
  write `["sh", "scripts/foo.sh"]` rather than `["scripts/foo.sh"]`. `dir`
  defaults to the first root.
- **`args`**: without it, the tool takes no arguments. With it, the model may
  pass up to `max` trailing arguments, each matching `match` end to end;
  `paths` also requires each one to name a path inside a root.
- **Approval**: `readOnly` makes a command auto-allowed under `read-only-auto`
  and above. `reversible` makes it auto-allowed under `reversible-auto`. An
  entry with neither is treated as destructive and always asked about.
- **`exec.sandbox`**: `"platform"` (the default) confines every command, and
  fails at startup where there is no backend. Today that is darwin only
  (`sandbox-exec`). `"unconfined"` opts out, for a process already isolated
  some other way, such as a container. Network is off per command unless
  `allowNetwork` is set. `writePaths` and `denyRead` override the defaults
  (build caches writable, credential directories unreadable); `[]` means none.
- **Limits**: `timeout` (Go duration; default `2m`, per command or for all)
  and `maxOutput` (bytes held per stream; default 1 MiB). The model gets 32
  KiB of each stream. When offloading is on (`--offload-threshold`, or
  `offload` in the config), the store holds the output, and the model gets all
  of it.
- **`languageServers`**: LSP in the loop. Diagnostics follow each edit, and the
  model gets `goto_definition` and `find_references`.
- **`repoMap`**: puts a ranked map of the repository in the system prompt.
- **`exec.network`**: `{"allow": [...hosts], "ask": true}` lets sandboxed commands reach the
  listed hosts through a local proxy, and asks about others; see `ext/exec/README.md`.
- **`exec.shell.processes`**: `true` (or an object with limits) adds `exec_command`,
  `write_stdin`, `list_processes` and `kill_process` for commands that keep running; see
  `ext/exec/README.md`.
- **`editTools`**: `["edit_file"]`, `["apply_patch"]`, or both (the default).
  `apply_patch` takes a multi-file patch in the `*** Begin Patch` format. Some
  models are trained on that format; see `ext/files/README.md`.
- **`plan`**: `true` adds `update_plan`, a checklist. The model publishes it
  and keeps it current. It shows in the conversation as it changes, and it is
  still in the model's view after a compaction.
- **`git`**: adds `ext/git` over the first root. That gives `git_status`,
  `git_diff` and `git_log`, plus a line after each turn saying what the turn
  changed (`/diff` shows it). With `"review": true` it also adds a `review`
  tool and `/review`. `"mutations": true` adds commit and branch tools, and
  `"worktreeDir"` adds the worktree tools; see `ext/git/README.md`.

Unknown fields are an error, so a misspelt `readOnly` fails at startup instead
of quietly making a command ask (or not ask). `examples/dogfood/workspace.json`
is a working file for this repo.

### Skills from the workspace

With `--workspace`, skills in `<folder>/<name>/SKILL.md` join the skill catalog
beside any MCP server's. By default the folders are `.agents/skills` and
`.claude/skills` in each workspace root, plus `~/.agents/skills`. The workspace
file changes that:

```json
{"skills": {"dirs": [".agents/skills", "~/my-skills"]}}
```

`dirs` replaces the defaults (an empty list reads none). `preset` picks a
different starting list instead: `codex` (`.agents/skills`, `~/.agents/skills`)
or `claude` (`.claude/skills`, `~/.claude/skills`). A relative folder is read
under every workspace root.

## Headless runs (`agentchat exec`)

`exec` runs one task without a terminal UI and exits, for CI jobs and scripts. It takes every
flag above (config, workspace, session store) plus its own:

```
agentchat exec --config agentchat.json --workspace . --approve reversible-auto \
  --output-schema result.schema.json --output-last-message result.json --json \
  -p "The tests fail. Make them pass." > events.jsonl
```

- The prompt comes from `-p`, `--prompt-file`, the arguments, or stdin.
- `--json` writes every event to stdout as JSON lines, `{"kind": ..., "payload": ...}`, the same
  frames that agentweb's Watch sends. Without it only the final message is printed.
- `--output-schema` asks for the final answer as JSON valid against the schema. The Runner
  checks it, shows the model what was wrong, and retries once.
- `--approve` is required, since nobody can answer an approval prompt. If the mode would ask
  about a call, the call is denied, and the run writes an `ask-denied` line saying so.
- With `--session-store`, the run id goes to stderr (`agentchat exec: session <id>`), and
  `--session <id>` continues that run.

| Exit | Meaning |
|---|---|
| 0 | done |
| 1 | the turn failed |
| 2 | it hit `--max-steps` |
| 3 | `--output-schema` was set and no answer matched it |
| 64 | bad usage: no `--approve`, no prompt, an unreadable schema |

`--max-steps` overrides a config file's `maxSteps`, as the tree-budget flags do.

## Status line

Both TUI surfaces show a persistent status line that reads
`model <active> · session <id> · ctx <in>↑ <out>↓ tok`. The token counts come
from the last turn's streamed `Usage`. The line is kept in the managed live
region and never committed to scrollback. Pass `--context-window <tokens>` to
add a `N% context left` gauge, since context exhaustion is the top silent failure. This is the
first slice of the TUI layout track (#1063 D1/D2); collapsible tool cells (B4)
already shipped with `--ui notebook`.

## Keys & help (TUI)

Both surfaces show a one-line help bar under the status line (`enter send · tab
complete · ↑↓ history · ? help · ctrl+c quit`). Press **`?`** on an empty prompt
to expand the full key list (in the notebook's NAV mode, `?` always toggles).
The bar, the `?` view, and the `/keys` cheatsheet are all generated from one
`key.Binding` set, so they can't drift from what the surface actually handles
(#1063 C2).

The input line supports readline-style editing. `/keys` prints this cheatsheet
in-session.

| Key | Action |
|---|---|
| `←` / `→` | char back / forward |
| `ctrl+←` / `ctrl+→` | word back / forward |
| `ctrl+a` / `ctrl+e` (or `Home` / `End`) | start / end of line |
| `ctrl+w` | delete previous word |
| `ctrl+k` / `ctrl+u` | delete to end / start of line |
| `ctrl+home` / `ctrl+end` | start / end of input |
| `↑` / `↓` | command history · `Tab` complete · `Enter` send · `ctrl+c` quit |

Word navigation is bound to both `ctrl+←/→` and `alt+←/→` (`alt+b`/`alt+f`).
The `alt+*` bindings need your terminal to send Option as Meta, and so do
`alt+d` (delete-word-forward) and `alt+<`/`alt+>` (input ends). The `ctrl+*`
bindings work without it.

## Interfaces (`--ui`)

`--ui` picks the surface. The default is `auto`, which picks the inline TUI
when stdout is a terminal and `plain` otherwise. The other values are `tui`
(inline), `notebook` (alt-screen), and `plain` (a scriptable line REPL for
pipes/CI).

- **`tui`** (inline): finished output commits to the terminal's own scrollback,
  so native scroll, copy/paste, and a transcript that survives exit all work.

Both interactive surfaces (`tui`, `notebook`) render the assistant's finished
prose as **markdown**. A block streams raw, and when it commits, its headings,
lists, and fenced code are formatted once. Tool lines and the turn footer pass
through verbatim. `NO_COLOR` falls back to raw text, and so does `plain`, which
never renders markdown. `ctrl+o` gives back the raw markdown for copying. In
`notebook` it toggles the whole transcript rich↔raw in place. In `tui` it dumps
the last reply's raw markdown to scrollback, because native scrollback can't
re-render; use `notebook` for the in-place toggle.
- **`notebook`** (alt-screen): a managed viewport with its own scroll and a
  transcript of **collapsible cells**. The assistant text, each tool call, and
  command / info lines each fold on their own, and parallel tool calls group
  into one cell. There are two modes. **INS** (default) types into the input,
  and `esc` enters **NAV**. In NAV, `↑↓`/`jk` move a cell cursor, `space`
  folds/unfolds, `g`/`G` jump to ends, and `esc`/`i` return to INS.
  `pgup`/`pgdn` and the mouse wheel scroll. In INS, `↑`/`↓` move within a
  multi-line prompt first, then recall history, then scroll the transcript.
  **Enter sends; `ctrl+j` inserts a newline** (also `shift+enter` /
  `alt+enter` where the terminal supports them). The prompt box auto-grows with
  its line count up to `--notebook-max-lines` (default 20) and shrinks back.
  Alt-screen has a cost. It takes the whole screen, breaks native copy/paste
  and shell scroll, and clears on exit. So the inline `tui` is still the
  default.

One color decision covers every surface. `--no-color` disables ANSI styling,
and so do `NO_COLOR` (any value) and `TERM=dumb`. Otherwise color is on and
degrades truecolor→256→16→mono to the terminal's capability. Glyphs mark
selections and status, so meaning never depends on color alone, and a
no-color terminal is still legible.

## Telemetry and failover

`--exporter` selects telemetry (`stdout`, `otlp`, `auto`; empty = off, zero
overhead), and `--otlp-endpoint` points at a collector (default
localhost:4317). The repo's examples use the same contract. With an exporter
on, every turn emits `agent.turn` / `agent.step` / `agent.tool` spans
stitched to client dispatch and server spans (one trace end to end), and
operational logs flow through slog to the same collector. The transcript is
UI, not logging, so it always renders to the terminal regardless of exporter.

`model.backup` in the config enables failover. If a call fails cleanly on the
primary, it is retried once on the backup, the primary is benched for a
cooldown, and the transitions are logged. A stream that already produced output
is never silently replayed. `/health` shows the current snapshot.

## Agent-managed events and tasks (meta-tools)

With `metaTools` enabled (implied whenever events or triggers are configured),
the model gets host-local tools to manage its own async: `subscribe_events` /
`unsubscribe` / `list_subscriptions`, `create_trigger` / `remove_trigger` /
`list_triggers`, and `list_tasks` / `cancel_task`. So a conversation can set up
standing behavior:

```
> email me whenever a user is created
⚙ subscribe_events({"server":"crm","name":"user.created"})
⚙ create_trigger({"event":"user.created","instructions":"send a welcome email","label":"welcome"})
Done — I'll email new users.
  (later, a user is created)
· trigger: welcome
⚙ send_email({"to":"ada@example.com"})
Sent a welcome email to ada@example.com.
```

`create_trigger` works for task completions too (they're `task.completed`
events), so "notify me when the build finishes" is the same tool.

## Background tasks

A task-backed tool call runs inline for a grace window (`taskGraceSec`,
default 10s). If it is still running when the window expires, it detaches. The
model gets a "moved to background" result and finishes its turn, you keep
chatting, and the task keeps running (input pauses still prompt you). When the
task completes, a `· task <id> completed: ...` line prints, and the outcome is
injected as `task.completed` context on your next turn, so the model can react
to it.
Bind a trigger on `task.completed` to have the agent proactively tell you the
moment a job finishes. `/tasks` lists running tasks; `/tasks cancel <id>`
stops one. Set `taskGraceSec` negative to wait inline forever.

## Connections

The `connections` block in the config names every model endpoint that agentchat
can use and says which one does which job. A connection is an endpoint plus a
model, and the role fields (`active`, `embedder`, `agentRoles`, `stepRoutes`)
point at connections by name.

```json
"connections": {
  "active": "big",
  "embedder": "embed",
  "connections": {
    "big":      { "type": "openai", "model": "gpt-5.5", "apiKeyEnv": "OPENAI_API_KEY", "temperature": false },
    "mini":     { "type": "openai", "model": "gpt-5.4-mini", "apiKeyEnv": "OPENAI_API_KEY" },
    "deepseek": { "baseUrl": "https://api.deepseek.com/v1", "model": "deepseek-chat", "apiKeyEnv": "DEEPSEEK_API_KEY", "responseSchema": false },
    "local":    { "type": "llamacpp" },
    "embed":    { "type": "ollama", "model": "nomic-embed-text", "dim": 768 }
  },
  "agentRoles": { "small": "mini", "large": "big" },
  "stepRoutes": [{ "fromStep": 2, "connection": "mini" }]
}
```

| Field | What it does |
|---|---|
| `active` | The chat connection at startup. `/provider <name>`, or agentweb's header picker, switches it for later turns. Required. |
| `connections` | Name to endpoint, with the fields below. |
| `embedder` | The connection that embeds for semantic memory. It must speak an embeddings API, so an `anthropic` connection is refused. Set `dim` on it. Optional. |
| `agentRoles` | A server-advertised agent can send a `small`, `medium` or `large` hint, and this maps each hint to a connection. An agent runs on its hinted role, or on a larger one when that role is unset or lacks the `vision` the hint asks for. An agent with no hint runs on `active`. Optional. |
| `stepRoutes` | Sends ranges of each turn's model calls to another connection. See [Routing steps to another model](#routing-steps-to-another-model). Optional. |

One connection's fields:

| Field | What it does |
|---|---|
| `type` | A built-in endpoint: `openai`, `openai-responses`, `anthropic`, `gemini`, `openrouter`, `litellm`, `lmstudio`, `llamacpp`, `ollama`. It sets the base URL. For `anthropic`, `openai-responses`, `llamacpp` and `ollama` it also sets the provider that talks to it. Optional when `baseUrl` is set. |
| `baseUrl` | The API root with its version prefix, such as `https://api.deepseek.com/v1`. Overrides `type`'s default. A connection with no `type` speaks the OpenAI chat-completions wire. |
| `model` | The model id sent on every request. Required except for `llamacpp`. |
| `apiKeyEnv` | The environment variable holding the key. Leave it out for local servers. |
| `temperature` | `false` for a model that refuses a temperature (OpenAI's reasoning models on the `openai` type), `true` for one that takes it on `openai-responses`. It changes what the provider reports, so a caller knows not to send one. |
| `responseSchema` | `false` for an endpoint that refuses `response_format: json_schema`, such as DeepSeek. A structured answer (`agentchat exec --output-schema`) then asks for `json_object` with the schema in the system message, and the Runner's validate-and-retry checks the reply against it. |
| `reasoningEffort` | `openai-responses` with a reasoning model only: `low`, `medium` or `high`. |
| `thinkingHint` | `{ "openTag": "<think>", "closeTag": "</think>" }` for a model that writes its reasoning inline, so surfaces show it as thinking. |
| `vision` | Marks a model that takes images. Read only when `agentRoles` picks a connection. |
| `dim` | The embedding width, when the connection is the `embedder`. |

The doc comments on `ConnectionsConfig` and `ConnectionConfig` in
`host/connections.go` are the full contract.

## Routing steps to another model

`/provider <name>` switches the model for whole turns. `connections.stepRoutes`
picks it step by step inside a turn, for instance planning on the active
model and continuing on a cheaper one:

```json
"connections": {
  "active": "big",
  "connections": { "big": {...}, "mini": {...} },
  "stepRoutes": [{ "fromStep": 2, "connection": "mini" }]
}
```

Steps count from 1 in every turn. `toStep` bounds a route, and the first route
that covers a step wins. If no route covers a step, the step runs on the active
connection, so `/provider` still sets the default. After each switch, the next
call starts with a cold prompt cache on the new model. So a few long ranges cost
less than alternating. A routed step's tokens show up per connection on the
turn's `usageByModel` and on a `step` event, and agentchat prints a dim
`↳ step 2 on mini` line after each step of a routed turn.

## Events, injected context, and triggers

Configure event streams per server, and the host consumes them through two
policies. Both policies live in the agent module, and both are fully pluggable
for embedders:

```json
"servers": [{ "id": "grocery", "url": "...",
  "events": [{ "name": "cart.changed",
    "hint": { "priority": "high",
              "aggregate": { "windowMs": 2000, "strategy": "merge" },
              "template": "the cart now holds {{count}} items" } }] }],
"triggers": [{ "event": "cart.changed",
  "filter": { "count": "2" },
  "instructions": "The cart changed. Offer one recipe suggestion.",
  "label": "recipe-pitch", "cooldownSec": 300 }]
```

The injection policy buffers occurrences per hint (merge, last-wins, or
debounce windows) and renders them via the template. At the next turn they join
the conversation as system messages, priority-ordered under a budget. Sensitive
events gate on consent. Host config hints override server-advertised ones (the
vendor context-hint on events/list).

Under the trigger policy, a matching event starts a proactive turn, marked with
its label. The anti-nag policy mediates these turns. A binding fires once, and
it can't fire again until the user engages AND the cooldown passes. A session
budget applies on top of that. Servers can
suggest triggers; the host decides.

## Auth modes

`auth.type` selects among the client auth modes that MCP supports:

- **`bearer`**: static token from `tokenEnv`.
- **`client-credentials`**: OAuth machine-to-machine via ext/auth. PRM and AS
  discovery, token caching, and refresh are automatic; `scopes` is optional
  (empty inherits the server's PRM scopes_supported); `allowInsecure: true`
  permits an http:// AS for dev setups.
- **`oauth`** (authorization-code browser flow): interactive login via ext/auth.
  PRM and AS discovery + PKCE are automatic. With no `clientIdEnv` the client
  self-registers via DCR; set `clientIdEnv` (and optional `clientSecretEnv`) to
  pin a pre-registered client. `scopes` is optional (empty follows the server's
  401 challenge). The PKCE flow runs on the first 401. If it does not complete,
  the server is left at `needs-login`. The `/mcp` overlay then offers a login
  action that re-runs the flow. `/servers login <name>` re-runs it too.
  `allowInsecure: true` permits an http:// AS for dev.

Every flag is env-overridable with the `AGENTCHAT_` prefix (dashes become
underscores: `--base-url` is `AGENTCHAT_BASE_URL`); an explicit flag beats
the env var.

In the REPL, `/tools` lists the merged tool index, `/history` shows the
conversation, `/health` shows the failover snapshot, and `/quit` exits. Ctrl-C
cancels the in-flight turn. During an elicitation, `/d` declines and `/c`
cancels.

`/sessions` shows recent persisted sessions (newest-first), with `/sessions
more` to page older ones, `/sessions find <text>` to search by id, and
`/sessions <id>` to resume. (The gorm/sqlite and in-memory stores sort by
recency for free. A redis store's SCAN order is unsorted; see the runstore
docs.)

`/mcp` (alias `/servers`) opens an interactive panel of the connected MCP
servers and their state. On a selected row: `enter` reconnects a failed or
needs-login server, `t` lists that server's own tools, and `l` starts an
interactive login for a needs-login server that uses the `oauth` auth type. The
same actions are available as commands: `/servers reconnect <name>`, `/servers
tools <name>`, `/servers login <name>`. In the interactive surfaces these render
as an overlay dialog; the plain REPL prints the result.

## What a session looks like

```
agentchat: 1 server(s), model qwen2.5-7b-instruct. /tools /history /quit; Ctrl-C cancels a turn.
> what tools do you have and what is 2+40?
⚙ add({"a":2,"b":40})
  ✓ add: 42
I have an add tool available. 2 + 40 = 42.
— 2 step(s), 812 in / 64 out tokens
>
```

Elicitation renders inline, schema-driven (enums become numbered choices,
booleans y/n, required fields re-prompt):

```
⚙ log_service({"car":"honda"})

? What was the mileage at service time?
  mileage (integer): 42000
  ✓ log_service: recorded at 42000 miles
```

## Walkthroughs against in-repo examples

Each assumes a local OpenAI-compatible model on `localhost:1234`.

For the **skills server** (`examples/skills`, port 18099 per its README), start
the fixture, then run `go run . --model <m> --url http://localhost:18099/mcp`.
The host discovers the server's SKILL.md skills, verifies their digests, and
puts them in the system prompt. A per-server `"skillsMode"` in the config picks
how. `"eager"` injects full bodies. `"catalog"` is the default, and it injects
only the name and description. It also exposes a `load_skill(name)` tool, which
fetches a body on demand and verifies it. SEP-2640 forbids fetching a skill
ahead of need, so `"eager"` is an explicit opt-in. `"skills": false` opts a
server out entirely.

For the **auth example** (`examples/auth`), start its server, export the token
that it mints, and connect with `authTokenEnv`. The bearer flows through
`client.WithClientBearerToken`; OAuth token sources are wired the same way in
config once a flow needs them.

In the **Tasks v2** example (`examples/tasks-v2`), task-returning tools work end
to end.
agentchat negotiates the tasks extension, polls task-backed calls to a
terminal state (honoring server poll hints), renders status transitions as
dim `· task <id>: <status>` lines, and routes input_required pauses through
the same terminal elicitation prompts as everything else. One InputHandler
covers ephemeral MRTR and task-backed pauses alike. Try `confirm_delete` or
`multi_input` from the example.

## Testing

The app core is fully testable offline. Every test drives real in-process
mcpkit servers with a scripted `StubProvider` (no network, no model), and
scripted stdin exercises the terminal elicitation UI. The live-model
session above is the manual interop check; run it against lmstudio before
releases.
