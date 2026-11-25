# calc

The smallest agent ladder. An assistant answers six arithmetic questions,
first on its own and then with a calculator tool. Nobody needs this agent.
It's here so `make exercise-agents` has a ladder to run, and so you can see
how to read the table before a real agent shows up.

## The eval

Six questions, in `calc.go`. Two are a sum and a difference, and four are
products and quotients, such as `1234 * 5678`. Each case passes when the reply
contains the right number and the turn ended without an error. Current hosted
models get all six right with no tool (see the live run below).

## The steps

1. **model only.** The model and the system prompt, with no tools.
2. **+ calc tool.** Adds `calc`, a read-only function tool that works out one
   operation on two integers (`withCalc` in `calc.go`). The step is one
   `Configure` that sets `RunnerConfig.Tools`.

## The table

```
$ make exercise
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
guesses to one significant figure. Its numbers show the ladder works, and
say nothing about a real model.

## A live run

`LIVE` names a connection in an agentchat config, and its `price` prices the
run. Here are Claude Haiku 5.5 and Sonnet 5.5, on 2026-11-25:

```
$ make exercise LIVE=haiku
on haiku
calc: 6 cases
step         pass  change  usd/correct  sec/correct
model only   6/6           $0.000019    0.59
+ calc tool  6/6   +0      $0.00013     1.00

on sonnet
calc: 6 cases
step         pass  change  usd/correct  sec/correct
model only   6/6           $0.00029     0.86
+ calc tool  6/6   +0      $0.0025      1.47
```

Both models get every case in their heads. The tool step passes nothing more,
and a correct answer costs about 7x to 9x as much and takes longer. That's a
real result: a tool the model doesn't need is pure overhead. A small local
model is likelier to miss `1234 * 5678`, and the tool step can show a gain
there.
