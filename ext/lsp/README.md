# lsp

Language servers in the agent loop, so the model works against what the
compiler thinks rather than against what it remembers writing.

`ext/files` gave the agent byte-exact edits and `search_files`. Neither
knows what a symbol is. An agent renaming a method greps for the name, gets the
string matches, and cannot tell a call site from a comment that mentions it.

```go
ext, _ := lsp.New(lsp.Config{
    Roots: []string{"/work/mcpkit", "/work/mcpkit-contribs"},
    Servers: []lsp.ServerSpec{
        {Command: []string{"gopls"}, Extensions: []string{".go"}, LanguageID: "go"},
    },
    Writes: []lsp.WriteSpec{
        {Tool: "edit_file", Paths: fx.WritePaths}, // fx is the files.Extension
        {Tool: "write_file", Paths: fx.WritePaths},
    },
})
app, _ := host.NewApp(cfg, out, in, host.WithExtension(fx, ext))
```

`Command` comes from configuration and is deliberately unreachable from a tool
argument. A model's instructions can come from content it read. Suppose an
agent could name the subprocess it starts. It would then be one injected
instruction away from running anything.

## Diagnostics arrive twice, for two different questions

|  | Within a turn | Across turns |
|---|---|---|
| Mechanism | appended to the write's tool result | transient `ContextStage` |
| The claim | "this edit introduced these errors" | "the file currently has these errors" |
| Goes into history | yes, and is still true there later | never |

The split is the design, not an implementation detail.

A tool result joins the conversation permanently. That suits a statement about
what an edit *caused*, since a record of the past is still true later. It
doesn't suit a statement about *current state*. Imagine the model fixes one of
the errors. The current-state statement is now false, but it is still in the
conversation. Each edit adds another stale block, until the context holds ten
overlapping and mostly wrong descriptions of the same file.
`host.contextPipeline` already names that failure for memory injection.

So why not put everything in the stage? Because **stages run once per turn**, at
`RunTurn`, while an edit-check-fix loop runs across the tool-call steps *inside*
a turn. Suppose a model edits at step 3 and keeps working to step 12. With only
the stage, it would learn nothing for nine steps. Only the tool result reaches
it in time.

```
$ edit_file {"path": "cache.go", ...}
  wrote cache.go
  cache.go: 1 problem(s) after this edit:
    42:14: error: undefined: fmtt
```

The per-turn block reports only what is currently wrong, and says nothing at all
when nothing is. If an all-clear were repeated every turn, it would spend
context to say nothing and train the model to skip the section.

If a server does not answer in time, the result says so:

```
cache.go: the language server did not report back within 8s
```

Silence would read as "no problems". The diagnostics we happened to be holding
would then read as problems this edit caused, and the model would go fix
something it already fixed.

### The first publication is not always the answer

Most servers publish once with what they found. Some compute in two phases and
publish an **empty set immediately**, then the real diagnostics after a slow
pass. An empty set can mean "clean" or "not computed yet", and nothing in the
protocol distinguishes them. So if we took the first publication, a file that
does not compile would be reported as clean.

So an empty result waits a quiet period for a correction. While the server
reports itself busy through work-done progress, the wait is extended. A result
with problems in it returns at once. A later publication can only add more, and
the next turn's context stage reports the full set anyway.

Measured on a warm rust-analyzer over a small crate:

| Edit | Cost | Why |
|---|---|---|
| introduces an error | ~130ms | the result is actionable, no waiting |
| leaves the file clean | ~1.9s | an empty result has to outlast a possible correction |
| any edit, gopls / pyright / clangd | ~250ms | they publish once and report no busy work |

`ServerSpec.SettleDelay` tunes the quiet period. The busy extension is bounded
on purpose. Waiting for a busy server to fall quiet does not work.
rust-analyzer reports progress almost continuously, and with that approach
every write measured the full eight-second timeout.

### Saving matters as much as changing

A write sends `didChange` **and** `didSave`. Sending the save is honest,
because everything here reads the file from disk after it was written.

The save also matters. **rust-analyzer publishes nothing at all for a
`didChange`**, and its cargo check runs on save. Without the save, only the
first edit to a file got checked, and only because opening the document
happened to trigger a check. Every edit after that went unchecked.

If the server already has the content, it is not resent. So a navigation call
and a re-check over an unchanged file cost no round trip. That also matters for
clangd. clangd does not re-publish for a `didChange` carrying content it
already holds.

## The tools take a symbol name, not a position

LSP addresses every request by a cursor position, and a model has no cursor.
Asking it for one means asking it to count lines and columns out of `read_file`
output.

```
$ find_references {"path": "cache.go", "symbol": "CacheWrapper.Get"}
handler.go:31:18: 	v, err := c.Get(ctx, key)
worker.go:88:9: 	_ = cache.Get(ctx, id)
```

A bare name works when it is unique. When it is not, the call is refused rather
than resolved to a guess. `edit_file` already applies the same discipline to a
non-unique anchor:

```
cache.go: "Close" is ambiguous: it matches Reader.Close, Writer.Close. Use the qualified name
```

Names are matched on the bare form, the qualified form, and whatever decorated
spelling the server uses, since gopls reports a method as `(*CacheWrapper).Get`.

**LSP 3.17 does not remove the cursor.** Its `positionEncoding` negotiation only
changes how a column is counted, and no LSP version has a name-addressed
request.

Servers disagree about whether to negotiate at all, so both paths are live:

| Server | `positionEncoding` |
|---|---|
| gopls, typescript-language-server, pyright | omitted, so utf-16 |
| rust-analyzer, clangd | `utf-8` |

An omitted value means utf-16 per spec, so columns arrive in code units and a
line containing an emoji counts differently from its bytes. That conversion
lives in one function and no position ever reaches the model.

A location outside the workspace is named but not quoted. A definition in a
dependency is a real answer. But reading that file would reach outside the
roots, and the roots confine everything else.

## The repository map

`Config.RepoMap` adds a ranked summary of the tree to the system prompt, so the
model is oriented before it knows what to ask for. For the same reason, the map
is not a tool. A model has to know to call a tool, so a tool can't orient it.
If the model thinks to call the tool, it already has some idea what it is
looking at.

```
## Repository map

The files the rest of the code leans on most, and what they declare.
Ranked, not complete: use list_files and search_files for the rest.

/work/mcpkit/core/session.go
  Session, SessionStore, NewSession, and 9 more
/work/mcpkit/server/server.go
  Server, Dispatch, RegisterTool, ...

18 more file(s) not shown, ranked lower.
```

**Definitions come from the language server**, and edges are lexical. File A
references file B if A mentions a name that B declares, matched as a whole
identifier. Reference edges from the server would be more accurate, but they
cost a request per symbol. That adds up to thousands of round trips to order a
list nobody reads past the top of. Ranking is PageRank over that graph. A new
contributor first needs the files the rest of the code depends on most. That is
a question about the graph, not about file size or recency.

**It lives here rather than in its own extension.** The map needs the language
servers, and this extension already runs them. A separate module could not
import this one (constraint C4), so it would start a second `gopls` over the
same tree to ask the same questions.

Indexing runs in the background, and the section is empty until it finishes.
So an early turn is missing the map rather than waiting on it. Both caps, the token
budget and the file limit, report what they dropped.

## There is no `document_symbols` tool

The repo map already answers the file-outline question, and it does so across
the whole tree rather than one file at a time. Shipping both would give two
mechanisms for one question. The better one would arrive later and have to
argue against an incumbent. Symbol resolution calls
`textDocument/documentSymbol` internally, so the capability is here and only
the tool is withheld.

## One server covers every root

`Roots` matches `files.Config.Roots`, and **one server instance covers all of
them**. It does so through the `workspaceFolders` array, which the protocol has
had since 3.6.

We measured that rather than assumed it, and the measurement changed the
design. gopls, rust-analyzer and typescript-language-server each answer
correctly for a file in a folder other than the `rootUri`. So a session across
three repositories runs one gopls rather than three, with one index and one
cold start. `TestServersSpanTwoWorkspaceFolders` checks that against real
servers.

Paths are absolute, in and out, for the same reason as in `ext/files`. Two
repositories can both hold `src/main.go`, so a bare relative path could name
either. A relative path on the way in still resolves against the first root.

**Cross-language references remain out of reach.** A Go server cannot tell you
that a TypeScript file calls the endpoint you renamed. The language server sets
that boundary, not the number of roots. So a cross-language rename still takes
`search_files` and judgement.

## Every request is bounded

A request to a language server is a local IPC round trip, and a healthy server
answers it in milliseconds. Each request carries a 30-second backstop. If a
caller has a shorter deadline, that deadline still applies. The backstop never
extends one.

The backstop exists because nothing else bounded a request. Imagine a server
reads a request, never answers, and keeps its stdout open. The read loop has
nothing to fail on, so the "server exited" path never fires. The navigation
tools pass the Runner's context straight down, and that context often has no
deadline. So one wedged server could hold a turn open with no error anywhere to
explain it. Now a wedged server comes back as a refusal the model can act on.

## Closing

This is the first extension to own a subprocess. Nothing before it needed shutting down, so
`host.Extension` grew `Close`. `App.Close` calls it in reverse registration order. If a server
ignores `shutdown` and keeps running after its stdin closes, it is killed. A host that blocks
forever on Close is worse than one that leaves an orphan.

## Wiring it with the file tools

`surfaces.WorkspaceExtensions` does the pairing. It registers checkpoint, then
files, then this extension. The order matters in both directions. Checkpoint
must see a file *before* it is written, and this extension must see it
*after*.

```go
exts, _ := surfaces.WorkspaceExtensions(surfaces.WorkspaceConfig{
    Root:            "/work/project",
    LanguageServers: []lsp.ServerSpec{{Command: []string{"gopls"}, Extensions: []string{".go"}, LanguageID: "go"}},
})
```

There, language servers are opt-in and checkpoint is opt-out. A checkpoint
costs a directory, and you want one wherever there are writes. A language
server is a subprocess and an index of the whole tree. Nothing can guess which
languages a workspace holds or which server should drive them.

## Testing

The suite drives a stub language server. The stub is the test binary
re-executed, so the suite needs nothing installed and exercises spawn, framing,
and teardown for real. It proves nothing about whether gopls behaves the way
the stub does.

The `lsp_live` tag runs the same surface against real servers and is not wired
into CI:

```bash
go test -tags lsp_live ./ext/lsp/ -v
```

`TestLive*` drives one server, defaulting to `gopls` and overridable with
`LSP_LIVE_SERVER`. `TestServers` is the multi-language conformance probe. It
runs against gopls, typescript-language-server, pyright, rust-analyzer and
clangd. For each server it checks startup, `documentSymbol` decoding, and
symbol resolution by name. It also checks that a file which does not compile
produces a diagnostic. Each subtest skips when its binary is absent.

That probe found the provisional-publication bug (#1303). The bug shipped
because gopls was the only server anyone had tried. Adding a server to the
probe is the cheapest way to find the next assumption of that kind.

## Status

Stdio JSON-RPC client, diagnostics on both paths, two navigation tools, and the
host extension. There are no code actions. Applying a server-proposed edit is a
write. Writes go through `ext/files` for anchoring and `ext/checkpoint` for
reversal, and a code action would go around both.
