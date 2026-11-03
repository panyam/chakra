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

| | `codex` | `claude` | `cursor` | `chakra` (default) |
|---|---|---|---|---|
| Names, first present per directory | `AGENTS.md` | `CLAUDE.md`, `.claude/CLAUDE.md` | `AGENTS.md` | `AGENTS.md`, `CLAUDE.md` |
| Override | `AGENTS.override.md` | none | none | `AGENTS.override.md` |
| Local file, after the main one | none | `CLAUDE.local.md` | none | none |
| Fallback, only if the chain has none of the above | none | `AGENTS.md`, `.claude/AGENTS.md` | none | none |
| Global directory | `~/.codex` | `~/.claude` | none | none |
| Startup chain | git root down to the workspace root | every parent down to the workspace root | git root down to the workspace root | git root down to each workspace root |
| Subdirectories | never | once a tool call names a path there | once a tool call names a path there | once a tool call names a path there |
| Edits during a session | ignored until the next session | ignored until the next session | ignored until the next session | shown before the next model call |
| `@path` imports, stripped HTML comments | no | yes | no | no |
| Size cap, all files together | 32 KiB | none | none | 32 KiB |
| Rule folders | none | `.claude/rules` | `.cursorrules`, `.cursor/rules` | none |

The Codex column follows its documentation for `AGENTS.md` discovery. The Claude column follows
Claude Code's memory documentation, with one difference. Claude Code re-reads its root file after a
compaction, and this preset doesn't.

The Cursor column follows Cursor's rules documentation. Cursor also reads `AGENTS.md`, and its
older single `.cursorrules` file still works.

## Rule folders

`Profile.Rules` names rule folders or single rule files, relative to each workspace root. A folder
is read recursively for `.md` and `.mdc` files. Each rule's frontmatter says when it applies:

| Frontmatter | When it applies |
|---|---|
| `alwaysApply: true`, or a `.md` file with none of the keys below | always, in the system prompt after the startup chain |
| `globs:` (Cursor) or `paths:` (Claude Code) | once a tool call names a matching path, in the per-call block |
| `description:` only | listed in the system prompt with its path, so the model reads it when the task matches |
| none of these, in a `.mdc` file | Cursor's manual rule, which applies only when mentioned, so it isn't loaded |

A glob is relative to the workspace root. `**` spans directories, a trailing `/` means everything
under a directory, and a glob with no `/` matches the file name at any depth (`*.tsx`). Values can
be a comma-separated string, a `[flow, list]` or `- item` lines. The frontmatter is read line by
line rather than as YAML, because Cursor writes `globs: *.ts` unquoted, and YAML would read that
as an alias.

Rules are read once, on the first turn. Each glob rule is shown once per session, the first time a
path matches it, and the announcement names the path and the glob. A described rule goes through
the model's ordinary file tools, so the approval gate sees that read like any other.

## Where each part hooks in

- **The startup chain** is a prompt section, read on the first turn and the same text every turn
  after, so the provider's prompt cache holds.
- **Subdirectory files** are found by a tool middleware. After a call that names a `path`, `dir` or
  `workdir`, it checks each directory between the workspace root and that path. `Config.Paths`
  replaces the argument names for other tools.
- **Found and changed files** reach the model in a request-scoped step stage. The stage adds a
  system message before each model call and never writes it to history. It costs tokens on every
  call and survives compaction. Under `changes: notice` it also re-checks every directory read so
  far, with a stat per file, and says which version a changed file replaces or that a file no longer
  applies.

## What the user sees

The extension implements `host.Announcer`, so each surface shows a line in the conversation when the
startup files load ("Instruction files loaded: AGENTS.md."), when a subdirectory's file is found,
and, under `changes: notice`, when one changes, appears or goes. No UI shows the model's context
itself, so a user reads these lines to tell what it's following.

## Trust

The files come from whoever can commit to the repository. The prompt says they rank below the
host's instructions and can't change which actions need approval, and the approval gate enforces
that in code. A project file's `@path` import is refused outside the workspace roots, so a pull
request can't pull `~/.ssh` into the prompt. The user's global file may import from anywhere.
