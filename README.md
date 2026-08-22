<p align="center">
  <img src="docs/assets/chakra-logo.svg" alt="chakra" width="128" height="128">
</p>

<h1 align="center">chakra</h1>

<p align="center">A Go agent SDK built on MCP.</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/panyam/chakra"><img src="https://pkg.go.dev/badge/github.com/panyam/chakra.svg" alt="Go Reference"></a>
  <a href="https://goreportcard.com/report/github.com/panyam/chakra"><img src="https://goreportcard.com/badge/github.com/panyam/chakra" alt="Go Report Card"></a>
  <a href="https://github.com/panyam/chakra/actions/workflows/test.yml"><img src="https://github.com/panyam/chakra/actions/workflows/test.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/panyam/mcpkit"><img src="https://img.shields.io/badge/built%20on-mcpkit-4F7BFF" alt="Built on mcpkit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/panyam/chakra" alt="License"></a>
  <a href="https://github.com/panyam/chakra/stargazers"><img src="https://img.shields.io/github/stars/panyam/chakra?style=social" alt="GitHub stars"></a>
</p>

<!-- Add once the first tag lands; until then it renders as "no releases":
  <a href="https://github.com/panyam/chakra/releases"><img src="https://img.shields.io/github/v/release/panyam/chakra?sort=semver" alt="Release"></a>
-->

It gives you a Provider seam over OpenAI-compatible and Anthropic
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

Or `make pg`, which boots a demo MCP server and drops you into the TUI wired to it.

`surfaces/chat/README.md` covers config files, auth modes, and per-server tool filtering. Runnable
examples live in `examples/`, and `examples/README.md` explains what each one demonstrates.

## Development

```bash
make test              # every module
make test-examples     # the SDK examples
make testall           # both, plus every gate below
make tidy-all          # required after touching a shared import
make setup-hooks       # install the pre-commit and pre-push hooks
make pg                # playground: demo server + agentchat TUI
```

Three gates run in CI and in the pre-push hook:

| Gate | Catches |
|---|---|
| `make check-no-binaries` | a compiled executable tracked in the repo, detected by magic bytes rather than filename |
| `make check-ext-isolation` | one `ext/` module directly requiring another |
| `make check-dep-consistency` | a third-party dependency pinned at two versions across modules |

The last one is not bookkeeping. Every module replaces its siblings by relative path, so third-party
deps resolve through MVS, which takes the maximum required version across the graph. A bump in one
module silently raises the version its siblings build against, and if that bump is API-breaking the
sibling fails without its `go.mod` ever changing. It found a real split (`golang.org/x/sys`) on its
first run.

`make tidy-all` matters for the same reason. Changing a shared import can drift the `go.sum` of a
module you never opened, and CI then fails somewhere unrelated.

## Releasing

Every module is tagged at one version, so `go get github.com/panyam/chakra/<module>@vX.Y.Z` resolves
consistently:

```bash
make bump-siblings V=v0.1.0   # repoint intra-repo requires, then tidy
git commit -am "release: v0.1.0"
make tag-push V=v0.1.0        # tag root + every module, push
```

`bump-siblings` is the step that is easy to skip and expensive to skip. Go ignores `replace` outside
the main module, so a sibling requirement naming a version that was never tagged makes the whole
tree unresolvable to anyone outside this repo, while building fine here. That was the deliberate
state under mcpkit, and it is the state to stay out of now.

## Where knowledge lives

- `CLAUDE.md` — the traps that cause wrong edits. Read before editing.
- `CONSTRAINTS.md` — enforceable invariants (A1 through A10), each with a verify recipe.
- `NOTES.md` — why the code is shaped this way and what bit us.
- `docs/` — design frames (`AGENT_DESIGN.md`, `AGENT_COMPOSITION.md`, `AGENT_MEMORY_FLOW.md`),
  the roadmap and phase status (`AGENT_SDK_ROADMAP.md`), and the web UI epic.

## License

See `LICENSE`.
