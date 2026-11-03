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
| `TB_TASKS` | 10 tasks across categories | empty runs all 89 |
| `TB_PATH` | `~/newstack/chakra/benchmarks/terminal-bench-2` | the task checkout |
| `TB_JOBS` | `~/newstack/chakra/benchmarks/jobs` | where Harbor writes results |
| `TB_CONCURRENT` | 2 | tasks at once |

Needs Docker, `harbor` on PATH (`uv tool install --from <harbor checkout> harbor`), and
`OPENAI_API_KEY` (or the key for `TB_MODEL`).

The task images are amd64 only, so on Apple Silicon they run under emulation, and `tbench-build`
builds `agentchat` for amd64 to match.

Chakra's turn is capped at 400 model calls, so the task's time budget ends a run, as it does for
Codex. Chakra's default cap of 8 would end most tasks early. `agentchat exec --json` writes JSON
lines, and the adapter reads token usage, steps, tool calls and denied asks from them. So those
numbers show up in Harbor's results next to the other agents'.

Things that bit the first runs (2026-11):

- Docker Desktop at 8 GB fell over partway through a run with two tasks at once under emulation.
  16 GB with `TB_CONCURRENT=1` got through every task. Move a crashed job's folder aside before
  rerunning, or `tbench-report` picks up its partial results as the newest job.
- An exhausted OpenAI account answers `429` with `insufficient_quota` / `credit_balance_exhausted`.
  chakra's retry deliberately doesn't retry it, so every trial fails fast with a model error, and
  the report reads as 0/10 rather than "could not run". Check the error in a trial's log before
  reading anything into a score of zero.
