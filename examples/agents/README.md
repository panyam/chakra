# Agents for a job

Each directory here is an agent for one job, built with chakra a step at a
time (mission 349). The first step is a model and its tools. Each step after
that adds one thing, such as a stuck policy, a budget, a verifier or memory.
After every step the agent runs the same eval, so the table shows what each
step changed.

| agent | job |
|---|---|
| [calc](calc/README.md) | Answer arithmetic, with and then without a calculator. It shows how to read the table. |

## Running the ladders

```
make exercise-agents                                          # every agent, on scripted models, as CI runs it
make exercise-agents AGENT=calc                               # one agent
make exercise-agents AGENT=calc LIVE=mini PROFILE=agentchat.json   # on a real model
```

`LIVE` names a connection in the agentchat config at `PROFILE`. The
connection's `price` prices the run. `eval.Price` has no cached-input rate,
so cached tokens are priced at the full input rate.

## Adding an agent

1. A package under `examples/agents/<job>/` with `Ladder() eval.Ladder`, its
   `Instructions`, and `Scripted() agent.Provider`, a stand-in model for CI.
2. An entry in `All` in `agents.go`.
3. A `README.md` that walks through the steps and shows the table.
   `TestEveryLadderClimbs` fails without one.

A step that needs something chakra lacks, or something awkward to use, becomes
a ticket on mission 349.
