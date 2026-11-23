# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
/checkpoint maintains it, with one thread per branch, and prunes a thread when its branch merges.

## At a glance

- main — mission #299 — next: rerun the Declaire exercise once jaala requires chakra v0.3.5 and sets `FirstStuck`.
- Mission #299 has 12 of 12 linked tickets closed and is still open. Its exercise last ran on 2026-11-09, after PR 328: `14b:loop` passed 0 of 15 and `14b` passed 2 of 15. PR 331 (#330) merged since then, and it can't show up until jaala picks it up.
- Mission #142 (`mission_api_v1`) is paused. #315 is its next ready ticket.
- This run: rewrote the main thread (the #162 thread was stale, and #162 is closed). Threads on feat/step-routing-185, tty-control and host-turnend-hooks belong to merged PRs 216, 252 and 253, so they stay out of this file.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`. Every go.mod replaces mcpkit with a relative path, so worktrees go at `~/newstack/chakra/<name>` (CLAUDE.md Traps).
- #234, #258, #298 and #329 are `waiting` on the trigger each issue names.

## main

- **Last touched**: 2026-11-23 (local:~/newstack/chakra/main)
- **Ticket**: mission #299. PRs 328 (#327) and 331 (#330) merged, and v0.3.5 is tagged and released.
- **Why**: jaala's 14B translator loop failed 13 of 14 questions by repeating the same draft. `CollapseStuckSteps` drops those repeats from the model's view on the first stuck finding.
- **Where it stopped**: the user owns the jaala side. That means bumping to chakra v0.3.5, adding `FirstStuck: agent.CollapseStuckSteps(stuckKey)` in `frontends/nlmodels/translator/loop.go`, tagging jaala, and bumping Declaire. panyam/jaala#544 (the budget-exceeded message behind x03 and x04) is open.
- **Next action**: after the user confirms jaala is in, run `make exercise-translator ARMS="14b:loop 14b"` in `~/newstack/declaire/main` and post the mission log on #299. Compare against the 2026-11-09 numbers above.
- **Open questions**: if `14b:loop` still passes nothing, is #299 done as a build-from-preset mission, with the pass rate as a new mission? Or does it stay open on pass rate?
