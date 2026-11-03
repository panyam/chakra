# agentsmd

Loads a repository's instruction files (`AGENTS.md`, `CLAUDE.md` and the like) into the agent's
context. A `Profile` sets the rules, and the presets copy other coding agents so a session can be
compared against them like for like.

```go
p, _ := agentsmd.Preset("codex")
ext, _ := agentsmd.New(agentsmd.Config{Roots: []string{"/work/repo"}, Profile: p})
app, _ := host.NewApp(cfg, out, in, host.WithExtension(ext))
```

`surfaces.WorkspaceExtensions` adds it whenever there are workspace roots, with the `chakra`
preset unless `WorkspaceConfig.Instructions` names another. In a workspace file:

```json
{"instructions": {"preset": "codex", "maxBytes": 65536}}
```

`{"instructions": {"off": true}}` turns it off.

## Presets

| | `codex` | `claude` | `chakra` (default) |
|---|---|---|---|
| Names, first present per directory | `AGENTS.md` | `CLAUDE.md`, `.claude/CLAUDE.md` | `AGENTS.md`, `CLAUDE.md` |
| Override | `AGENTS.override.md` | none | `AGENTS.override.md` |
| Local file, after the main one | none | `CLAUDE.local.md` | none |
| Fallback, only if the chain has none of the above | none | `AGENTS.md`, `.claude/AGENTS.md` | none |
| Global directory | `~/.codex` | `~/.claude` | none |
| Startup chain | git root down to the workspace root | every parent down to the workspace root | git root down to each workspace root |
| Subdirectories | never | once a tool call names a path there | once a tool call names a path there |
| Edits during a session | ignored until the next session | ignored until the next session | shown before the next model call |
| `@path` imports, stripped HTML comments | no | yes | no |
| Size cap, all files together | 32 KiB | none | 32 KiB |

The Codex column follows its documentation for `AGENTS.md` discovery. The Claude column follows
Claude Code's memory documentation, with one difference: Claude Code re-reads its root file after
a compaction, and this preset doesn't.

Cursor's rule folders (`.cursor/rules/*.mdc`, applied always, by glob, by description or on
mention) are a different mechanism and aren't covered here.

## Where each part hooks in

- **The startup chain** is a prompt section, read on the first turn and the same text every turn
  after, so the provider's prompt cache holds.
- **Subdirectory files** are found by a tool middleware. After a call that names a `path`, `dir` or
  `workdir`, it checks each directory between the workspace root and that path. `Config.Paths`
  replaces the argument names for other tools.
- **Found and changed files** reach the model in a request-scoped step stage: a system message
  added before each model call and never written to history. It costs tokens on every call and
  survives compaction. Under `changes: notice` it also re-checks every directory read so far,
  with a stat per file, and says which version a changed file replaces or that a file no longer
  applies.

## Trust

The files come from whoever can commit to the repository. The prompt says they rank below the
host's instructions and can't change which actions need approval, and the approval gate enforces
that in code. A project file's `@path` import is refused outside the workspace roots, so a pull
request can't pull `~/.ssh` into the prompt. The user's global file may import from anywhere.
