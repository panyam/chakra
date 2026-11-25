# calc

The smallest agent ladder. An assistant answers six arithmetic questions,
first on its own and then with a calculator tool. Nobody needs this agent.
It's here so `make exercise-agents` has a ladder to run, and so you can see
how to read the table before a real agent shows up.

## The eval

Six questions, in `calc.go`. Two are a sum and a difference, which a model
gets right in its head. Four are products and quotients, such as `1234 * 5678`,
which a small model often gets wrong. Each case passes when the reply
contains the right number and the turn ended without an error.

## The steps

1. **model only.** The model and the system prompt, with no tools.
2. **+ calc tool.** Adds `calc`, a read-only function tool that works out one
   operation on two integers (`withCalc` in `calc.go`). The step is one
   `Configure` that sets `RunnerConfig.Tools`.

## The table

```
$ make exercise-agents AGENT=calc
on scripted
calc: 6 cases
step         pass  change  usd/correct  sec/correct
model only   2/6           -            0.00
+ calc tool  6/6   +4      -            0.00
```

`pass` is the cases that passed out of all of them. `change` is how many more
passed than on the step before. `usd/correct` is what the step spent over the
cases it got right, so a step that passes more for the same spend reads
cheaper. It shows `-` when the model has no price. `sec/correct` is the same
for wall time.

This run is on the scripted model, a stand-in that CI runs with no LLM. It
adds and subtracts in its head, calls `calc` when it's offered, and otherwise
guesses to one significant figure. Its numbers show the ladder works. Only a
live run says what the tool step buys on a real model:

```
make exercise-agents AGENT=calc LIVE=<connection> PROFILE=<agentchat.json>
```
