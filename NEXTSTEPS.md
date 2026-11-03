# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
/checkpoint maintains it, with one thread per branch, and prunes a thread when its branch merges.

## At a glance

- Mission #162 (`mission_coding_harness`) is active, 22/29 tickets closed. `make coding-harness-exercise` (scripted) last passed 2026-11-03 at 38c9ee1, after #219 and #220. Live, parity and Terminal-Bench have not run since #208. The next ready issue is #195 (P1), and it waits on credits.
- Mission #142 (`mission_api_v1`) is paused, not closed; its exercise is `make api-report` (#143).
- main is on mission #162. Next is #215 item 1, running DeepSeek through the structured finalize, so live runs can go without OpenAI credits.
- fix/responses-error-event has PR #221 open from another session, and no thread is recorded here.
- In this run, #214 merged as PR #219 (the mission log on #162), and #218 was filed for the xterm.js tty view. The process-output lessons went into NOTES.md.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`; every go.mod replaces mcpkit with `../../mcpkit/main` (one more `../` per module depth). Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- `make host-exercise` needs no model; `make host-exercise-live` and `make dogfood-web` need one (`DOGFOOD_ACTIVE=deepseek` has been the working default). CI runs one client wire; run the host and root modules with `MCPKIT_CLIENT_MODE=stateless` too before trusting a change to server-sent data.
- As of 2026-11-03, live coding-harness runs fail two ways. OpenAI returns `429 credit_balance_exhausted`, and DeepSeek fails the harness's structured finalize because it refuses `json_schema` (#215 item 1). Nothing live runs on the harness until one of those clears.

## main

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/codex)
- **Ticket**: mission #162, worked through its queue with /start-pr. No branch open from this clone.
- **Why**: build a Codex-equivalent from chakra primitives and measure it against Codex on Terminal-Bench 2 (#195; adapter and targets in `examples/terminal-bench`).
- **Where it stopped**: #214 merged (PR #219); live and parity could not run, and the log on #162 says why. The orphan `pr-assets` branch now holds PR screenshots (see CLAUDE.md).
- **Next action**: take #215 item 1 with /start-pr, so `CH_ACTIVE=deepseek make coding-harness-exercise-live` gets past the structured finalize (it fails with `response_format type is unavailable`). That gives the mission a live table while OpenAI has no credits.
- **Waiting on credits**: first rerun `make tbench TB_AGENTS=chakra TB_TASKS="polyglot-c-py sanitize-git-repo nginx-request-logging"` to confirm #208's fixes, since #203 was closed on them without a rerun. Then do the k=3 run for chakra and Codex. Then run live and parity, each with and without `CH_ROUTE_TO=openai CH_ROUTE_MODEL=gpt-5.4-mini` (`PARITY_ROUTE_*` for parity), to get #185's per-model cost split. The last score was chakra 8/10, Codex 7/10 (k=1).
