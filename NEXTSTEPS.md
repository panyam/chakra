# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
/checkpoint maintains it, with one thread per branch, and prunes a thread when its branch merges.

## At a glance

- Mission #162 (`mission_coding_harness`) is active, with 29 of 32 tickets closed. The exercises last ran at 0f9c656, after #252, and the scripted run matched expected.go with 12 PASS. PR #253 merged since then, off-mission. The next ready issue is #195 (P1), then #32 and #239.
- Mission #172 (jaala's translator) closed on 2026-11-03. One check is still unlogged: a live run against a single-slot (`-np 1`) llama-server, from jaala's side.
- Mission #142 (`mission_api_v1`) is paused, not closed. Its exercise is `make api-report` (#143), and that target doesn't exist yet.
- main is next on #195 (see the main thread).
- This run: dropped tty-control (PR #252 merged, exercises logged on #162). #223 merged as PR #253, and v0.2.7 carries it.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`. Every go.mod replaces mcpkit with `../../mcpkit/main`, with one more `../` per module depth. Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- `make host-exercise` needs no model. `make host-exercise-live` and `make dogfood-web` need one. CI runs one client wire, so before trusting a change to server-sent data, run the host and root modules with `MCPKIT_CLIENT_MODE=stateless` too.
- Each of the batch's PRs filed one `waiting` follow-up: #234 (skill grants), #240 (rule folders) and #244 (`/code/`). They wait on the trigger named in each issue, so they don't belong in a queue.

## main

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/main)
- **Ticket**: mission #162. No branch is open from this clone.
- **Why**: build a Codex-equivalent from chakra primitives and measure it against Codex on Terminal-Bench 2 (#195, with the adapter and targets in `examples/terminal-bench`).
- **Where it stopped**: #247 put chakra on the Responses API for tbench. `escalation_asked` still fails live (log on #162 at b19787d). Off-mission, PR #253 let host extensions contribute turn-end hooks (#223).
- **Next action**: take #195 with /start-pr. Start from why `escalation_asked` fails live, and rerun `make tbench` on the hard task set from #247.
- **Open questions**: whether to report judge tokens on the eval report. PR 251 left them out, and nobody has asked for them yet. Whether the coding harness should use a turn-end hook for a "tests still red" check now that #253 lets it.
