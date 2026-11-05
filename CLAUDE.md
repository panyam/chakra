# chakra — read before editing here

The agent SDK. It sits **above** the MCP protocol layer and consumes mcpkit as an ordinary
dependency. It is not part of the MCP specification surface.

The tree lived in `panyam/mcpkit` under `chakra` and then `` before moving here.
History came across, so `git log` and `git blame` reach back through both moves. Issue numbers in
commit messages and docs below 1330 or so refer to `panyam/mcpkit`.

## Where things are

| Need | File |
|---|---|
| Enforceable invariants (A1–A12) | `CONSTRAINTS.md` |
| Why the code is shaped this way, what bit us | `NOTES.md` |
| Terminal surface lore (TUI, notebook, overlays) | `surfaces/chat/NOTES.md` |
| Design frames | `docs/AGENT_DESIGN.md`, `docs/AGENT_COMPOSITION.md`, `docs/AGENT_MEMORY_FLOW.md` |
| Roadmap and phase status | `docs/AGENT_SDK_ROADMAP.md` |
| How to use the host / the CLI | `host/README.md`, `surfaces/chat/README.md` |
| Where a new behavior hooks in (turn start, each model call, each tool call, turn end, events) | `docs/HOOKS.md` |

Screenshots for a PR body go on the orphan `pr-assets` branch under `<PR number>/` and are linked
by their `raw.githubusercontent.com` URL (GitHub has no CLI upload, and PNG keeps them visible).

**Every PR says at the top which hook it uses and why**, in a short `Hook:` line before the rest of
the body, using the points in `docs/HOOKS.md`. If a change needs a hook point missing from the page,
the PR says so as its main decision and updates the page in the same PR.

## Issues and missions

Work is ranked by which mission it serves, not by how recently it was filed. The active mission is
#162 (`mission_coding_harness`). It builds a second coding agent only by rewiring chakra, with
different tools, hooks, policies and provider and no new harness. It is agentweb over the profile in
`surfaces/coding`, so there is no TUI to fight. `examples/coding-harness/profile.go` adds the
exercise's scripted model, recorder and step routing, and `agentweb --coding` serves the same
profile at `/code/` (`make coding-harness-web`). Its exercise is
`make coding-harness-exercise` (#163). The exercise drives the agent over agentweb's Connect API on
a fixture repo with a failing test, and prints PASS or FAIL per behaviour. Known failures are pinned
in `examples/coding-harness/expected.go`. The `-live` variant also prints steps, tokens, wall time
and whether the tests passed. `make coding-harness-parity` runs `codex exec` beside it on the same
fixture and model. Run the live and parity targets after each merge on the mission and log both
tables on #162.
#142 (`mission_api_v1`) is paused, not closed. Its goal is a chakra 1.0 with a reviewed, settled
exported API. Its exercise is `make api-report` (#143), which diffs each module's exported symbols
against a checked-in `api.txt`. The earlier missions were #57 (`mission_dogfood_coding`,
`make dogfood`) and #89 (`mission_host_completion`, `make host-exercise`). Both closed when their
exercises passed, and both exercises still run.
#172 (`mission_jaala_translator`) closed on 2026-11-03 when panyam/jaala#496 moved jaala's
translator onto the Runner. Its exercise lives in jaala.

A new issue gets one of `P0`–`P3` (P1 is capped at 5 per mission), plus either a `mission_<slug>`
label with a blocked-by link from the mission, or `waiting` with its trigger named in the body.
Trackers are `epic`. `~/.claude/skills/retriage/queue.sh` computes the order.

## The invariants that cause wrong edits when missed

Full text in `CONSTRAINTS.md`. People violate these five by accident:

- **A6 — mechanism in the client, policy in the agent.** A primitive belongs in `client/` if any
  non-agent consumer would want it (a script, a service, a dashboard poller). It belongs here
  only if it needs a model and a turn to make sense. The tell is the return type: a protocol
  object is client-layer, a model-facing object is agent-layer.
- **A2 — Runner events are wire-serializable.** Scope, depth, and other envelope metadata go on the
  envelope, never inside `Event`. So `agent.Message` can't carry a provider-specific opaque blob,
  such as an Anthropic signed thinking block.
- **A7 — sub-agents get no ambient parent state.** Memory is not shared downward. A child that
  needs memory owns its own store and namespace entirely. Guarded by
  `TestSubAgentCannotReachParentMemory`.
- **A9 — the provider seam exposes loop-visible capabilities only.** Loop-invisible provider
  optimizations, such as prompt caching and extended thinking, are left out of the seam. If one is
  ever genuinely needed, wrap the vendor SDK behind the seam.
- **A10 — dependency weight decides module boundaries.** The root `go.mod` has exactly two direct
  requires and must keep exactly two. An implementation belongs beside its interface here when it
  costs no third-party dependency, and in a satellite module (`store/redis`, `store/gorm`) when it
  needs a driver or SDK. Seam interfaces live with the Runner, which consumes them. Do not create an
  interface-only package.

Also: **A8 rules out building a workflow engine here.** Orchestration is model-driven or
integrated with a dedicated engine.

## Traps

- **Builds depend on the checkout layout.** Every `go.mod` replaces mcpkit with a relative path to
  `~/newstack/mcpkit/main`, written for a checkout at `~/newstack/chakra/<branch>` (this one is
  `chakra/main`). A worktree has to sit at the same depth, e.g. `git worktree add
  ~/newstack/chakra/feat-x`, or nothing resolves. CI recreates the layout by checking out
  `chakra/main` and `mcpkit/main` side by side. The replace is invisible to importers, so tag mcpkit
  and repoint the requires before tagging chakra. When mcpkit main changes its own requires, every
  go.mod here goes stale and main goes red. `make sync-mcpkit` fixes it, and
  `.github/workflows/tidy.yml` opens that fix as a PR every six hours once the new pins are tagged.
- **Background goroutines use `core.DetachForBackground(ctx)`, never `context.WithoutCancel`.** Some
  background work outlives a turn *and* calls MCP server tools. That work needs the session-level
  persistent push. Applies to async sub-agents, the agent pool, and task dispatch.
- **CI fails on any change to an exported name until `api.txt` records it.** `make api-report`
  diffs each module's exported API against its `api.txt` (#143). Run `make api-update` and commit
  the diff with the change. Doc comments and parameter names don't count.
- **Run tests with `-race`.** The signal, pool, and interruptible tests exist to catch races.
- **Host behavioral tests race on a shared sequential StubProvider** when a main turn and a
  background child both pull from it. Assert *wiring* in host tests and test the *behavior* at
  the agent layer with isolated per-child providers.
- **Memory injection never writes into `a.history`.** Summary and recall are transient per-turn
  producers; appending them to history stacks them up in both history and the RunStore log.
- **Adding a module is one edit: `MODULES` in the `Makefile`.** CI runs `make test` and
  `make test-examples` rather than restating the list, so the two cannot drift. CI used to hardcode
  a step per module. That ordering produced a failure mode worth remembering even now that it is
  fixed. A module missing from the workflow builds fine locally and never runs in CI, so the gap
  reports itself as a pass.
- **A tool's safety annotations are per `ToolDef`; per-call dispositions need a classifier.**
  `toolHints` resolves `readOnlyHint` / `destructiveHint` by tool name, so one tool covering N
  operations gives all N the same disposition unless its extension supplies an `ApprovalClassifier`,
  which the gate consults on the final arguments (#153). Extension middleware can't do this job. It
  runs *before* the gate, and anything it sets on `ToolCallInfo` is advisory. `ext/exec` has both
  shapes. It offers one `run_*` tool per allowlisted command, judged by annotations, and the opt-in
  `shell` tool, judged per call by prefix rules.
- **Tools are re-listed every step**, not once per turn, and `RunnerConfig.Selector` narrows the
  offered set per step. A context-varying tool set is already supported, and needs nothing new.
- **An exec command's `argv[0]` resolves from the launching process, not the workspace root.**
  `exec` looks it up on PATH at construction, and `shell` looks it up on the same PATH per call. So
  `scripts/foo.sh` only works if agentchat was started in the repo. Write `["sh", "scripts/foo.sh"]`
  instead. `sh` is on PATH, and the script path is an argument, resolved against the command's `Dir`
  (the first root).
- **CI never runs `-tags eval_llm`.** A judge scorer tested only under that tag is untested. Keep the
  scorer untagged and test it with a stub, and tag only the choice to grade with a model (see
  NOTES.md, Eval).
- **A pty behaves differently on macOS and Linux**, so an `ext/exec` tty test can pass locally and
  fail in CI (NOTES.md, Testing). Run that module on Linux with Docker, from the worktree:
  `docker run --rm --init -v ~/newstack:/src/newstack:ro -v ~/go/pkg/mod:/go/pkg/mod:ro -e GOFLAGS=-mod=mod
  -e GOPROXY=off -w /src/newstack/chakra/<branch>/ext/exec golang:1.26 go test -race ./...`
  Keep `--init`. Without it nothing reaps orphans in the container, and
  `TestATimeoutKillsTheWholeProcessTree` sees a zombie grandchild as alive and fails.
- **A sandbox profile is only checked by a tagged live test.** `ext/exec`'s golden test covers
  profile generation and runs anywhere. CI is Linux and the backend is darwin-only, so **nothing in
  CI exercises a real sandbox**. Run `go test -tags exec_live ./...` in that module before believing
  a profile change. The live test caught two real bugs, and the golden test could not have caught
  them.
