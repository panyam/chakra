# host-exercise

Mission #89's exercise. It runs a `host.App` against one demo MCP server that
declares the things chakra says it hosts (server instructions, skills, an
event type with a context hint, and an agent), and checks that the host does
what each spec asks of it. A scripted model plays the conversation, so it
needs no model or API key, and every check runs on both client wires
(`MCPKIT_CLIENT_MODE=legacy` and `stateless`).

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
| `no-prefetch` | no `SKILL.md` is fetched before the model asks for it (SEP-2640), in the host's default skills mode | the server counts `resources/read` calls for `SKILL.md` after connect |
| `skill-rotation` | approving a skill is bound to its content, so a rotated skill asks again (SEP-2640) | `load_skill` approved with `remember` on, the skill's body changes on the server, `load_skill` again, and the approval prompts are counted |
| `event-hint` | an event type's `_meta` context hint applies without a config override | a `build.finished` occurrence has to reach the model through the server's template |
| `agent-delegable` | an agent the server advertises can be delegated to | the model calls the `researcher` tool and gets the child agent's answer back |

`TestChecksCanTellPassFromFail` flips each check with a config change that
should change its answer (catalog mode for `no-prefetch`, the hint in config
for `event-hint`, and so on), so a check that can only ever pass or only ever
fail gets caught.

## When a check starts passing

Remove its lines from `expected.go` in the PR that fixed it. The run fails
until you do, which is deliberate, since that diff is how the mission records
progress. A check that starts failing is a regression, unless the change was
meant to tighten it.

## Looking at it with a real model

```
make host-exercise-live HOST_EXERCISE_ACTIVE=deepseek   # or claude, local
```

boots the same server on `:8790` (with a `build.finished` event every 30s) and
starts `agentchat --ui plain` against it with `agentchat.json`. Things to try:
`/tools` shows the `researcher` delegate, asking for release notes should go
through `load_skill`, and a few turns in, the build events should show up.
