# codeq

An agent that answers questions about Go code through a tool server over MCP
(#351). It works like Declaire's agentbench chakra arm, which runs
`agentchat exec` with only Declaire's tools. Here the tool server is
`cmd/codeserver`, a small MCP server that indexes two modules in `testdata`,
so CI needs no Declaire checkout.

## The tool server

`codeserver -root testdata` reads `example.com/shop` and `example.com/ledger`
with `go/parser` and serves five read-only tools over stdio:

| tool | answers |
|---|---|
| `packages` | the packages of a module |
| `imports` | what one package imports |
| `importers` | what imports a package, from every module |
| `find` | the qualified name of a function or type, and where it's declared |
| `callers` | what calls a function, from every module |

Each tool answers one row per line. A row is an import path, or an import
path, a dot and a name, such as `example.com/shop/orders.Place`. An import
that only `_test.go` files make ends in ` (test files only)`. `callers` needs
a qualified name and refuses a bare one, such as `Save`.

The ladder builds the server, starts it as its own process and connects
through `agent.ClientSource`, so every run goes through the server's public
MCP surface. `Ladder` returns a `stop` that closes the client, which stops
the process.

## The eval

Ten questions in `codeq.go`, shaped like Declaire's bench families. Each
expects a set of rows, graded by `eval.Rows`: every expected row present,
and no other row the index knows. Three things trip a model:

- **Scoping.** "Which packages in example.com/shop import
  example.com/ledger/journal?" `importers` also returns
  `example.com/ledger/audit`, from the other module.
- **Test files.** "...counting only non-test files?" `importers` returns
  `example.com/shop/payments` marked `(test files only)`.
- **A bare name.** "Which functions call Save from
  example.com/shop/internal/db?" `callers("Save")` is refused until the
  model qualifies the name, with `find` or by itself.

`TestImportAnswersAgreeWithGoList` re-derives the import questions' rows with
`go list`, so they can't drift from the fixture.

When an answer has lines holding a row and nothing else, `eval.Rows` grades
those lines, so a list followed by "audit imports it too, but it's in the
ledger module" grades the list. Two mentions never count as wrong answers: a
row the question names, and the package of a wanted function.

## The steps

1. **tools only.** The model, a two-line prompt, and the five tools.
2. **+ instructions.** Which tool answers what, and the two scoping rules:
   leave out other modules when a question says "in <module>", and test-only
   rows when it says "non-test".
3. **+ stuck policy.** `agent.NewStuckDetector` with its default `StuckNote`.
   When the model repeats a call, the next model call carries a note saying
   so.
4. **+ answer check.** A `TurnEnd` hook that refuses an answer unless every
   line is a row the index knows, and sends the model back for rows alone.

## The table

On the scripted model CI runs:

```
$ make exercise-agents AGENT=codeq
on scripted
codeq: 10 cases
step            pass   change  usd/correct  sec/correct
tools only      5/10           -            0.00
+ instructions  8/10   +3      -            0.00
+ stuck policy  9/10   +1      -            0.00
+ answer check  10/10  +1      -            0.00
```

The scripted model has one habit for each step to fix: it keeps every row a
tool returns, repeats a refused `callers` call, and answers one question in
prose with short names. So the numbers show the steps are wired, and say
nothing about a real model.

## A live run

On Claude Haiku 5.5 and Sonnet 5.5, on 2026-11-25. [WALK.md](WALK.md) is a
recorded walk on Haiku, with every case's line and what each step added. It
re-renders from `walk-haiku.json` with no model calls:
`make walk-agent AGENT=codeq LIVE=haiku ARGS="--doc md --from codeq/walk-haiku.json"`.


```
$ make exercise-agents AGENT=codeq LIVE=haiku PROFILE=~/.agentchat/exercise-agents.json
on haiku
codeq: 10 cases
step            pass   change  usd/correct  sec/correct
tools only      10/10          $0.00027     1.73
+ instructions  10/10  +0      $0.00030     1.82
+ stuck policy  10/10  +0      $0.00031     1.86
+ answer check  10/10  +0      $0.00057     3.30

on sonnet
codeq: 10 cases
step            pass   change  usd/correct  sec/correct
tools only      10/10          $0.0053      1.87
+ instructions  10/10  +0      $0.0056      2.10
+ stuck policy  10/10  +0      $0.0056      2.42
+ answer check  10/10  +0      $0.0101      4.19
```

Both models pass everything with tools alone. Each step after that costs
more and passes nothing more, and the answer check nearly doubles the cost,
since it sends most prose answers back for a second reply. On Declaire's
bench, scoping failures came from large real repos with many lookalike
rows. These two modules are too small to trip a current hosted model.

Runs vary, too. An earlier run of the same ladder had Haiku answer "catalog
and orders" by short name on one step, which failed. A step's row is one run
per case.

Set `CASES=1` to print each failing case with its scores and the model's
answer.

## Chatting with it

`make chat-agent AGENT=codeq LIVE=sonnet` opens agentchat with the last step's
instructions and codeserver attached. The stuck policy and the answer check
are code, so the chat runs without them.
