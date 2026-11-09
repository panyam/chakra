# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
/checkpoint maintains it, with one thread per branch, and prunes a thread when its branch merges.

## At a glance

- Mission #299 (`mission_graph_query_preset`) is active, with 0 of 3 chakra tickets closed. It serves panyam/jaala#322, and it's also blocked by panyam/jaala#514 and #517. The next ready tickets are #279 (state slots) and #296 (windowed stuck detector), both P2. #300 (the preset, P1) waits on both. Its exercise is `make exercise-translator ARMS="14b:loop 14b"` in Declaire, and it hasn't run on this mission yet.
- Mission #162 (`mission_coding_harness`) closed on 2026-11-08 with all 42 tickets done. The last log is on #162: the live exercise had 12 of 13, with `escalation_asked` accepted as a known limit, and `make mission-tbench` showed chakra at 54/66 and Codex at 51/67 on gpt-5.5.
- Mission #142 (`mission_api_v1`) is paused, not closed. Its exercise `make api-report` runs green in CI, and #40 is its next ticket.
- Mission #172 (jaala's translator) closed on 2026-11-03. One check is still unlogged: a live run against a single-slot (`-np 1`) llama-server, from jaala's side.
- This run: PRs 283, 284, 288, 290, 291, 292, 293 and 295 merged (#276, #278, #285, #274, #280, #289, #281, #287), and v0.3.0 was tagged. #195 and #26 closed, and #32 moved off the mission.

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
