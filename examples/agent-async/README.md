# agent-async

An agent manages its own asynchronous work **through conversation**, not
config. It subscribes to an event stream, installs a standing "when X happens,
do Y" trigger and runs a task tool. Later the event wakes it and it acts. All
of the control goes through host meta-tools (`subscribe_events`,
`create_trigger`, `list_tasks`, ...), and the model calls them like any other
tool.

This example shows the agent module's scriptable loop. The whole flow runs
deterministically against a scripted `StubProvider`, with no LLM and no
network. So the example is both a runnable demo and a golden-transcript test.

## Run it

```bash
just agent          # deterministic scripted run (no model)
just test           # the golden-transcript test
just demo qwen2.5-7b-instruct   # a live model improvising against the same server
```

## One config, three surfaces

Both live surfaces load the single host config `config.json`. One surface is
the CLI (`agentchat`) and the other is the browser (`agentweb`). Start the demo
MCP server, then point either surface at it:

```bash
just serve   # the app-domain MCP server (events + tasks + send_email) on :8788
just chat    # agentchat CLI against config.json (another terminal)
just web     # agentweb browser surface against config.json (another terminal)
```

`config.json` holds the model connection, `metaTools`, and the server. It
cannot hold the scripted `StubProvider`, and `just agent` needs that stub to
be deterministic. So `runScenario` loads the same `config.json`, points the
server URL at the in-process test server, and injects the stub. The live
surfaces use the config verbatim.

## What happens

```
> email me whenever a user is created
⚙ subscribe_events({"server":"app","name":"user.created"})
⚙ create_trigger({"event":"user.created","instructions":"send a welcome email...","label":"welcome"})
Set — I'll welcome-email every new user.
> start the quarterly report
⚙ long_report({})
The quarterly report is ready — 42 pages.
(a user signs up...)
· trigger: welcome
⚙ send_email({"to":"ada@example.com"})
Welcomed ada@example.com.
```

The model set up the standing behavior in the first turn. Later `user.created`
fired, the trigger woke the agent, and the agent emailed the new user. No code
path was hardcoded for that.

## How it's built

- `scenario.go` holds the app-domain MCP server (`user.created` events,
  `send_email`, a task-backed `long_report`), the scripted model turns, and
  `runScenario`. `runScenario` wires the reusable host (`host`) to the server.
- `main.go` is a dual-mode entry (stub vs `--model`). It also has a
  concurrency-safe transcript writer, because the proactive trigger turn
  writes from the event goroutine.

The host is `github.com/panyam/chakra/host`. The agentchat CLI runs on the same
App, and this example imports the host to drive that App programmatically.

On a genuinely long task, the task runs past its grace window, detaches into
the background, and notifies via a `task.completed` event. The agentchat tests
exercise that path. Here the report finishes fast to keep the demo
deterministic.
