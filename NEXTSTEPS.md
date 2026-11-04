# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
/checkpoint maintains it, with one thread per branch, and prunes a thread when its branch merges.

## At a glance

- Mission #162 (`mission_coding_harness`) is active, with 30 of 32 tickets closed. The scripted, live and parity exercises last ran on 2026-11-03 at b19787d, after #250. Scripted is 12 PASS and live is 11/12, with `escalation_asked` still failing. The next ready issue is #195 (P1), then #239.
- Mission #172 (jaala's translator) has 9/9 tickets closed. Its exercise lives in jaala.
- Mission #142 (`mission_api_v1`) is paused, not closed. Its exercise is `make api-report` (#143), and that target doesn't exist yet.
- main is next on #195 (see the main thread).
- tty-control is on #249 (PR #252). After it merges, run the exercises and log them on #162.
- This run merged in another clone's checkpoint from main: #243 merged as PR #248, and #246 as PR #251. Before that, a six-issue batch merged as #229, #232, #235, #241, #245 and #250, closing #225, #187, #184, #178 and #218. #32 was closed on 2026-11-03 with no linked PR.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`. Every go.mod replaces mcpkit with `../../mcpkit/main`, with one more `../` per module depth. Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- `make host-exercise` needs no model. `make host-exercise-live` and `make dogfood-web` need one. CI runs one client wire, so before trusting a change to server-sent data, run the host and root modules with `MCPKIT_CLIENT_MODE=stateless` too.
- Each of the batch's PRs filed one `waiting` follow-up: #234 (skill grants), #240 (rule folders) and #244 (`/code/`). They wait on the trigger named in each issue, so they don't belong in a queue.

## main

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/second)
- **Ticket**: mission #162. No branch is open from this clone.
- **Why**: build a Codex-equivalent from chakra primitives and measure it against Codex on Terminal-Bench 2 (#195, with the adapter and targets in `examples/terminal-bench`).
- **Where it stopped**: #247 put chakra on the Responses API for tbench. `escalation_asked` still fails live (log on #162 at b19787d). Off-mission, #248 and #251 merged.
- **Next action**: take #195 with /start-pr. Start from why `escalation_asked` fails live, and rerun `make tbench` on the hard task set from #247.
- **Open questions**: whether to report judge tokens on the eval report. PR 251 left them out, and nobody has asked for them yet.

## tty-control

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/tty-view)
- **Ticket**: #249 (PR #252).
- **Why**: #218's terminal view drew at a fixed 120x30 and was read-only. This lets a surface resize a tty and type into it.
- **Where it stopped**: Everything is committed and pushed: `host.ProcessController`, the `ResizeProcess` and `WriteProcess` RPCs, the view's fit and input, agentchat's sizer, and the `<Index>` fix for the remount loop. `make test`, `make test-examples`, vitest, Playwright, and `ext/exec` on Linux in Docker all pass.
- **Next action**: after PR #252 merges, run `make coding-harness-exercise`, `-live` and `coding-harness-parity`, and log them on #162. Then remove this thread.
