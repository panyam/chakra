# git

Git for a coding agent: read-only tools the model calls, opt-in tools that
change the repository, and a report of what each turn changed on disk (#151).

## Tools

| Tool | Hint | Offered |
|---|---|---|
| `git_status` | read-only | always |
| `git_diff` | read-only | always |
| `git_log` | read-only | always |
| `git_commit` | destructive | `Config.Mutations` |
| `git_branch` | destructive | `Config.Mutations` |
| `git_worktree_add`, `git_worktree_remove` | destructive | `Config.WorktreeDir` set |

One tool per operation, because approval hints are per `ToolDef`: a single
`git` tool would give a commit the same disposition as a status.

`git_diff` with no target is everything uncommitted against HEAD, staged and
unstaged together, with untracked files listed after it, since `git diff`
leaves them out. With `base` it's the working tree against where HEAD forked
from `base` (their merge base), so commits `base` gained since don't show up.
With `commit` it's that one commit. `Source.Diff` returns the same text to Go
callers, which is what review (#152) uses.

Every call runs `git` with the repository's pager, colour, external diff and
textconv drivers and fsmonitor hook turned off, and pathspecs taken
literally. A ref that starts with `-` or holds anything a ref, commit id or
rev expression doesn't is refused before git sees it, and every path is
confined to `Config.Root`, which can be a directory inside the repository.
`git_commit` runs the repository's commit hooks, which is one reason it is
destructive and off by default.

## What a turn changed

The extension snapshots the work tree when a turn starts and again when it
ends, and reports the difference: a one-line summary announced to every
surface ("git: this turn changed 3 files (+2 -2); /diff shows it"), the patch
behind `/diff` (`/diff stat` for the summary), and `Extension.LastTurn()` for
code.

A snapshot is a git tree written from a copy of the index, so the user's own
index and files are never touched, and a change they staged before the turn
isn't counted as the turn's. It sees whatever wrote the files, a tool or a
shell command, and leaves out what `.gitignore` does. The blobs it writes stay
unreferenced in the object store until git's gc removes them.

The turn-end half needs the host to say a turn ended, which it does through
the optional `host.TurnEnder` interface. It's called for finished, partial and
failed turns alike, since a failed turn's writes happened all the same.

This reports on version control and restores nothing. `ext/checkpoint` is
what puts files back, and the two don't share snapshots.

## Review

With `Config.Review` set (`"git": {"review": true}` in a workspace file), the
extension adds a `review` tool and a `/review` command (#152). Both diff a
target (the uncommitted changes by default, `base` for a branch's changes
since it forked, `commit` for one commit) and hand the diff to a fresh
reviewer agent as its task. The reviewer:

- starts with no conversation, only the rubric (`DefaultRubric`, or
  `ReviewConfig.Rubric`) and the diff, so nothing from the session it reviews
  colours what it finds (A7 already keeps parent state out);
- gets the session's read-only tools, so it can read the files around a hunk,
  and nothing that writes, and not `review` itself;
- answers under a schema, so the findings come back as data: title, body,
  file, line range, priority 0 to 3 and confidence, plus a verdict.

`/review [base REF | commit REF] [-- instructions]` prints the findings for a
person. The tool returns them as JSON for the model, with the same object as
structured content. A target with no changes says so without asking a model.

The reviewer runs on the session's provider and renders as a sub-agent on
every surface. Both come from the host through the optional
`host.SubAgentUser` interface, which lends an extension the provider, the
read-only tools and the sub-agent event sink.
