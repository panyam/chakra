# exec

Running project commands from an agent, where the operator names the commands
and the model chooses among them.

## What the model may name

Nothing. That is the whole design.

The operator writes an allowlist in `Config.Commands`, and each entry becomes
its own tool. The model picks a tool. No parameter anywhere carries a command, a
binary, a flag string, or a path to a script. A model's instructions can come
from content it read rather than from the user, and nobody can tell the two
apart after the fact. `agent.Spotlight` exists for that reason. If an agent can
name the process it starts, one injected instruction can make it start anything.

```go
src, _ := exec.NewSource(exec.Config{
    Roots: []string{"/work/mcpkit"},
    Commands: []exec.CommandSpec{{
        Name:        "test",
        Argv:        []string{"go", "test"},
        Description: "Run the Go tests.",
        Args:        &exec.ArgPolicy{Max: 3, Match: `\./[\w./-]*(\.\.\.)?`, Paths: true},
        ReadOnly:    true,
    }, {
        Name:        "build",
        Argv:        []string{"go", "build", "./..."},
        Description: "Compile every package.",
        Reversible:  true,
    }},
})
```

That registers `run_test` and `run_build`. Nothing is passed to a shell, so an
argument containing `;` or `$(` is an argument.

## One tool per command, not one tool taking a name

The obvious shape is a single `run_command` whose `name` parameter is an enum
over the allowlist. It contains the model equally well, but it cannot express
what an operator actually wants. An operator wants `go build` to run unattended
while `terraform apply` asks first.

Safety annotations are per tool. Collapsed into one tool, every command shares
one `destructiveHint`, and `agent.ModeReversibleAuto` either prompts for all of
them or none.

| Spec field | Annotation | Effect on the approval ladder |
|---|---|---|
| `ReadOnly` | `readOnlyHint: true` | auto-allowed from `ModeReadOnlyAuto` up |
| `Reversible` | `destructiveHint: false` | auto-allowed from `ModeReversibleAuto` up |
| neither | nothing declared | asked under every mode below `ModeAlwaysAllow` |

Declaring nothing means destructive, because the spec reads an absent
`destructiveHint` as destructive. If nobody classified a command, the gate asks
about it. The operator wrote the allowlist and knows which entries are safe to
run unattended. A default that guessed for them would guess in the permissive
direction.

## The shell tool: one tool, judged per call (#154)

`Config.Shell` adds a single `shell` tool that takes an argv, as the alternative
to listing every command. It gets around the per-tool annotation problem above
with an `ApprovalClassifier` (#153). The gate asks the extension to judge each
call from its arguments, so one tool can let `git status` run while asking about
`git push`.

Prefix rules decide. Each position of a prefix lists the words accepted there;
the longest matching prefix wins, and the stricter decision wins a tie.

| Rule decision | Gate disposition | Remembered as |
|---|---|---|
| `allow` | read-only: runs unasked from `ModeReadOnlyAuto` up | the matched prefix |
| `prompt` | destructive: asks below `ModeAlwaysAllow` | the matched prefix |
| `forbidden` | refused under every mode, with the justification | never |
| no rule | the tool's destructive default | the first two words |
| `escalate: true` | always asks, under every mode, outside the sandbox | never |

A rule's `match` and `notMatch` examples are checked at load. If a rule doesn't
do what its author meant, it fails at startup.

`["bash", "-lc", script]` is split on `&&`, `||`, `;` and `|`, and every piece
has to pass; a forbidden piece forbids the whole call. The splitter gives up on
anything it doesn't fully understand (redirection, `$`, backticks, subshells,
globs, `~`, environment assignments, comments, backslashes). When it gives up on
a script, the script matches no rule, so the gate asks about it. That direction
is deliberate. If the splitter misread a construct, no rule ever saw the command
inside it.

Commands run in the sandbox. When one fails with output that looks like a
sandbox block (`Operation not permitted`, `Read-only file system`, or a
network error while the network is off), the result says so and tells the model
it can retry with `escalate` and a justification. The retry is a new call, so
it goes through the gate again, always asks, and is never remembered. The
escalated run skips the sandbox but keeps the workspace directory, timeout and
output caps. A forbidden rule still refuses an escalated call.

`argv[0]` is looked up on the launching process's PATH at call time, not at
construction as for allowlisted commands.

## Commands that keep running (#155)

`ShellConfig.Processes` adds four tools for commands that outlive one call: dev servers,
watchers, programs that wait for input. They're named as Codex names them, so a model trained
on Codex calls them without being taught:

| Tool | Does | Judged as |
|---|---|---|
| `exec_command {cmd, workdir, yield_ms, tty, detach}` | runs `cmd` through `bash -lc`; returns what it printed within `yield_ms`, or a `session_id` if it's still running | the shell's prefix rules, script by script |
| `write_stdin {session_id, chars, yield_ms}` | types `chars`, or with none just collects new output | read-only when polling; otherwise asks (it types into a running program), remembered per process |
| `list_processes` | what is running, and what finished but hasn't been read | read-only |
| `kill_process {session_id}` | stops it and everything it started | reversible |

Processes run under the same sandbox as `shell`, and `tty: true` gives one a pseudo-terminal
(`github.com/creack/pty`). They belong to the session, not the turn, so a server started in one
turn is still there in the next, and `Extension.Close` kills them all. Each call returns only the
output since the last one, cut from the middle past 16 KiB. A process keeps at most `MaxOutput`
bytes (1 MiB), and a read says how much older output it missed. At most `Max` (8) run at once.

While a process runs, its output also streams to the surfaces as it prints (#214), so agentweb's
Tools panel and agentchat show a dev server or a build between the model's polls. The extension
implements `host.ProcessReporter`. It sends output in batches, every 250ms or at 8 KiB, and each
batch becomes a `process-output` event keyed by the process's `session_id`. The last batch carries
the exit code. None of it reaches the model, whose results are the same with or without a surface
watching. Streaming pauses after `StreamBudget` bytes per process (4 MiB; negative for no limit),
because the host keeps every event in its log and a server left running would grow it without end.
`MaxEventLogRetention` bounds the log by count across all kinds, so it can't protect a session's
turns from a noisy process the way a per-process budget does.

Some tasks want a server left running after the agent finishes, such as a check that
connects to it later. A process killed by `Close` can't do that. So `AllowDetach` adds a
`detach` argument to `exec_command` (#228). A detached command starts in its own session and
reads stdin from `/dev/null`. It writes its output to a log file in the system temp dir under
`chakra-detached/`, and the log stays out of the workspace so it doesn't appear among the
task's files. `Close` leaves the command running. It still runs under the sandbox. While the
session lasts, `write_stdin` with no chars reads the log, and `kill_process` stops it. It
doesn't stream to the surfaces. Every detached call asks for approval, whatever the prefix
rules say, unless the rules forbid the command outright. The ask says the command keeps
running after the session ends. The network proxy closes with the session, so a detached
command loses network access through it at that point.

In a workspace file: `"exec": {"shell": {"processes": true}}`, or an object with `max`,
`maxOutput`, `defaultYield`, `maxYield`, `streamBudget` and `detach`.

## Network by host, through a local proxy (#158)

`AllowNetwork` is all or nothing. `Config.Network` sits between. Commands can reach the hosts it
lists, and nothing else beyond loopback.

```json
"exec": {"network": {"allow": ["proxy.golang.org", "sum.golang.org", "*.npmjs.org"], "ask": true}}
```

The extension starts a proxy on `127.0.0.1`. It speaks HTTP, including `CONNECT` for HTTPS, and
SOCKS5 on one port. Every command gets `HTTP_PROXY`, `HTTPS_PROXY` and `ALL_PROXY`, with `NO_PROXY`
for loopback, and that covers `run_*`, `shell` and `exec_command` alike. The sandbox already
confines a command's network to loopback, so the proxy is its only way out, and the proxy decides
per host. `*.npmjs.org` covers subdomains, not the apex.

With `ask`, a connection to an unlisted host becomes a question for the user, through the same
queue as approval asks (`host.Asker`), and the answer holds for the session. Without `ask`, or with
nobody to answer (headless `exec`), it's refused, and the conversation gets a line naming the host
(`host.Announcer`) so the user can see why a command failed.

Limits:

- Without a sandbox (`exec.Unconfined()`, or Linux until #32), commands can ignore the proxy
  variables, so the allowlist is advice rather than enforcement there.
- `Network` and `AllowNetwork` can't both be set; `AllowNetwork` already lets everything out.
- A read-only mode (GET and HEAD only) isn't offered, because the proxy can't see methods inside an
  HTTPS tunnel without decrypting it.

## Arguments, when they are permitted at all

`Args` is nil by default and the tool then exposes no argument. If a command
accepts free-form arguments, its allowlist entry promises less than it appears
to.

When set, `Match` is required and is anchored end to end. An unanchored pattern
matches substrings. `\./\w+` would accept `./pkg;rm -rf ~` because of the
`./pkg` inside it. `Paths` additionally requires every argument to resolve
inside a root, through symlinks. It applies to every argument, not only to the
ones that look like paths, so a command that mixes flags and paths cannot use
it.

Path checking resolves symlinks on the longest existing part of the path and
keeps the rest. Requiring the whole path to exist would reject `./agent/...`,
which names no file; skipping symlinks would accept a link inside a root
pointing out of one.

## The sandbox

`Config.Sandbox` confines every command. Nil selects the platform's backend and
**fails construction when there is none**, rather than running unconfined.

| Platform | Backend |
|---|---|
| darwin | `sandbox-exec`, a generated Seatbelt profile |
| everything else | none; construction refuses |

`exec.Unconfined()` is the opt-out and has to be written in the config. It is
the right setting when an operator already isolated the process, and that is the
ordinary deployed case. The operator built a container, the container is the
boundary, and a second fence inside it protects nothing. The design stops you
selecting it by accident. It doesn't stop you selecting it on purpose.

Linux has no backend yet. Delegating to `bubblewrap` is the obvious shape, but
it could not be verified here. CI runs on `ubuntu-latest`, and there the
AppArmor restriction on unprivileged user namespaces can block `bwrap` outright.
So a bubblewrap backend would have shipped less proven than the darwin path.
Issue #32 starts by establishing what a runner actually permits, before writing
anything.

### What the profile does

- **Writes** are denied, then opened onto the roots and `WritePaths`. A path
  missing from that list fails loudly, which is the direction to fail in.
- **Reads** are open, then carved back by `DenyRead`. Confining reads to the
  workspace breaks every toolchain, which reads compilers, SDKs, module caches
  and system libraries from all over the disk, and the breakage reads as a bug
  rather than as policy. With reads open, the remaining risk is credential
  theft. The default denials address it directly, though not exhaustively.
- **Network** is loopback only unless a command sets `AllowNetwork`. The
  ordinary case is a test suite that starts a server on `127.0.0.1`. Egress
  would let a build script post the workspace somewhere. Outbound connections
  are limited to loopback. Listening is not quite limited, because Seatbelt's
  `localhost` filter also matches a wildcard bind, so a command can listen on
  `0.0.0.0`.

`DefaultWritePaths` covers the temp directory and the build and module caches,
because a sandboxed `go build` fails on its build cache before it compiles
anything. That widens confinement past the roots. So under the defaults,
"outside every root" and "denied" are different statements.
`WritePaths: []string{}` is the roots-only setting.

Paths in the profile are symlink-resolved and emitted in both spellings.
Seatbelt matches the resolved path. On darwin `/tmp` links to `/private/tmp`, so
a rule naming `/tmp` covers nothing there. Such a read denial looks in force but
permits the read, and that is worse than having no denial at all.

## Why the sandbox is here and not around a `ToolSource`

The coding epic routed sandboxed execution to a `ToolSource` wrapper. A wrapper
has nothing to confine. Most tools in this tree are in-process Go functions or
MCP calls with no subprocess in them, and isolating those would mean isolating
the agent process, which is a deployment decision. Only the code that spawns
can confine what it spawned.

**Extension-owned subprocesses are out of scope, deliberately.** `ext/lsp`
spawns `gopls` from `ServerSpec.Command`, outside the tool path, where no
wrapper reaches it. That command is operator-supplied, and no tool argument can
reach it. `client.CommandTransport` has the same property, and that property
makes it a sanctioned exception under C6. A convention could require every
extension to route its spawns through a shared helper, but that would be
enforcement in name only. Recorded on issue 1312.

## What the sandbox is not

It is not the primary defense. The approval gate is. Under `ModeAlwaysAllow` and
the auto modes these commands run unattended, so the sandbox is needed there. It
also keeps the allowlist honest.

`sandbox-exec` has also been deprecated for years with an undocumented profile
language. The golden test pins what we generate. It cannot pin whether Apple
keeps honoring it. Only the live suite checks that half.

### The allowlist names commands, not capabilities

Understand this part before trusting an allowlist. It goes deeper than a
composition problem between two extensions.

**Most useful entries execute workspace code by design.** `make test` runs
whatever the Makefile says. `go test` compiles and runs test code from the repo.
`npm run build` runs the scripts in `package.json`. So the allowlist constrains
which *program* starts. Files in the workspace define what that program does, so
for any real build command the allowlist says almost nothing about its
behaviour.

`ext/files` makes this sharper by letting the agent rewrite those files first,
but the property holds without it. A repository is content. If you run a build
in a repository you did not write, you run that repository's code. That is true
of a cloned dependency and of a PR branch under review, with no model involved
at all.

**A generic binary with a loose `ArgPolicy` is close to a shell.** `git` with
`Match: ".*"` reaches `-c core.pager=`, aliases, and hooks. Nothing here
detects that: `Match` is required to be non-empty and `.*` satisfies it. Keep
patterns narrow, and prefer a specific subcommand in `Argv` over a bare binary
plus permissive arguments.

So for a test or build loop the allowlist states *intent*, useful for review and for the approval
prompt. The sandbox does the containing. Don't treat the allowlist as the boundary. This section
exists to prevent that mistake.

## Reading the result

A non-zero exit is a normal result with `exit_code` set, not a tool error. The
point of running a build is to learn that it failed. If the tool flagged that as
an error, the model would think the tool malfunctioned rather than the code.

A timeout is a tool error, because the command did not finish and nothing it
printed is a verdict. The command runs in its own process group and the whole
group is killed, since a command is normally a launcher whose children do the
work and outlive it.

Output is cut from the **middle**, at two sizes. `MaxOutput` (1 MiB a stream)
caps what a command may hold in memory, as a guard against a runaway process.
`InlineOutput` (32 KiB a stream) caps what the result carries, because a failing
build usually states the problem in the first lines or the last ones. A cut
result says how many bytes went missing. Without that count, a model can't tell
a truncated run from a complete one, and it reads a passing tail as a passing
run.

A failing assertion tends to sit in the middle of a long `go test` run (#59),
and the 32 KiB view loses it there. A host that offloads large results
sets `InlineOutput` negative, so the whole capture reaches the store and the
model greps it with `read_tool_result`. A timed-out run keeps the short view
either way, since offloading leaves error results inline.

## Wiring it into a host

```go
ext, err := exec.New(exec.Config{Roots: roots, Commands: commands})
if err != nil {
    return err
}
app, err := host.NewApp(cfg, host.WithExtensions(ext))
```

The approval prompt shows the resolved command line, the directory, and what is
confining it. The host default renders the call's JSON trimmed to 200
characters. For these tools that JSON is an argument array, and it shows no sign
of which command it belongs to.

```
Run test?

  go test ./...
  in /work/mcpkit
  confined by sandbox-exec, no network
```

## Tests

`go test ./...` needs nothing installed. The commands are this test binary,
re-executed, the way `ext/lsp` drives its stub server.

`go test -tags exec_live ./...` runs the real `sandbox-exec` and is darwin-only.
CI runs on Linux, so **nothing in CI exercises a real sandbox**. The live suite
caught the symlink-resolution bug above. Run it before believing a change to the
profile.

## Status

Experimental, on the unreleased agent line. Issue 1312; child of the coding
epic 1252. The Linux backend is chakra #32. Issue 1313 is the test and build feedback
loop, which consumes this. Two issues hold open questions about the tool set
itself. Issue 1323 asks what happens when an allowlist gets large. Issue 1324
asks whether the allowlist may change after construction, and who is allowed to
widen it.
