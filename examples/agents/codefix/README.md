# codefix

A coding agent that edits a Go module until its tests pass (#352). Each case
copies `testdata/base`, a small module called `example.com/shopkit`, into a
temporary directory and brings in one bug. The agent gets a bug report. The
case passes when `go test ./...` passes in the copy and no `_test.go` file
changed, so the agent can't fix a test instead of the code.

```
cd examples/agents/codefix
make tui                 # walk the ladder on the scripted model
make note LIVE=haiku     # on a real model, as notebook cells
make exercise            # the whole ladder, table at the end
```

## The tasks

`shopkit` has four packages, each with tests: `money` (Sum, Max), `cart`
(Total, Priciest), `discount` (Apply over a $10.00 threshold) and `report`
(one summary line). `tasks.go` lists five bugs as edits to the correct
module, so undoing an edit is the fix. `TestEachTaskFailsUntilFixed` checks
each bug fails `go test` and each fix passes.

| task | bug report | where the bug is |
|---|---|---|
| sum | Cart totals come out low. money.Sum seems to miss an item. | `money.Sum` starts at the second item |
| threshold | An order of exactly $10.00 gets no discount. | `>` where it should be `>=` |
| nilcart | A test in the cart package panics. | `Cart.Total` lost its nil check |
| priciest | The report's summary line shows the wrong priciest item. | `money.Max`, one package away from where it shows |
| twobugs | Discounts round down, and cents under ten lose their leading zero. | one bug in `discount`, one in `report` |

## The steps

1. **read + edit.** `ext/files` (`read_file`, `edit_file`, and the rest),
   rooted at the case's copy. `edit_file` takes the hash `read_file`
   returned, so a model has to read a file before it edits it.
2. **+ run tests.** `ext/exec` offers `run_tests`, which runs `go test ./...`
   and shows which test failed, and where a panic happened.
3. **+ repo map.** A retrieval stage puts a map of the module before the
   first model call: each file, what it declares, and the functions it calls
   in other packages. The map shows that `cart.Priciest` calls `money.Max`.
4. **+ plan slot.** A `plan` slot the model writes with `write_slot`. Its
   text is shown on every call.
5. **+ test gate.** A turn-end hook runs the tests when the model says it's
   done. While they fail, it sends the model back with the output.

A step only says which tools are on. Each case's `Setup` makes the copy and
points a shared workspace at it, and the tools, the repo map, the plan slot
and the gate all read the workspace. Before any case runs, the tools list
against the clean module, so a walk can show them, and a call is refused.

The test tool runs `go test ./...` with no arguments in a temporary copy, so
it runs with `exec.Unconfined()` rather than a sandbox that would need to
allow the Go build cache.

## The table

On the scripted model CI runs:

```
$ make exercise
codefix: 5 cases
step         pass  change  usd/correct  sec/correct
read + edit  1/5           -            0.29
+ run tests  2/5   +1      -            1.09
+ repo map   3/5   +1      -            0.71
+ plan slot  4/5   +1      -            0.54
+ test gate  5/5   +1      -            0.88
```

The scripted model has one habit for each step to fix. It can't find the
panicking line without the test output. It looks for the priciest bug in
`report.go` without the repo map. It fixes one of two bugs without a plan.
And on the threshold task it changes the constant, which breaks
`TestThresholdIsTenDollars`, and calls it done until the gate sends it back.
So the numbers show the steps are wired, and say nothing about a real model.

## A live run

On Claude Haiku 5.5, on 2026-11-25. [WALK.md](WALK.md) is the recorded walk,
with every case's answer. `make walk-md LIVE=haiku` re-renders it from
`walk-haiku.json` with no model calls.

```
step         pass  change  usd/correct  sec/correct
read + edit  5/5           $0.0033      7.58
+ run tests  5/5   +0      $0.0022      5.30
+ repo map   5/5   +0      $0.0014      4.23
+ plan slot  5/5   +0      $0.0015      4.25
+ test gate  5/5   +0      $0.0015      4.32
```

Haiku fixes all five bugs from the start, even without running the tests.
On the two-bug task it said "I couldn't run go test because I have no shell
here", and fixed both anyway. The steps don't add passes here. They make a
fix cheaper and faster. Running the tests cut the cost per fix by a third,
since the model stopped reading every file to find the bug, and the repo
map cut it by about a third again. The plan slot and the gate cost a little
and changed nothing. The whole walk cost $0.05.

The tasks are small enough for Haiku to see the bug from the report and one
file. #365 asks for a harder eval for codeq, and the same applies here.
