# coding-harness

Mission #162's coding agent and its exercise. The agent is agentweb's server and browser UI over a
`host.App` wired for coding in `profile.go`. Every way it differs from `agentchat --workspace` is
a choice made there, of config, extension, tool or provider. If a mission ticket would have to
change how a turn runs, that's a gap in a hook point, and it gets its own ticket.

## Running it

| Target | What it does | Needs |
|---|---|---|
| `make coding-harness-exercise` | Scripted model, PASS/FAIL per behaviour, compared against `expected.go`. CI runs this one. | nothing |
| `make coding-harness-exercise-live` | Same flow with a real model, plus `steps tool_calls in_tok out_tok wall tests` | `CH_ACTIVE` (default `openai`), its key |
| `make coding-harness-web` | The agent in the browser at http://localhost:8091 on a fresh copy of the fixture | same as live; `CH_WORKSPACE=dir` for another repo |
| `make coding-harness-parity` | `codex exec` and the live harness on the same fixture, prompt and model, side by side | `codex` on PATH, `OPENAI_API_KEY`; `PARITY_MODEL` (default `gpt-5.5`) |

Each run copies `testdata/fixture` to a temp directory. The fixture is a Go module whose `Sum` skips
the first element, plus an `AGENTS.md` with three rules. The exercise starts an in-process
agentweb server, creates a session, opens `Watch` and `Submit`s the task. After the first tool
result it submits a steer. When the turn ends it runs `go test ./...` in the copy itself.

`-keep`, `-jsonl file` and `-metrics file` keep the workspace, the Watch frames and the metrics.

## The checks

`checks.go` has one check per behaviour the mission asks for, with the wiring that serves it.
`expected.go` pins each check that fails today to the ticket that fixes it. A ticket that makes a
check pass removes its line, and the mission is done when the map is empty. When a ticket lands,
its tool also replaces today's equivalent in `scripted.go` (for example `apply_patch` for
`edit_file`), so the scripted run keeps exercising that wiring.

Live runs print the same table but aren't held to `expected.go`, because a model's choices vary
from run to run.

## Parity with Codex

`scripts/coding-harness-parity.sh` gives `codex exec` the API key as `CODEX_API_KEY`, which
applies to exec only and leaves a ChatGPT login alone. It passes `--ignore-user-config` so
`~/.codex/config.toml` stays out of the comparison. Both sides bill the same key on the same model.
Token counts from a ChatGPT-login run aren't comparable, because that goes through a different
backend.

Until #157 lands, the two sides use different wires. Codex speaks the Responses API and chakra
speaks chat completions, so the codex-tuned models (`gpt-5.x-codex`) are out of reach for chakra.
Compare on a general model both serve.
