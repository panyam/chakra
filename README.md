# chakra

A Go agent SDK built on MCP. It gives you a Provider seam over OpenAI-compatible and Anthropic
endpoints, a Runner loop with wire-serializable events, tool sources that federate any number of MCP
servers, tiered memory, durable run persistence with fork and rewind, approval gating, sub-agents,
and two ready-made surfaces (a terminal CLI and a web bridge).

It consumes [mcpkit](https://github.com/panyam/mcpkit) as an ordinary dependency. mcpkit is the
protocol layer; chakra is the agent layer above it.

## Status

Unreleased, and carrying no compatibility promise. Nothing here is tagged, so
`go get github.com/panyam/chakra@main` resolves to a pseudo-version, and that pseudo-version is the
whole stability signal you get. The surface still takes deliberate breaking reshapes.

The tree previously lived in `panyam/mcpkit`, first under `agent/` and then under
`experimental/agent/`. It moved here so the protocol surface could commit to API stability while the
agent surface kept breaking things. History came with it: `git log` and `git blame` reach back
through both moves. Issue numbers referenced in commit messages and docs point at `panyam/mcpkit`
for anything predating the split.

## Modules

Each of these is its own Go module, so a heavy dependency stays out of the modules that do not need
it. `make test` walks all of them.

| Module | What it is |
|---|---|
| `.` | Provider, Runner, ToolSource, memory, compaction, sub-agents. Two direct requires, by constraint. |
| `host/` | Surface-agnostic host application core: config, connections, slash commands, approval. |
| `surfaces/chat/` | Terminal CLI (`agentchat`). The reference in-process surface. |
| `surfaces/web/` | Connect bridge plus DockView frontend (`agentweb`). |
| `surfaces/` | Shared surface plumbing (run-store construction from a spec). |
| `store/redis/`, `store/gorm/` | RunStore and memory backends that need a driver. |
| `ext/files/` | Workspace file tools. Stale and ambiguous edits are refused. |
| `ext/exec/` | Allowlisted project commands, sandboxed. darwin backend; elsewhere it refuses. |
| `ext/checkpoint/` | Reversal seam (restore versus compensate) plus file checkpoints. |
| `ext/lsp/` | Language servers in the loop: diagnostics on two paths, symbol-addressed navigation. |

## Quick start

The terminal surface against a local OpenAI-compatible model and one MCP server:

```bash
cd surfaces/chat
go run . --model qwen2.5-7b-instruct \
  --base-url http://localhost:1234/v1 \
  --url http://localhost:8080/mcp
```

`surfaces/chat/README.md` covers config files, auth modes, and per-server tool filtering. Runnable
examples live in `examples/`, and `examples/README.md` explains what each one demonstrates.

## Development

```bash
make test              # every module
make test-examples     # the three SDK examples
make testall           # both, plus the tracked-binary and ext-isolation gates
make tidy-all          # required after touching a shared import
make setup-hooks       # install the local pre-commit hook
```

`make tidy-all` matters more than it looks. The modules replace each other by relative path, so
changing a shared import can drift the `go.sum` of a module you never opened, and CI then fails
somewhere unrelated.

## Where knowledge lives

- `CLAUDE.md` — the traps that cause wrong edits. Read before editing.
- `CONSTRAINTS.md` — enforceable invariants (A1 through A10), each with a verify recipe.
- `NOTES.md` — why the code is shaped this way and what bit us.
- `docs/` — design frames (`AGENT_DESIGN.md`, `AGENT_COMPOSITION.md`, `AGENT_MEMORY_FLOW.md`),
  the roadmap and phase status (`AGENT_SDK_ROADMAP.md`), and the web UI epic.

## License

See `LICENSE`.
