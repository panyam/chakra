# agentchat playground

One command to feel the chakra agent SDK. Run `make pg` from the repo root. It
boots a demo MCP server on `:8787`, which is `examples/agent-async`, the events
and tasks app domain. Then it launches `agentchat` in its TUI, wired to a local
model, a SQLite session store, and a filesystem offload dir.

```bash
make pg
```

`make pg` is the batteries-included wrapper, with the sqlite session store and
the offload dir. This directory also has a justfile for the plain config-driven
surfaces. It runs both surfaces off `playground.json`:

```bash
just serve   # the demo MCP server (agent-async app domain) on :8787
just chat    # agentchat CLI against playground.json (another terminal)
just web     # agentweb browser surface against playground.json (another terminal)
```

## What you need

A local OpenAI-compatible model endpoint. The bundled `playground.json`
assumes [LM Studio](https://lmstudio.ai) on its default `http://localhost:1234/v1`
with a model named `qwen2.5-7b-instruct` loaded. Edit `connections` in
`playground.json` to match what you have (or point `type`/`baseUrl` at Ollama,
vLLM, or any OpenAI-compatible server). `/provider` in the TUI switches between
the configured connections at runtime.

## What to try in the TUI

- Type a message. Ask it to `greet` someone and watch the live tool call.
- `↑` / `↓` recall previous inputs. The input box is editable and cursor-traversable.
- `/` then `Tab` completes slash commands. `/provider `<Tab> cycles connections,
  and `/sessions `<Tab> cycles saved sessions.
- `/fork` branches the conversation. `/sessions` lists saved sessions and switches
  between them, and `/resume <id>` reopens one. Sessions persist to the SQLite
  file, so they survive restarts.
- Large tool outputs are offloaded to `~/.agentchat/pg-blobs/`. Each is replaced
  by a stub, and the model can query the full output with `read_tool_result`.

`--ui plain` (or piping) falls back to the line-based REPL.
