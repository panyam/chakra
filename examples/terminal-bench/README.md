# Terminal-Bench

Mission #162's comparison against Codex (and later Claude Code) on a public benchmark (#195). The
benchmark is [Terminal-Bench 2](https://github.com/harbor-framework/terminal-bench-2), and its
official harness [Harbor](https://github.com/harbor-framework/harbor) runs it. Every agent runs
under the same harness on the same tasks. Tests grade each run, and the agent never sees them.

`chakra_agent.py` adds chakra as a Harbor installed agent. Harbor copies a static Linux build of
`agentchat` into the task container and runs `agentchat exec` there. The container is the sandbox,
so chakra runs with approvals off and its exec sandbox unconfined. Harbor runs Codex the same way
(`--dangerously-bypass-approvals-and-sandbox`). Codex and Claude Code are Harbor's built-in
agents.

```
make tbench                       # chakra then codex, the 10-task starter set, one attempt each
make tbench TB_AGENTS=chakra TB_K=3
make tbench TB_SET=hard           # eight harder tasks
make tbench TB_TASKS=             # all 89 tasks
make tbench-report                # markdown side by side: the newest job of each agent
```

`harbor view ~/newstack/chakra/benchmarks/jobs` opens Harbor's own browser view of every job,
with each trial's logs and trajectory.

| Variable | Default | |
|---|---|---|
| `TB_AGENTS` | `chakra codex` | `claude-code` works too, with `TB_MODEL` set to a Claude model |
| `TB_MODEL` | `openai/gpt-5.5` | Harbor's provider/model form |
| `TB_K` | 1 | attempts per task |
| `TB_SET` | `starter` | `hard` picks eight tasks the dataset marks hard that need no GPU and fit a 30 minute budget |
| `TB_TASKS` | the `TB_SET` tasks | empty runs all 89 |
| `TB_EFFORT` | `medium` | reasoning effort for every agent, through Harbor's `reasoning_effort` option; empty leaves each agent at its own default |
| `TB_PATH` | `~/newstack/chakra/benchmarks/terminal-bench-2` | the task checkout |
| `TB_JOBS` | `~/newstack/chakra/benchmarks/jobs` | where Harbor writes results |
| `TB_REFS` | `~/newstack/chakra/benchmarks/refs` | where `tbench-ref` downloads leaderboard submissions |
| `TB_PREFLIGHT` | on | `0` skips the quota check before a run |
| `TB_CONCURRENT` | 2 | tasks at once |

`make tbench` first sends one tiny request for `TB_MODEL` (`scripts/tbench_preflight.py`, #266). An
exhausted account, a refused key or an unknown model stops it there with one line, and a rate limit
only warns. `TB_PREFLIGHT=0` skips the check.

Needs Docker, `harbor` on PATH (`uv tool install --from <harbor checkout> harbor`), and
`OPENAI_API_KEY` (or the key for `TB_MODEL`).

The task images are amd64 only, so on Apple Silicon they run under emulation, and `tbench-build`
builds `agentchat` for amd64 to match.

Both agents talk to the Responses API, and `TB_EFFORT` pins both to the same effort, so the cost
and time columns compare like with like. Jobs before #195's fairness fixes ran chakra over Chat
Completions with neither effort pinned, and `make tbench-report` prints each job's effort so a
table says how it was made.

### Chakra next to Claude Code on a Claude model (#264)

```
make tbench TB_AGENTS="chakra claude-code" TB_MODEL=anthropic/claude-sonnet-5-5 TB_EFFORT=high
```

With an `anthropic/` model, the adapter gives chakra an `anthropic` connection with `maxTokens`
at 64000. The provider's default of 4096 would cut a long file write short. Both agents think
adaptively at effort `high`. Chakra's Anthropic provider sends no `thinking` or effort field, so
Sonnet 5.5 runs at its default, which is adaptive at `high`. `TB_EFFORT=high` pins Claude Code to
the same level. Sonnet 5.5 has no plain "thinking off". Its only off mode is `between_tools`, which
chakra can't send, so matching at the default is the closest pair.

Two differences remain, and both come from A9 in `CONSTRAINTS.md`. Chakra sends no prompt caching,
so its cost column pays full price for every resent token, and Claude Code's doesn't. That makes the
cost columns not comparable. Chakra also drops thinking blocks from the history it resends. So the
model doesn't see its earlier reasoning between tool calls, while Claude Code passes it back. The
solved columns compare the harnesses with that gap included.

Chakra's turn is capped at 400 model calls, so the task's time budget ends a run, as it does for
Codex. Chakra's default cap of 8 would end most tasks early. `agentchat exec --json` writes JSON
lines, and the adapter reads token usage, steps, tool calls and denied asks from them. So those
numbers show up in Harbor's results next to the other agents'. A trial that hits its time limit
never finishes the turn. Its usage is then the sum of the model calls that finished, and the
trial's metadata says `usage_partial` (#262). The call in flight at the kill isn't counted, so a
partial figure is a lower bound.

## Run history and stale cells

Each check used to mean another sweep, because nothing remembered which cells had run (#257).
Three targets keep that record now. A cell is one task on one setup. A setup is agent + model +
effort, written `chakra/openai-gpt-5.5/medium`.

```
make tbench-ingest                # new jobs in TB_JOBS -> benchmarks/results/<job>.json; safe to rerun
make tbench-matrix                # every task x every setup, as markdown and benchmarks/matrix/index.html
make tbench-stale N=10 BUDGET=5   # print the oldest or never-run cells of the current setups
make tbench-stale RUN=1           # run them, one `make tbench` per setup, then ingest
```

Two sessions often merge on the mission a few minutes apart, and each runs `make mission-tbench`
from its own checkout. On 2026-11-06 two batches started 30 seconds apart, picked the same oldest
cells, and ran four Codex tasks twice for about $3 (#289). So `tbench-stale RUN=1` now runs its
pick, trials and ingest under a lock in `TB_JOBS`, which every checkout shares
(`scripts/tbench-lock.sh`). A second batch prints the holder's pid and waits, then picks after the
first batch's results are in. A lock left by a process that died is taken over. Each `make tbench`
the batch starts writes its job name to `benchmarks/matrix/stale-jobs`, and `mission-tbench`
reports only those jobs, so another batch's trials stay out of its table.

`benchmarks/results/` is checked in, so `git log benchmarks/results/` gives the run history. Once a
job is ingested, its folder under `TB_JOBS` can go. The matrix reads only the results folder.

A cell reads `2/3 · Nov 3` for two solved out of three trials, last run on Nov 3. Chakra cells add
`+4c` for four commits touching Go code or this folder since the build. `tbench-build` stamps the
build's sha in `bin/VERSION`, and the adapter reports it as the agent version. Jobs from before the
stamp get the last commit on main before the run, marked `?`. Codex cells show only the date, since
we don't control when Codex changes.

`tbench-stale` picks from the setups `make tbench` would run with the current `TB_AGENTS`,
`TB_MODEL` and `TB_EFFORT`. Cells that never ran come first, then the oldest. Each cell's cost is
that task's average past tokens at list price (`PRICES` in `scripts/tbench_matrix.py`). If the task
never ran on this model, the estimate scales the reference columns' cost on that task by how our
cost compares with theirs on tasks both have run (#267). Only a task no reference covers falls back
to the median trial. On #263's ten-task batch, the median alone predicted $2.08 and the batch cost
over $5. The scaled estimate predicted $5.69.

Published runs come in as reference columns. The
[leaderboard](https://huggingface.co/datasets/harborframework/terminal-bench-2-leaderboard)
publishes each submission as plain Harbor job folders, so ingest reads them directly:

```
make tbench-ref SUB=Terminus2__GPT-5.3-Codex
```

`SUB` is the folder name under `submissions/terminal-bench/2.0/`. The target downloads the
submission's `metadata.yaml`, job configs and trial results into `TB_REFS`, and skips files it already
has. It writes `ref-<submission>-<yyyy-mm>.json` with notes on the harness, model, effort, sandbox
and attempts. Terminus 2 on Claude Opus 4.6 and on GPT-5.3-Codex are in so far. A reference column is never picked
as stale. Its model, effort and hardware are the submitter's, so comparing it with our columns
mixes harness with model. Codex stays a column we run, on chakra's model and provider.

## Things that bit the first runs (2026-11)

- Docker Desktop at 8 GB fell over partway through a run with two tasks at once under emulation.
  16 GB with `TB_CONCURRENT=1` got through every task. Move a crashed job's folder aside before
  rerunning, or `tbench-report` picks up its partial results as the newest job.
- An exhausted OpenAI account answers `429` with `insufficient_quota` / `credit_balance_exhausted`.
  chakra's retry deliberately doesn't retry it, so every trial used to fail fast and the report
  read 0/10. The preflight now stops the run before it starts.
- Docker Desktop sometimes can't reach Docker Hub partway through a run. Harbor then fails the trial
  with a `RuntimeError` from `docker compose` before the agent starts. The matrix doesn't count a
  trial that never ran, and `tbench-stale` picks the cell again, so rerun later.
- Installing Codex (nvm and npm, under emulation) on `constraints-scheduling` takes longer than
  Harbor's 360-second agent setup limit. The trial ended in `AgentSetupTimeoutError` twice, and an
  uncounted trial gets picked again. So `make tbench` now passes `TB_SETUP_TIMEOUT=3` as
  `--agent-setup-timeout-multiplier`, for 18 minutes (#277). Setup runs before the agent starts, so
  the agent's budget and the score don't change. A failed install still exits at once, because the
  install script runs under `set -euo pipefail`. `TB_SETUP_TIMEOUT=` keeps Harbor's default.
- Harbor imports `chakra_agent.py` and the scripts from the checkout at the start of each job. So
  don't edit a worktree while a batch runs from it. Make a second worktree for the edits.
