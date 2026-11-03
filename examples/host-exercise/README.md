# host-exercise

Mission #89's exercise. It runs a `host.App` against one demo MCP server. The
server declares each thing chakra says it hosts: server instructions, skills,
an event type with a context hint, and an agent. The exercise checks that the
host does what each spec asks of it. A scripted model plays the conversation,
so the exercise needs no model or API key. Every check runs on both client
wires (`MCPKIT_CLIENT_MODE=legacy` and `stateless`).

```
make host-exercise
```

prints one line per check and wire:

```
PASS  legacy     instructions     present, labelled with server "demo"
FAIL  legacy     no-prefetch      3 SKILL.md read(s) at connect, before any load_skill call  expected, #50
...
5 check(s) x 2 wire(s), 0 unexpected
```

It exits non-zero only when a result differs from `expected.go`, in either
direction. CI runs it through `make test-examples`.

## The checks

| id | obligation | how it's checked |
|---|---|---|
| `instructions` | the server's `instructions` reach the system prompt, labelled with their origin | the system prompt the model received |
| `no-prefetch` | in the host's default skills mode, no `SKILL.md` is fetched before the model asks for it (SEP-2640) | after connect, the server counts `resources/read` calls for `SKILL.md` |
| `skill-rotation` | approving a skill is bound to its content, so a rotated skill asks again (SEP-2640) | `load_skill` approved with `remember` on, the skill's body changes on the server, `load_skill` again, and the approval prompts are counted |
| `event-hint` | an event type's `_meta` context hint applies without a config override | a `build.finished` occurrence has to reach the model through the server's template |
| `agent-delegable` | the host can delegate to an agent the server advertises | the model calls the `researcher` tool and gets the child agent's answer back |
| `roots` | when a server asks `roots/list`, it sees the workspace roots (#159) | the host is given `.` as its root, the model calls `workspace_roots`, and the server asks `roots/list` as SEP-2322 input and echoes the URIs |

`TestChecksCanTellPassFromFail` flips each check with a config change that
should change its answer. For example, it uses catalog mode for `no-prefetch`
and puts the hint in config for `event-hint`. So if a check can only ever pass
or only ever fail, the test catches it.

## When a check starts passing

Remove its lines from `expected.go` in the PR that fixed it. The run fails
until you do. That is deliberate, since the diff to `expected.go` records the
mission's progress. If a check starts failing, that is a regression, unless the
change was meant to tighten it.

## Looking at it with a real model

```
make host-exercise-live HOST_EXERCISE_ACTIVE=deepseek   # or claude, local
```

boots the same server on `:8790` (with a `build.finished` event every 30s) and
starts `agentchat --ui plain` against it with `agentchat.json`. Some things to
try. `/tools` shows the `researcher` delegate. Asking for release notes should
go through `load_skill`. A few turns in, the build events should show up.
