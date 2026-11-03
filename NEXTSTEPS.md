# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #162 (`mission_coding_harness`) is active, 19/28 tickets closed. `make coding-harness-exercise` (scripted) last passed 2026-11-03 at adde7de (#217) and on feat/step-routing-185. Live, parity and Terminal-Bench have not run since #208 (see Across threads). Next ready: #195 (P1).
- Mission #172 (jaala's translator drafts) shows 8/8 closed in `queue.sh`; nobody has closed the mission issue yet.
- Mission #142 (`mission_api_v1`) is paused, not closed; its exercise is `make api-report` (#143).
- feat/step-routing-185 — #185, PR #216 — next: merge (CI green), then run live and parity with and without routing and log both on #162.
- main — mission #162 — next: pick #214 (no credits needed) or #195's credit pre-flight; rerun the #203 tasks once credits return.
- This run: merged main's checkpoint into feat/step-routing-185 and folded the duplicate glance lines from both clones into one set. #203 closed 2026-11-03 on #208's fixes.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`; every go.mod replaces mcpkit with `../../mcpkit/main` (one more `../` per module depth). Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- `make host-exercise` needs no model; `make host-exercise-live` and `make dogfood-web` need one (`DOGFOOD_ACTIVE=deepseek` has been the working default). CI runs one client wire; run the host and root modules with `MCPKIT_CLIENT_MODE=stateless` too before trusting a change to server-sent data.
- Live coding-harness runs, as of 2026-11-03: OpenAI returns `429 credit_balance_exhausted`, and DeepSeek fails the harness's structured finalize because it refuses `json_schema` (#215 item 1). Nothing live runs on the harness until one of those clears.

## feat/step-routing-185

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/feat-185)
- **Ticket**: #185 (PR #216, which also closes #4); follow-ups in #215
- **Why**: route steps inside a turn to another connection, so #162's parity runs can compare cost per model.
- **Where it stopped**: done and pushed, with main merged in twice (the first time the CHANGELOG conflict went into a new `[Unreleased]`). All four CI checks passed on 11bf063.
- **Next action**: after merge, run `make coding-harness-exercise-live` with and without `CH_ROUTE_TO=openai CH_ROUTE_MODEL=gpt-5.4-mini`, then `make coding-harness-parity` with `PARITY_ROUTE_*`, and post the mission log on #162.
- **Open questions**: none on the design. The live comparison has never run, so the per-model metrics line is covered only by `TestMetricsLineSplitsTokensByModel`.

## main

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/codex)
- **Ticket**: mission #162, worked through its queue with /start-pr. No branch open from this clone.
- **Why**: build a Codex-equivalent from chakra primitives and measure it against Codex on Terminal-Bench 2 (#195; adapter and targets in `examples/terminal-bench`).
- **Where it stopped**: #213 (#155) and #217 (#158) merged, mission logs for both are on #162, and no worktrees are left besides this checkout.
- **Next action**: ask the user to pick between #214 (stream a running process's output to the surfaces) and #195's next PR (a credit pre-flight in `make tbench`, then k runs), and start it with /start-pr.
- **Waiting on credits**: rerun `make tbench TB_AGENTS=chakra TB_TASKS="polyglot-c-py sanitize-git-repo nginx-request-logging"` to confirm #208's fixes (#203 was closed on them 2026-11-03 without a rerun), then the k=3 run for chakra and Codex, then live and parity. Last score was chakra 8/10, Codex 7/10 (k=1).
- **Open questions**: whether to close mission #172 (queue.sh shows 8/8 closed); that's the user's call.
