# codeq

An agent that answers questions about Go code through a tool server over MCP
(#351). It works like Declaire's agentbench chakra arm, which runs
`agentchat exec` with only Declaire's tools. Here the tool server is
`cmd/codeserver`, a small MCP server that indexes two modules in `testdata`,
so CI needs no Declaire checkout. The shop module has nine packages. Its
call chains run up to three hops deep, from `api.Handle` through `checkout.Run`
and `orders.Place` to `journal.Post`.

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

Fifteen questions in `codeq.go`, shaped like Declaire's bench families, each
with a tag: definition, imports, reverse hop, negation, callers or
multi-hop. Each expects a set of rows. `eval.Rows` checks that every expected
row is present and no other row the index knows. `eval.NoError` checks that
any tool error was recovered by a later call to the same tool. These trip a
model:

- **Scoping.** "Which packages in example.com/shop import
  example.com/ledger/journal?" `importers` also returns
  `example.com/ledger/audit`, from the other module.
- **Lookalikes.** Both modules declare a `Format`. Asked for ledger's, `find`
  returns both, and a model that mentions the shop's fails.
- **Test files.** "...counting only non-test files?" `importers` returns
  `example.com/shop/payments` marked `(test files only)`.
- **A bare name.** "Which functions call Save from
  example.com/shop/internal/db?" `callers("Save")` is refused until the
  model qualifies the name, with `find` or by itself.
- **Negation.** "Which packages in example.com/shop import nothing from
  example.com/ledger?" No tool answers it directly. The model has to read
  every package's imports and leave the users out.
- **Multi-hop.** "Which functions call `db.Save`, directly or through other
  functions?" `callers` gives one hop, so the model has to call it again on
  each caller until nothing new turns up. No step in this ladder helps with
  that. Mission 395 works on breaking a problem into hops (#385, #390).

`TestImportAnswersAgreeWithGoList` re-derives the import questions' rows with
`go list`. `TestQuestionAnswersMatchTheIndex` walks the index to re-derive
the multi-hop and negation answers. So no answer can drift from the fixture.

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
$ make exercise
on scripted
codeq: 15 cases
step            pass   change  usd/correct  sec/correct
tools only      4/15           -            0.00
+ instructions  9/15   +5      -            0.00
+ stuck policy  10/15  +1      -            0.00
+ answer check  12/15  +2      -            0.00
```

The scripted model copies Haiku's habits, one for each step to fix. It keeps
every row a tool returns, so it names both `Format`s. It repeats a refused
`callers` call. And it answers some questions in prose, such as a negation
that names the packages it left out as well. A fourth habit stays. On a
multi-hop question it stops after one hop, so the three multi-hop cases fail
on every step. So the numbers show the steps are wired, and say nothing about
a real model.

## A live run

On Claude Haiku 5.5, on 2026-11-26, with `REPEATS=3`. Each case ran three
times. The pass column shows the mean, with the lowest and highest run in
brackets.

```
$ make exercise LIVE=haiku REPEATS=3
step            pass             change  usd/correct  sec/correct
tools only      9.7/15 (9-11)            $0.00084     5.02
+ instructions  10.7/15 (9-12)   +1.0    $0.00081     4.67
+ stuck policy  10.0/15 (9-11)   -0.7    $0.00084     4.82
+ answer check  14.0/15 (14-14)  +4.0    $0.00089     4.97
```

The bare step fails a third of the cases, and the answer check passes all
but one. The middle two steps move within the spread, so these runs don't
tell them apart. Per step:

- **tools only.** Both lookalike questions fail on every run. Haiku gives
  the right `Format` and then mentions the other module's: "The search also
  found a `Format` in the other module, `example.com/shop/receipt.Format`".
  The negation fails on every run, and the multi-hop questions fail on two
  or three of them.
- **+ instructions.** The scoping rule fixes the lookalikes on half the
  runs, and the negation on two of three. The multi-hop questions still
  fail.
- **+ stuck policy.** The step fixes no case the instructions left failing.
  Its numbers sit inside the same spread.
- **+ answer check.** Sent back for rows alone, Haiku drops the mentions and
  fixes the negation. It also passes two of the three multi-hop questions
  on every run. "Which functions
  in example.com/shop reach journal.Post only through other functions" fails
  on every run.

[WALK.md](WALK.md) is one recorded walk on Haiku, with every case's line and
what each step added. In that walk the steps passed 10, 10, 10 and 15, and
it cost $0.039. It re-renders from `walk-haiku.json` with no model calls:
`make walk-md LIVE=haiku` in this directory.

Gemini 3.5 Flash-Lite passed 12 and 13 of 15 on the first two steps in one
run, and failed every multi-hop case. Its later steps hit the free tier's
limit of 500 requests a day, so they have no numbers here.

The first version of this eval had ten questions over a smaller fixture.
Haiku and Sonnet passed all ten with tools alone, so no step could show a
gain (#365). The lookalike `Format`, the negation and the multi-hop
questions are the cases that make Haiku fail.

Set `CASES=1` to print each failing case with its scores and the model's
answer.

## Chatting with it

`make chat LIVE=sonnet`, in this directory, opens agentchat with the last step's
instructions and codeserver attached. The stuck policy and the answer check
are code, so the chat runs without them.
