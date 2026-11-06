# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
/checkpoint maintains it, with one thread per branch, and prunes a thread when its branch merges.

## At a glance

- Mission #162 (`mission_coding_harness`) is active, with 36 of 40 tickets closed. The exercises last ran at a4dbde9, after PR 275. The live run had 11 of 12, with the pinned `escalation_asked` failing. `make mission-tbench` shows chakra at 47/55 on 34 tasks and Codex at 42/52 on 31. The log is on #162. The next ready issues are #274, #32 and #277, all P3.
- #195 (P1) has nothing left but a decision. It closes once the user says the original ablations are dropped.
- Mission #172 (jaala's translator) closed on 2026-11-03. One check is still unlogged: a live run against a single-slot (`-np 1`) llama-server, from jaala's side.
- Mission #142 (`mission_api_v1`) is paused, not closed. Its exercise `make api-report` exists now and runs in CI.
- main: see the thread. `postmerge-275` holds the last batch's results and goes in with #277.
- This run: PRs 261, 263, 269, 271, 273 and 275 merged (#257, #262, #265, #266, #267, #264). Filed #274 and #277.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`. Every go.mod replaces mcpkit with `../../mcpkit/main`, with one more `../` per module depth. Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- After each mission merge, run `make coding-harness-exercise-live` and `make mission-tbench` (CLAUDE.md, missions). The batch writes new `benchmarks/results/` files into the checkout it ran from, and the next mission PR carries them.
- #234 (skill grants) and #258 (retention sweep) are `waiting` on the trigger each issue names, so they don't belong in a queue.

## main

- **Last touched**: 2026-11-06 (local:~/newstack/chakra/codex)
- **Ticket**: mission #162. The open branch is `postmerge-275`, which holds batch results only.
- **Why**: build a Codex-equivalent from chakra primitives, and measure it against Codex (and Claude Code on Claude) on Terminal-Bench 2 with `make tbench-matrix` and `make mission-tbench`.
- **Where it stopped**: chakra matches Codex on gpt-5.5 at about half the tokens, and matches Claude Code on Sonnet 5.5 at 9/10 each. On Claude it costs more than twice as much, because it has no prompt caching (#274).
- **Next action**: get the user's answers to the three open questions below, then take #277 with the `postmerge-275` results.
- **Open questions**:
  - Should #274 move off the mission as `waiting`? Leaning yes, since solved rates already match on Claude.
  - Are #195's original ablations dropped (`editTools`, the agentsmd presets, steering on or off)? If so, #195 closes.
  - Should #162 close with `escalation_asked` accepted as a known limit, once #274 and #32 move off? Leaning yes.
  - Should `agentchat` get an `apiKeyCommand` key source, so keys don't have to sit in env vars? It was offered but not filed.
