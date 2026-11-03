# coding-harness

Mission #162's coding agent and its exercise. Here the agent is agentweb over `config.json` and
`workspace.json`. `profile.go` builds the same `host.App` for the exercise. The exercise also needs
a scripted model and a recorder, and agentweb can't take either from outside. The agent differs
from `agentchat --workspace` only by choice of config, extension, tool or provider, and these files
make each choice. If a mission ticket would have to change how a turn runs, that's a gap in a hook
point, and it gets its own ticket.

## Running it

| Target | What it does | Needs |
|---|---|---|
| `make coding-harness-exercise` | Scripted model, PASS/FAIL per behaviour, compared against `expected.go`. CI runs this one. | nothing |
| `make coding-harness-exercise-live` | Same flow with a real model, plus `steps tool_calls in_tok out_tok wall tests` | `CH_ACTIVE` (default `openai`), its key |
| `make coding-harness-web` | Plain agentweb at http://localhost:8091 over this directory's config and workspace files, on a fresh copy of the fixture | same as live; `CH_WORKSPACE=dir` for another repo, `CH_ADDR` |
| `make coding-harness-parity` | `codex exec` and the live harness on the same fixture, prompt and model, side by side | `codex` on PATH, `OPENAI_API_KEY`; `PARITY_MODEL` (default `gpt-5.5`), `PARITY_ACTIVE` (default `openai-responses`), `PARITY_INSTRUCTIONS` |

`CH_INSTRUCTIONS` (`--instructions` on both binaries) picks how `AGENTS.md` and the like load. The
choices are `chakra` (the default), `codex` or `claude`, and they are the presets in
`ext/agentsmd`. `PARITY_INSTRUCTIONS=codex` makes the chakra side load them the way Codex does.

Codex talks to the Responses API, so parity runs chakra over the `openai-responses` connection.
Both sides are pinned to medium reasoning effort, and both keep their reasoning between tool
steps. `PARITY_ACTIVE=openai` puts chakra back on chat completions, which shows what that
continuity is worth. `CH_ACTIVE=openai-responses` does the same for the live exercise.

Each run copies `testdata/fixture` to a temp directory. The fixture is a Go module. Its `stats.Sum`
skips the first element. An `AGENTS.md` at the root has three rules, and `stats/AGENTS.md` has one
more. The root file loads at startup. The agent reads the `stats/` file once it works in `stats/`,
and Codex never does that. The `stats/` rule says to end the answer with `stats: checked`, so the
parity table shows whether each side saw it. The exercise starts an in-process agentweb server,
creates a session, opens `Watch` and `Submit`s the task. After the first tool result it submits a
steer. When the turn ends it runs `go test ./...` in the copy itself.

`-keep`, `-jsonl file` and `-metrics file` keep the workspace, the Watch frames and the metrics.

Routing sends later steps to a cheaper model. Set
`CH_ROUTE_TO=openai CH_ROUTE_MODEL=gpt-5.4-mini` (or `--route-to`, `--route-model`). Every step
from `CH_ROUTE_FROM` (default 2) on then goes to that connection and model, so step 1 plans on the
active model and the rest run cheaper. The metrics line then adds one `model=in/cached/out`
entry per model. `PARITY_ROUTE_TO`, `PARITY_ROUTE_FROM` and `PARITY_ROUTE_MODEL` do the same for
the parity table.

## The checks

`checks.go` has one check per behaviour the mission asks for, with the wiring that serves it.
`expected.go` pins each check that fails today to the ticket that fixes it. When a ticket makes a
check pass, the ticket removes that check's line, and the mission is done when the map is empty.
When a ticket merges, its tool also replaces today's equivalent in `scripted.go` (for example
`apply_patch` for `edit_file`), so the scripted run keeps exercising that wiring.

Live runs print the same table but aren't held to `expected.go`, because a model's choices vary
from run to run.

## Parity with Codex

`scripts/coding-harness-parity.sh` gives `codex exec` the API key as `CODEX_API_KEY`. That variable
applies to exec only and leaves a ChatGPT login alone. The script passes `--ignore-user-config`, so
`~/.codex/config.toml` is left out of the comparison. Both sides bill the same key on the same
model. Token counts from a ChatGPT-login run aren't comparable, because that run goes through a
different backend.

Both sides speak the Responses API. #157 (PR 198) added chakra's Responses provider, and the
parity script runs chakra on the `openai-responses` connection by default (`PARITY_ACTIVE`). So the
codex-tuned models (`gpt-5.x-codex`) are in reach for both sides.
