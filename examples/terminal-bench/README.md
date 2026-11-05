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
| `TB_CONCURRENT` | 2 | tasks at once |

Needs Docker, `harbor` on PATH (`uv tool install --from <harbor checkout> harbor`), and
`OPENAI_API_KEY` (or the key for `TB_MODEL`).

The task images are amd64 only, so on Apple Silicon they run under emulation, and `tbench-build`
builds `agentchat` for amd64 to match.

Both agents talk to the Responses API, and `TB_EFFORT` pins both to the same effort, so the cost
and time columns compare like with like. Jobs before #195's fairness fixes ran chakra over Chat
Completions with neither effort pinned, and `make tbench-report` prints each job's effort so a
table says how it was made.

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
never ran on this model, the estimate falls back to the median trial. Hard tasks cost more than the
median, so the budget can undercount a batch of never-run tasks.

Published runs come in as reference columns. The
[leaderboard](https://huggingface.co/datasets/harborframework/terminal-bench-2-leaderboard)
publishes each submission as plain Harbor job folders, so ingest reads them directly. Fetch a
submission's `config.json` and trial `result.json` files, then run:

```
python3 scripts/tbench_matrix.py ingest <folder> --name ref-<agent>-<model>-<yyyy-mm> \
    --source <submission url> --notes "<harness, model, effort, hardware, attempts>"
```

`ref-terminus2-claude-opus-4.6-2026-02.json` is the first one. A reference column is never picked
as stale. Its model, effort and hardware are the submitter's, so comparing it with our columns
mixes harness with model. Codex stays a column we run, on chakra's model and provider.

## Things that bit the first runs (2026-11)

- Docker Desktop at 8 GB fell over partway through a run with two tasks at once under emulation.
  16 GB with `TB_CONCURRENT=1` got through every task. Move a crashed job's folder aside before
  rerunning, or `tbench-report` picks up its partial results as the newest job.
- An exhausted OpenAI account answers `429` with `insufficient_quota` / `credit_balance_exhausted`.
  chakra's retry deliberately doesn't retry it, so every trial fails fast with a model error, and
  the report reads as 0/10 rather than "could not run". Check the error in a trial's log before
  reading anything into a score of zero.
