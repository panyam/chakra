# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
/checkpoint maintains it, with one thread per branch, and prunes a thread when its branch merges.

## At a glance

- Mission #162 (`mission_coding_harness`) is active, 30/32 tickets closed. The last exercise ran at b19787d after #250: scripted 12 PASS, live 11/12, and parity posted on #162. The next ready ticket is #195 (P1). Live runs still fail `escalation_asked`.
- Mission #172 (jaala's translator) has 9/9 tickets closed. Its exercise lives in jaala.
- Mission #142 (`mission_api_v1`) is paused, not closed. Its exercise is `make api-report` (#143), and that target doesn't exist yet.
- tty-control is another session's branch. It has the #249 fold (terminal resizing and typing), with a PR to follow.
- main: next is #195.
- This run: #243 merged as PR #248, and #246 merged as PR #251 (off-mission, for panyam/jaala#437). The #162 thread lines about credits and #215 were dropped, because #215 closed and the live runs now go through.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`. Every go.mod replaces mcpkit with `../../mcpkit/main`, with one more `../` per module depth. Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- `make host-exercise` needs no model. `make host-exercise-live` and `make dogfood-web` need one. CI runs one client wire, so run the host and root modules with `MCPKIT_CLIENT_MODE=stateless` too before trusting a change to server-sent data.

## main

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/second)
- **Ticket**: mission #162. No branch is open from this clone.
- **Why**: build a Codex-equivalent from chakra primitives and measure it against Codex on Terminal-Bench 2 (#195, with the adapter and targets in `examples/terminal-bench`).
- **Where it stopped**: #247 put chakra on the Responses API for tbench. `escalation_asked` still fails live (log on #162 at b19787d). Off-mission, #248 and #251 merged.
- **Next action**: take #195 with /start-pr. Start from why `escalation_asked` fails live, and rerun `make tbench` on the hard task set from #247.
- **Open questions**: whether to report judge tokens on the eval report. PR 251 left them out, and nobody has asked for them yet.
