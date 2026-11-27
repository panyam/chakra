# Agents for a job

Each directory here is an agent for one job, built with chakra a step at a
time (mission 349). The first step is a model and its tools. Each step after
that adds one thing, such as a stuck policy, a budget, a verifier or memory.
After every step the agent runs the same eval, so the table shows what each
step changed.

| agent | job |
|---|---|
| [calc](calc/README.md) | Answer arithmetic, without and then with a calculator. It shows how to read the table. |
| [codeq](codeq/README.md) | Answer questions about Go code through a tool server over MCP. |
| [codefix](codefix/README.md) | Fix a bug in a Go module until its tests pass. |
| [support](support/README.md) | Answer a shop's customers from policy pages, and refund, readdress or hand off through tools. A model plays each customer. |
| [copilot](copilot/README.md) | Work beside a user on a kanban board inside a `host.App`: see their actions as events, react through a trigger, and don't overwrite what they change mid-turn. |

## Running an agent

Every agent has its own make targets. Run them from its directory:

```
cd codeq
make tui                 # walk the ladder in a styled terminal UI, on the scripted model
make note                # the same as notebook cells, so you can scroll back and compare steps
make note LIVE=haiku     # on a real model
make exercise            # run the whole ladder and print its table
make exercise CASES=1    # also print each failing case and its answer
make chat LIVE=sonnet    # chat with the last step in agentchat
make help                # everything else: walk, serve, record, walk-md, profiles, test
```

The targets come from `common/agent.mk`, and an agent's Makefile is two
lines: `AGENT := <name>` and `include ../common/agent.mk`. An agent adds its
own targets below those. codeq has `make server`, which runs its tool server.

From `examples/agents`, `make exercise` runs every agent's ladder and `make
profiles` lists the connections. From the repo root, `make exercise-agents`
is the mission's exercise, the same as `make exercise` here, and
`AGENT=codeq` runs one.

While a ladder runs, each step prints a header to stderr as it starts, and
each case prints a line as it finishes: PASS or FAIL, seconds, dollars when
the connection has a price, the question, and why a failed case failed. The
tables go to stdout.

```
codeq on haiku: 4 steps, 10 cases each
[1/4 tools only]
  PASS   1.7s  $0.00024  What is the qualified name of the function Format?
  PASS   1.3s  $0.00023  What is the qualified name of the type Amount?
  ...
```

`LIVE` names a connection in an agentchat config, and `PROFILE` is that
config's path. It defaults to `~/.agentchat/exercise-agents.json`. `make
profiles` (or `make exercise LIVE=?`) lists its connections with their type, model and
price, and a name it lacks is an error that lists the ones it has. A
connection's `price` prices the run. Cached input tokens bill at its
`cachedInput` rate, or at the input rate when it has none.

A live model gives different answers from run to run, so one run can move a
step by a case or two either way. `REPEATS=3` runs each case three times. The
pass column then shows the mean, out of the distinct cases, with the lowest
and highest run in brackets, such as `10.7/15 (9-12)`. Two steps whose
ranges overlap haven't been told apart.

A profile holds only a `connections` block. This one has Claude Haiku 5.5 and
Sonnet 5.5, with prices per million tokens:

```json
{
  "connections": {
    "active": "haiku",
    "connections": {
      "haiku":  { "type": "anthropic", "model": "claude-haiku-5-5",  "apiKeyEnv": "ANTHROPIC_API_KEY", "maxTokens": 16000,
                  "price": { "input": 0.10, "output": 0.50 } },
      "sonnet": { "type": "anthropic", "model": "claude-sonnet-5-5", "apiKeyEnv": "ANTHROPIC_API_KEY", "maxTokens": 16000,
                  "price": { "input": 2.00, "cachedInput": 0.10, "output": 10.00 } }
    }
  }
}
```

## Chatting with an agent

The ladders are batch evals. To talk to an agent yourself, `make chat` in its
directory runs its last step in agentchat. It writes an agentchat config to
`examples/agents/.chat-<agent>.json`, with the profile's connections, the last
step's instructions and the agent's MCP servers, then opens agentchat on it.
`LIVE` picks the connection, and leaves the profile's active one when it's
unset. A step written as code, such as a stuck detector or a `TurnEnd` hook,
has no agentchat config field, so the chat runs without it. Only codeq can run
in agentchat so far.

## Walking a ladder

`make exercise` runs a whole ladder. `make tui`, `make note`, `make walk` and
`make serve` step through it with [demokit](https://github.com/panyam/demokit).
The TUI and notebook modes are the ones to work in. The notebook keeps every
step's output as a cell, so you can scroll back and compare what two steps
did. `make serve` runs it in a browser on http://localhost:8765, to show
someone.

Each step shows what it adds to the agent: instructions, tools, step stages
and turn-end hooks. Then it asks one question. Press Enter to run every case,
with a line per case, ending on the step's row, what it cost, and what the
next step will likely cost. Or type a case number to watch that case: its
transcript, with each tool call and result, the answer, and its scores. A
watched step leaves no row in the table. The last step prints the table. The last step prints the
table.

`make record LIVE=haiku` records a full walk to `walk-haiku.json`, and `make
walk-md LIVE=haiku` renders it to `WALK.md` with no model calls. Render with
the same `LIVE` the walk ran on. `--replay`
reuses a recording's inputs but runs the steps again, so on a live
connection it calls the model again. [codeq/WALK.md](codeq/WALK.md) is a
recorded walk on Haiku 5.5.

## Adding an agent

Two checks come before any code, and the plan records both.

- **Calibrate the eval on a live model.** Draft the cases first. Run them on
  Haiku, and on a 14B too where one is set up, with the bare step and with
  the step you plan to finish on. Two numbers decide whether to build. The
  bare step should fail a good share of the cases, and the last step should
  plausibly pass them. If the bare step already passes nearly everything, the
  ladder can't show a step helping, so make the eval harder before building
  it. codeq skipped this check at first, and Haiku passed 10 of 10 on every
  step (#365). Run it with `REPEATS=3`, so a step's spread shows. The run
  costs a few cents.
- **Name each step's hook point.** List the steps, and give each one its
  point from [`docs/HOOKS.md`](../../docs/HOOKS.md): turn start, each model
  call, each tool call, turn end, or events. A step with no matching point
  needs a host or core change, and that change becomes part of the plan.
  copilot found three such gaps while it was being built (#383). The plan
  would have named them first.

The scripted model's gaps should copy failures the live run showed, so its
climb replays real mistakes.

1. A package under `examples/agents/<job>/` with its ladder, its
   `Instructions`, and `Scripted() agent.Provider`, a stand-in model for CI.
   The ladder is `func(ctx) (eval.Ladder, stop func(), error)`, so it can
   start what its steps share, as codeq starts its tool server. A ladder that
   starts nothing is `func() eval.Ladder`, wrapped with `common.Static`.
2. `Agent() common.Agent` in that package, and an entry in `All` in
   `agents.go`.
3. For the walk, `About` (the opening section) and `StepNotes` (why each step
   exists). `Walk` lays the walk out in code when the default isn't enough.
   It gets a `common.Walker`, whose `Intro`, `Steps`, `Step` and `Table` are
   the default pieces, and can add demokit sections and steps between them.
   codeq adds a section on its tool server after the intro.
4. A `README.md` that walks through the steps and shows the table.
   `TestEveryLadderClimbs` fails without one.
5. A `Makefile`: `AGENT := <name>` and `include ../common/agent.mk`.
6. Optionally, `Chat`, so `make chat` can run it.

`common` holds what every agent shares: `Agent`, `Climb`, the profile
helpers, `ChatConfig`, and `Walk`.

A step that needs something chakra lacks, or something awkward to use, becomes
a ticket on mission 349.
