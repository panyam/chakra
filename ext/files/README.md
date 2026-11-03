# files

File reading and editing for an agent. If an edit was written against a stale
view of a file, it fails instead of applying.

## The failure this prevents

An agent reads a file, decides on a change, and writes it back. In between,
something changed the file underneath. A formatter ran, a sibling tool wrote
it, or the user saved in their editor. An edit addressed by line number applies
anyway and silently discards that change. Nothing reports it, because nothing
noticed.

## Two mechanisms, two questions

|  | Anchor (the text being replaced) | Hash of the content |
|---|---|---|
| Answers | where does this change go | is this still the file I read |
| Survives | lines moving around | nothing, by design |
| Misses | a file reformatted underneath | where anything belongs |

Neither substitutes for the other, so both are here. Suppose a file was
reformatted after you read it. An anchor can still match uniquely in it, and in
exactly that case applying the edit is wrong.

```go
src, _ := files.NewSource(files.Config{Roots: []string{
    "/work/mcpkit",
    "/work/mcpkit-contribs",
}})
```

`Roots` is required and has no "unset means anywhere" mode. A model drives
these tools. Its instructions can come from content it read rather than from
the user, and you cannot tell the two apart after the fact. `agent.Spotlight`
exists for that reason. An unconfined editor turns any such instruction into a
write to an arbitrary path.

**A workspace is a set of directories, and one is the degenerate case.** A
coding session rarely keeps to a single repository. The ordinary task changes
an API in one repo and fixes its callers in another. Give the agent one root,
and it edits the API, reports success, and never sees what it broke.

Naming a common parent instead of listing the roots is not a shortcut. It
silently widens confinement to everything else under that parent, and
confinement exists to deny exactly that.

**Results are absolute paths.** Two repositories often both hold
`src/main.go`, so a bare relative path could name either. A relative path on
the way *in* still resolves against the first root, so a single-root workspace
behaves as it always did.

The confinement is a directory handle (`os.Root`), not a string comparison.
Checking a path and then opening it by name are two separate operations, and
every way out of a workspace lives in the gap between them:

- A symlink is followed when the file is **opened**, not when the path is
  checked. `workspace/notes.md -> /etc/passwd` passes any comparison of the
  path text and then reads the wrong file.
- The filesystem can change between the two. A name can be a regular file
  when it is checked and a symlink a moment later. So an earlier check only
  says something about the past.

`os.Root` holds a directory. At open time it resolves each path component
against that directory and refuses anything that leaves it. So the check and
the use become one act. There is one handle per root, and a path has to resolve
inside one of them. Escape attempts come back as refusals:

```
refusing ../../etc/passwd: outside every workspace root
refusing innocent.md: outside every workspace root      # a symlink pointing out
```

## The tools

**`read_file`** returns the content, its line count, and a hash of it.

```
path: notes.md
hash: dcf19a8dce4a
lines: 42

# Notes
...
```

For a large file, `start_line` and `end_line` read just that part. The hash is
still the whole file's, so a partial read is enough to edit with:

```
path: host/app.go
hash: 7b1e04c9a2f3 (whole file)
lines: 771-830 of 1214

func (a *App) RunTurn(ctx context.Context, input string) error {
...
```

**`edit_file`** takes that hash as `expect_hash`, plus a list of exact
replacements. It reports the new hash, so a follow-up edit needs no re-read.

There are three refusals. Each comes back as an `IsError` result rather than
a dispatch error, so the model gets it back and can correct, and the turn
doesn't abort:

```
notes.md: file changed since it was read: expected dcf19a8dce4a, found 9dd516a4fd95; re-read it before editing
notes.md: hunk 0: anchor is not unique: "todo: write the" matches 2 places; extend it until it matches one
notes.md: hunk 0: anchor not found: "todo: write the middle"
```

Matching is byte-exact. There is no fuzzy or whitespace-normalized fallback.
Approximate matching produces silently wrong edits, and this package exists to
prevent those. When an anchor does not match, the model gets an error and a
chance to look again.

All edits in one call apply together or none do, and every anchor resolves
against the original content rather than against the result of earlier edits.
So listing order cannot change the outcome. If an earlier edit introduces
some text, a later edit cannot match it.

## Multi-file patches (`apply_patch`)

Some models are trained to write changes as a patch envelope and do measurably
worse when made to use anchored replacements. `apply_patch` takes that format as
one `input` string:

```
*** Begin Patch
*** Update File: stats/stats.go
@@ func Sum(xs []int) int {
-	for i := 1; i < len(xs); i++ {
+	for i := 0; i < len(xs); i++ {
*** Add File: stats/doc.go
+// Package stats has small helpers.
+package stats
*** Delete File: stats/old.go
*** End Patch
```

An update can rename with `*** Move to: new/path`, and `*** End of File` anchors
a hunk to the end. `apply_patch` finds each hunk by its context lines. It tries
an exact match first, then ignores trailing whitespace, then ignores whitespace
at both ends. An `@@ text` header finds that line first and searches after it.

The whole patch applies or none of it does. Every file's new content is worked
out before anything is written. So the call is refused with nothing changed if
a hunk doesn't match, if a path is outside the roots, or if a file is in the
wrong state (adding one that exists, updating one that doesn't). If a write
fails part-way, the files already written are put back. There is no
`expect_hash`. The context lines act as the anchor, so a stale file shows up as
a hunk that doesn't match.

`Config.EditTools` picks `edit_file`, `apply_patch`, or both (the default), and
the prompt section only describes the tools offered. Checkpoint snapshots every
file a patch touches, both ends of a move included, so one `/undo` reverts the
whole patch.

## Creating and replacing whole files

`edit_file` needs existing content to anchor against. So `write_file` creates
a file or replaces one outright. It applies the same discipline through
`expect_hash` rather than through a separate flag:

| Call | Meaning |
|---|---|
| no `expect_hash` | create; **refused** if the path already exists |
| `expect_hash` set | replace, only if the content still hashes to it |

There is deliberately no way to spell "write this, whatever is there".
`edit_file` exists to close a hole, and a create-or-overwrite mode would reopen
it. A model would also reach for that mode the moment anchoring felt awkward.

```
$ write_file {"path": "notes.md", "content": "..."}
  [IsError] refusing notes.md: it already exists. Read it first and pass
            expect_hash to replace it, or use edit_file to change part of it.
```

Missing parent directories are created. Replacing a file keeps its mode, so
editing a script does not disarm it.

`delete_file` removes a file, and always needs `expect_hash`. A create has a
harmless mode, since the path must not exist yet. A delete has no harmless
mode, so it only removes content that still hashes to what the model read. It
refuses directories and symlinks, and it gets the same approval prompt as the
writes. Emptying a file through `edit_file` isn't a substitute, since a
zero-byte `.go` file breaks the build (issue 108).

## Finding things

`list_files` walks the workspace; `search_files` matches lines and returns
them as `path:line: text`.

```
$ list_files {"dir": "agent/ext"}
agent/ext/files/edit.go
agent/ext/files/tools.go
...

$ search_files {"query": "func Get\\w+"}
a.go:12: func GetUser() (*User, error) {
a.go:31: func GetOrder(id string) (*Order, error) {
```

**The query is a regex** (RE2, so no catastrophic backtracking). Pass
`literal: true` for text containing `( ) [ ] . * + ? | \ $`. If a pattern does
not compile, the call comes back as a refusal naming the pattern, never as
zero matches. You act differently on "no matches" and on "your pattern was
malformed". If the second were reported as the first, you would go looking for
code that is sitting right there.

**Both tools say what they left out.** If a capped listing didn't say it was
capped, it would read as the complete contents of a directory. So each tool
reports the cap, the directories it skipped, and the files it would not search:

```
showing 100 of 4312 match(es); narrow the query or raise limit

3 file(s) not searched: 2 binary, 1 over 2 MiB, 0 unreadable
1 directory was skipped: node_modules
```

Those limits bound the work a call does. They are **not** a context-window
mechanism, and deliberately so. `agent.OffloadingSource` already stores
oversized tool results out of band behind a `read_tool_result` stub. A second
truncation mechanism here would only disagree with it.

`Config.Exclude` sets which directories are skipped, defaulting to
`DefaultExclude` (`.git`, `node_modules`, `vendor`, and similar). A nil slice
means the default, and an explicitly empty one means exclude nothing. It does
not read `.gitignore`, because that file answers a different question. It says
what should not be *committed*. A `.env` is ignored but is sometimes exactly
what you need, and a vendored dependency is committed but is usually noise.

Traversal uses the same root handle as everything else, so it cannot leave the
workspace. A symlinked directory is listed as a name but never descended into.
So a link cycle cannot hang a walk either.

## Wiring it into a host

```go
ext, _ := files.New(files.Config{Roots: []string{"/work/project"}})
app, _ := host.NewApp(cfg, out, in, host.WithExtension(ext))
```

The extension contributes the tools and a prompt section stating the rules the
tools enforce. Those travel together on purpose. Suppose a model has not been
told that anchors must be unique. It will keep sending single-word anchors and
keep being refused. So if the tools shipped alone, the model could only
discover the contract by failing.

`files.NewSource` returns the bare `agent.ToolSource` instead, for a `Runner`
with no `host`. Prefer `New` wherever there is a host, for the reason
above.

To get `/undo` over these writes, pair it with `ext/checkpoint`. Register
checkpoint **first**, so it takes its snapshot before the write happens.
`surfaces.WorkspaceExtensions` does that pairing, and both
`agentchat --workspace` and `agentweb --workspace` use it.

## What the approval prompt shows

When a write is gated, the host asks before it runs. By default it renders the
call's JSON trimmed to 200 characters. For an edit, that cuts off the change,
and you are being asked to judge the change:

```
Allow tool call "edit_file" with {"path":"main.go","expect_hash":"dcf19a8dce4a"…
```

This extension renders its own writes instead, so the question is about the
change rather than the arguments:

```
Apply 2 change(s) to main.go?

  - func Old() {}
  + func New() {}

  - return nil
  + return fmt.Errorf("not implemented")
```

A whole-file write shows a capped preview and says whether it creates or
replaces, since those differ by whether anything is destroyed. Any call this
package does not own falls through to the host default untouched.

An edit with two or more hunks is asked about one hunk at a time, in a single
form with every change ticked to start with. On the terminal that reads:

```
? Apply 3 changes to main.go? Untick any you don't want.

Change 1:
  - alpha
  + ALPHA
...
  Apply change 1 (y/n): y
  Apply change 2 (y/n): n
  Apply change 3 (y/n): y
```

Only the ticked hunks are sent to `edit_file`. Every anchor resolves against
the original content, so each ticked hunk applies exactly where it would have
with the others present. The model's tool result ends with a note naming the
changes the user left out. A one-hunk edit still gets the plain yes/no
question.

## Undo, and why there is no Reverser here

An edit is checkpointable through `ext/checkpoint`, but this package
contains no reversal code and does not import that one:

```go
cp, _ := checkpoint.New(checkpoint.Config{
    Root:   ".mcpkit/checkpoints",
    Writes: []checkpoint.WriteSpec{
        {Tool: "edit_file", Paths: ext.WritePaths},
        {Tool: "write_file", Paths: ext.WritePaths},
        {Tool: "delete_file", Paths: ext.WritePaths},
    },
})
// Checkpoint first, so it snapshots before the write rather than after.
app, _ := host.NewApp(cfg, out, in, host.WithExtension(cp, ext))
```

`checkpoint.WriteSpec` is a declaration keyed by tool name, and the wiring
supplies it. So the two features compose at the config layer, with no import
edge in either direction. Every writing tool here names its target in the same
`path` argument, so one `WritePaths` serves them all. `WritePaths` resolves
that argument against the workspace roots the way the tools do, and returns an
absolute path. It has to, because the checkpoint refuses a relative one.
Resolving against the process's working directory snapshotted the wrong file
(issue 69). `PathArg` still returns the raw argument for anything that wants
it. Both are plain functions rather than a `WriteSpec`, precisely so that
neither package imports the other. The alternative was for this package to
declare its own `checkpoint.Reverser`. That would stabilize checkpoint's API
for this package's benefit, and no design decision says the two must
interoperate. Constraint C4 exists to prevent that kind of coupling.

## Status

Anchored edit engine, seven tools (`read_file`, `edit_file`, `apply_patch`,
`write_file`, `delete_file`, `list_files`, `search_files`), and the host
extension.
