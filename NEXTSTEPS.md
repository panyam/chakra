# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #162 (`mission_coding_harness`) is active, 20/28 tickets closed. `make coding-harness-exercise` (scripted) last passed 2026-11-03 at 7fe109d (#216). Live, parity and Terminal-Bench have not run since #208. Next ready: #195 (P1).
- Mission #142 (`mission_api_v1`) is paused, not closed; its exercise is `make api-report` (#143).
- main — mission #162 — next: pick #214 (no credits needed) or #195's credit pre-flight; the live runs wait on credits.
- This run: dropped feat/step-routing-185 (PR #216 merged, mission log on #162); its routed live comparison moved into main's "Waiting on credits". Closed mission #172 (8/8; the rest is panyam/jaala#496). Synced go.mod files to mcpkit v0.8.16 (fb9b8c6).

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`; every go.mod replaces mcpkit with `../../mcpkit/main` (one more `../` per module depth). Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- `make host-exercise` needs no model; `make host-exercise-live` and `make dogfood-web` need one (`DOGFOOD_ACTIVE=deepseek` has been the working default). CI runs one client wire; run the host and root modules with `MCPKIT_CLIENT_MODE=stateless` too before trusting a change to server-sent data.
- Live coding-harness runs, as of 2026-11-03: OpenAI returns `429 credit_balance_exhausted`, and DeepSeek fails the harness's structured finalize because it refuses `json_schema` (#215 item 1). Nothing live runs on the harness until one of those clears.

## main

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/codex)
- **Ticket**: mission #162, worked through its queue with /start-pr. No branch open from this clone.
- **Why**: build a Codex-equivalent from chakra primitives and measure it against Codex on Terminal-Bench 2 (#195; adapter and targets in `examples/terminal-bench`).
- **Where it stopped**: #213 (#155) and #217 (#158) merged, mission logs for both are on #162, and no worktrees are left besides this checkout.
- **Next action**: ask the user to pick between #214 (stream a running process's output to the surfaces) and #195's next PR (a credit pre-flight in `make tbench`, then k runs), and start it with /start-pr.
- **Waiting on credits**: rerun `make tbench TB_AGENTS=chakra TB_TASKS="polyglot-c-py sanitize-git-repo nginx-request-logging"` to confirm #208's fixes (#203 was closed on them 2026-11-03 without a rerun), then the k=3 run for chakra and Codex, then live and parity, each with and without `CH_ROUTE_TO=openai CH_ROUTE_MODEL=gpt-5.4-mini` (`PARITY_ROUTE_*` for parity) so #185's per-model cost split gets its first real numbers. Last score was chakra 8/10, Codex 7/10 (k=1).
