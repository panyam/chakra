# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
/checkpoint maintains it, with one thread per branch, and prunes a thread when its branch merges.

## At a glance

- main — mission #337 — next: #338, diff what the loop arm and plain `14b` send on x01 and x05.
- Mission #299 closed on 2026-11-24. Its last run after PR 331: `14b:loop` 0 of 15 (recall 3/121), plain `14b` 2 of 15 (recall 52/121). The pass rate moved to #337.
- #334 is P0. It stops `make tbench-build` and vulncheck, and PR 336 fixes it. The tidy bot can't open its own PRs until Actions may create PRs in the repo settings.
- Mission #142 (`mission_api_v1`) is paused. #315 is its next ready ticket.
- This run: rewrote the main thread (the #162 thread was stale, and #162 is closed). Threads on feat/step-routing-185, tty-control and host-turnend-hooks belong to merged PRs 216, 252 and 253, so they stay out of this file.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`. Every go.mod replaces mcpkit with a relative path, so worktrees go at `~/newstack/chakra/<name>` (CLAUDE.md Traps).
- #234, #258, #298 and #329 are `waiting` on the trigger each issue names.

## main

- **Last touched**: 2026-11-24 (local:~/newstack/chakra/codex)
- **Ticket**: mission #337, next ready #338. Declaire friction tickets: panyam/declaire#381 (P1), #382 (P2). panyam/jaala#544 blocks the mission too.
- **Why**: on #299's last run the loop arm's recall was 3/121, against 52/121 for plain `14b`. The loop makes the 14B model's queries worse, beyond the repeats.
- **Where it stopped**: #299 closed and #337 opened. A Terminal-Bench stale batch (`make tbench-stale N=20 BUDGET=10 RUN=1`, 10 cells, ~$9.41) failed at `tbench-build` on #334 before spending anything.
- **Next action**: after PR 336 merges, rerun that tbench batch. Then start #338 from `~/newstack/declaire/main/translator-arms-20261123-1617/`.
