# codeq on haiku

A walk through codeq's ladder, one step at a time: what each step adds to the agent, what it passes on 10 cases, and what it costs.

## Walkthrough

### The job

codeq answers questions about Go code in two small modules, example.com/shop and example.com/ledger.
Every answer is a set of rows: import paths, or qualified names such as example.com/shop/orders.Place. eval.Rows grades them.
Three things trip a model. A question scoped to one module, where the tool also returns rows from the other. A question about non-test files, where one row comes only from a test. And a bare function name, which the callers tool refuses.

The steps:
1. tools only
2. + instructions
3. + stuck policy
4. + answer check

### The tool server

cmd/codeserver is an MCP server. The ladder builds it, runs it as its own process, and connects over stdio.

| tool | answers |
|---|---|
| packages | the packages of a module |
| imports | what one package imports |
| importers | what imports a package, from every module |
| find | the qualified name of a function or type |
| callers | what calls a function, from every module |

### 1. tools only

The model gets a two-line prompt and the five tools. Nothing tells it about the scoping rules.

**Inputs:**

- `action` = `run`
- `case` = `1`

```
  + tools: packages, imports, importers, find, callers

Running 10 cases:
  PASS   1.8s  $0.00025  What is the qualified name of the function Format?
  PASS   1.5s  $0.00024  What is the qualified name of the type Amount?
  PASS   1.7s  $0.00027  Which packages does example.com/shop/orders import?
  PASS   1.9s  $0.00033  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
  PASS   1.7s  $0.00030  Which packages in example.com/shop import example.com/ledger/journal?
  PASS   1.8s  $0.00030  Which functions in example.com/shop call example.com/ledger/journal.Post?
  PASS   1.7s  $0.00027  Which functions call Save from example.com/shop/internal/db?
  PASS   1.1s  $0.00023  Which packages make up the module example.com/ledger?
  PASS   1.6s  $0.00024  Which packages import example.com/shop/catalog?
  PASS   1.6s  $0.00026  Which packages in example.com/shop import example.com/shop/orders?

tools only passed 10 of 10.
This step cost $0.00269, and the walk has spent $0.00269.
The next step runs the same 10 cases, so expect about $0.00269 more.
```

### 2. + instructions

The prompt now says which tool answers what, and spells out the two scoping rules.

**Inputs:**

- `action` = `run`
- `case` = `1`

```
  + instructions:
      Use packages for a module's packages, imports for what one package imports, importers for what imports a package, find to turn a bare name into a qualified one, and callers for what calls a function.
      When a question says "in <module>", leave out every row from other modules.
      When a question says "non-test", leave out rows marked (test files only).

Running 10 cases:
  PASS   1.4s  $0.00026  What is the qualified name of the function Format?
  PASS   1.7s  $0.00027  What is the qualified name of the type Amount?
  PASS   1.6s  $0.00031  Which packages does example.com/shop/orders import?
  PASS   2.3s  $0.00035  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
  PASS   2.0s  $0.00033  Which packages in example.com/shop import example.com/ledger/journal?
  PASS   2.3s  $0.00037  Which functions in example.com/shop call example.com/ledger/journal.Post?
  PASS   1.8s  $0.00032  Which functions call Save from example.com/shop/internal/db?
  PASS   1.1s  $0.00025  Which packages make up the module example.com/ledger?
  PASS   1.5s  $0.00026  Which packages import example.com/shop/catalog?
  PASS   1.7s  $0.00028  Which packages in example.com/shop import example.com/shop/orders?

+ instructions passed 10 of 10, +0 on the step before.
This step cost $0.00300, and the walk has spent $0.00569.
The next step runs the same 10 cases, so expect about $0.00300 more.
```

### 3. + stuck policy

A stuck detector watches for a call repeated three times, and adds a note to the next model call saying so. It matters when callers refuses a bare name and the model asks again.

**Inputs:**

- `action` = `run`
- `case` = `1`

```
  + 1 step stage(s), run before each model call

Running 10 cases:
  PASS   1.4s  $0.00027  What is the qualified name of the function Format?
  PASS   1.7s  $0.00027  What is the qualified name of the type Amount?
  PASS   1.6s  $0.00031  Which packages does example.com/shop/orders import?
  PASS   2.0s  $0.00034  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
  PASS   1.8s  $0.00032  Which packages in example.com/shop import example.com/ledger/journal?
  PASS   1.8s  $0.00032  Which functions in example.com/shop call example.com/ledger/journal.Post?
  PASS   1.9s  $0.00031  Which functions call Save from example.com/shop/internal/db?
  PASS   1.2s  $0.00025  Which packages make up the module example.com/ledger?
  PASS   1.4s  $0.00025  Which packages import example.com/shop/catalog?
  PASS   1.7s  $0.00032  Which packages in example.com/shop import example.com/shop/orders?

+ stuck policy passed 10 of 10, +0 on the step before.
This step cost $0.00296, and the walk has spent $0.00865.
The next step runs the same 10 cases, so expect about $0.00296 more.
```

### 4. + answer check

A turn-end hook refuses an answer unless every line is a row the index knows, and sends the model back for rows alone.

**Inputs:**

- `action` = `run`
- `case` = `1`

```
  + 1 turn-end hook(s), which can send an answer back

Running 10 cases:
  PASS   2.2s  $0.00040  What is the qualified name of the function Format?
  PASS   2.4s  $0.00043  What is the qualified name of the type Amount?
  PASS   9.2s  $0.00179  Which packages does example.com/shop/orders import?
  PASS   2.5s  $0.00047  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
  PASS   2.5s  $0.00047  Which packages in example.com/shop import example.com/ledger/journal?
  PASS   2.5s  $0.00045  Which functions in example.com/shop call example.com/ledger/journal.Post?
  PASS   3.1s  $0.00048  Which functions call Save from example.com/shop/internal/db?
  PASS   1.8s  $0.00040  Which packages make up the module example.com/ledger?
  PASS   2.4s  $0.00041  Which packages import example.com/shop/catalog?
  PASS   3.2s  $0.00044  Which packages in example.com/shop import example.com/shop/orders?

+ answer check passed 10 of 10, +0 on the step before.
This step cost $0.00575, and the walk has spent $0.01440.
```

### 5. The table

```
codeq: 10 cases
step            pass   change  usd/correct  sec/correct  
tools only      10/10          $0.00027     1.65         
+ instructions  10/10  +0      $0.00030     1.74         
+ stuck policy  10/10  +0      $0.00030     1.65         
+ answer check  10/10  +0      $0.00057     3.18         
```

