# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #162 (`mission_coding_harness`) is active, 18/28 tickets closed. `make coding-harness-exercise` (scripted) last passed 2026-11-03 at d90faf3 (#213). Live, parity and Terminal-Bench have not run since #208 (see Across threads). Next ready: #195 (P1).
- Mission #172 (jaala's translator drafts) shows 8/8 closed in `queue.sh`; nobody has closed the mission issue yet.
- Mission #142 (`mission_api_v1`) is paused, not closed; its exercise is `make api-report` (#143).
- main — #158 in review as PR #217 (branch feat/egress-158) — next: post-merge routine for #217.
- This run: dropped the stale "#142 active" line and the #89 / #57 closure notes. feat/step-routing-185 (PR #216) keeps its thread on its branch.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`; every go.mod replaces mcpkit with `../../mcpkit/main` (one more `../` per module depth). Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- `make host-exercise` needs no model; `make host-exercise-live` and `make dogfood-web` need one (`DOGFOOD_ACTIVE=deepseek` has been the working default). CI runs one client wire; run the host and root modules with `MCPKIT_CLIENT_MODE=stateless` too before trusting a change to server-sent data.
- Live coding-harness runs, as of 2026-11-03: OpenAI returns `429 credit_balance_exhausted`, and DeepSeek fails the harness's structured finalize because it refuses `json_schema` (#215 item 1). Nothing live runs on the harness until one of those clears.

## main

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/codex)
- **Ticket**: mission #162, working through its queue with /start-pr; #158 is PR #217 (worktree `~/newstack/chakra/egress-158`)
- **Why**: build a Codex-equivalent from chakra primitives and measure it against Codex on Terminal-Bench 2 (#195; adapter and targets in `examples/terminal-bench`).
- **Where it stopped**: #213 (#155 process tools) merged, its mission log is on #162, and its worktree is removed. PR #217 is pushed, rebased on #213, and all suites pass.
- **Next action**: when #217 merges, pull, run `make coding-harness-exercise`, post the mission log on #162, and remove the `egress-158` worktree and `feat/egress-158` branch.
- **Waiting on credits**: rerun `make tbench TB_AGENTS=chakra TB_TASKS="polyglot-c-py sanitize-git-repo nginx-request-logging"` to close #203 (fixes in #208), then the k=3 run for chakra and Codex, then live and parity. Last score was chakra 8/10, Codex 7/10 (k=1).
- **Open questions**: none pending with the user. Next on the queue after #217 is #195's later PRs (k runs, a credit pre-flight in `make tbench`) or #214.
