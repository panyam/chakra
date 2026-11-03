# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #162 (`mission_coding_harness`) is active, 17/28 tickets closed. Its exercise, `make coding-harness-exercise` (scripted), last passed 2026-11-03 on feat/step-routing-185. Live and parity runs are blocked (see Across threads). Next ready after #185: #195 (P1), then #203 (P1, which looks done by PR #208).
- Mission #142 (`mission_api_v1`) is paused, not closed; its exercise is `make api-report` (#143).
- feat/step-routing-185 — #185, PR #216 — next: merge once CI is green, then run live and parity with and without routing and log both on #162.
- This run: no other branch carries its own thread. Dropped the stale "#142 active" line and the #89 / #57 closure notes (both closed 2026-10-26, recorded on their issues).

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`; every go.mod replaces mcpkit with `../../mcpkit/main` (one more `../` per module depth). Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- `make host-exercise` needs no model; `make host-exercise-live` and `make dogfood-web` need one (`DOGFOOD_ACTIVE=deepseek` has been the working default). CI runs one client wire; run the host and root modules with `MCPKIT_CLIENT_MODE=stateless` too before trusting a change to server-sent data.
- Live coding-harness runs, as of 2026-11-03: OpenAI returns `429 credit_balance_exhausted`, and DeepSeek fails the harness's structured finalize because it refuses `json_schema` (#215 item 1). Nothing live runs on the harness until one of those clears.
- #203 is still open although PR #208 fixed all three of its misses; it probably wants closing.

## feat/step-routing-185

- **Last touched**: 2026-11-03 (local:~/newstack/chakra/feat-185)
- **Ticket**: #185 (PR #216, which also closes #4); follow-ups in #215
- **Why**: route steps inside a turn to another connection, so #162's parity runs can compare cost per model.
- **Where it stopped**: done and pushed, with main merged in (the CHANGELOG conflict resolved into a new `[Unreleased]`). On CI, gates and test-web are green; test and test-examples were still running.
- **Next action**: after merge, run `make coding-harness-exercise-live` with and without `CH_ROUTE_TO=openai CH_ROUTE_MODEL=gpt-5.4-mini`, then `make coding-harness-parity` with `PARITY_ROUTE_*`, and post the mission log on #162.
- **Open questions**: none on the design. The live comparison has never run, so the per-model metrics line is covered only by `TestMetricsLineSplitsTokensByModel`.
