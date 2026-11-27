# codeq on haiku

A walk through codeq's ladder, one step at a time: what each step adds to the agent, what it passes on 15 cases, and what it costs.

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

- `watch` = `all`

```
  + tools: packages, imports, importers, find, callers

Running 15 cases:
  FAIL   1.6s  $0.00028  What is the qualified name of the function Format in example.com/ledger?
         got  The qualified name is **`example.com/ledger/money.Format`**, declared at `example.com/ledger/money/money.go...
         want example.com/ledger/money.Format
         Rows: 1 of 1 rows, extra example.com/shop/receipt.Format
  PASS   1.3s  $0.00023  What is the qualified name of the type Amount?
         got  The type `Amount` is declared at `example.com/ledger/money/money.go:7`, so its qualified name is `example.c...
         want example.com/ledger/money.Amount
  PASS   1.5s  $0.00027  Which packages does example.com/shop/orders import?
         got  `example.com/shop/orders` imports these packages: | - `errors` | - `example.com/ledger/journal` | - `exampl...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.2s  $0.00035  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  Three packages in example.com/shop import example.com/ledger/money in non-test files: | - example.com/shop/...
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   2.0s  $0.00031  Which packages in example.com/shop import example.com/ledger/journal?
         got  Three packages in example.com/shop import example.com/ledger/journal: | - example.com/shop/orders | - examp...
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  FAIL   1.8s  $0.00029  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  Two functions in example.com/shop call `example.com/ledger/journal.Post`: | 1. `example.com/shop/orders.Pla...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
         Rows: 2 of 2 rows, extra example.com/ledger/audit.Replay
  PASS   1.6s  $0.00028  Which functions call Save from example.com/shop/internal/db?
         got  Two functions call `Save` from `example.com/shop/internal/db`: | 1. `example.com/shop/orders.Place` | 2. `e...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.0s  $0.00023  Which packages make up the module example.com/ledger?
         got  The module example.com/ledger contains three packages: | - example.com/ledger/audit | - example.com/ledger/...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   1.4s  $0.00025  Which packages import example.com/shop/catalog?
         got  Only one package imports `example.com/shop/catalog`: **`example.com/shop/orders`**. I searched every module...
         want example.com/shop/orders
  PASS   1.6s  $0.00027  Which packages in example.com/shop import example.com/shop/orders?
         got  Two packages in `example.com/shop` import `example.com/shop/orders`: | - `example.com/shop/checkout` | - `e...
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  FAIL   1.6s  $0.00029  What is the qualified name of the function Format in example.com/shop?
         got  The qualified name is **`example.com/shop/receipt.Format`**, declared at `example.com/shop/receipt/receipt....
         want example.com/shop/receipt.Format
         Rows: 1 of 1 rows, extra example.com/ledger/money.Format
  FAIL   7.2s  $0.00125  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  Four packages in example.com/shop import nothing from example.com/ledger in their non-test files: | - **api...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
         Rows: 0 of 4 rows, missing example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, exa...
  FAIL  10.0s  $0.00166  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  Four functions in example.com/shop reach `journal.Post` only through intermediate calls: | 1. **`refunds.Is...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 0 of 4 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   6.2s  $0.00114  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  Five functions reach `example.com/shop/internal/db.Save`: | **Direct callers of `db.Save`:** | - `example.c...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 2 of 5 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   5.8s  $0.00117  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  The example.com/shop/api package depends on two packages in example.com/ledger: **example.com/ledger/journa...
         want example.com/ledger/journal, example.com/ledger/money
         Rows: 2 of 2 rows, extra example.com/ledger/audit

tools only passed 8 of 15.
This step cost $0.00827, and the walk has spent $0.00827.
The next step runs the same 15 cases, so expect about $0.00827 more.
```

### 2. + instructions

The prompt now says which tool answers what, and spells out the two scoping rules.

**Inputs:**

- `watch` = `all`

```
  + instructions:
      Use packages for a module's packages, imports for what one package imports, importers for what imports a package, find to turn a bare name into a qualified one, and callers for what calls a function.
      When a question says "in <module>", leave out every row from other modules.
      When a question says "non-test", leave out rows marked (test files only).

Running 15 cases:
  PASS   1.3s  $0.00025  What is the qualified name of the function Format in example.com/ledger?
         got  The qualified name of `Format` in example.com/ledger is **example.com/ledger/money.Format**, declared at ex...
         want example.com/ledger/money.Format
  PASS   1.3s  $0.00026  What is the qualified name of the type Amount?
         got  The type `Amount` is `example.com/ledger/money.Amount`. It is declared at `example.com/ledger/money/money.g...
         want example.com/ledger/money.Amount
  PASS   2.0s  $0.00032  Which packages does example.com/shop/orders import?
         got  `example.com/shop/orders` imports these packages: | - `errors` | - `example.com/ledger/journal` | - `exampl...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.1s  $0.00035  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  Three packages in example.com/shop import example.com/ledger/money in non-test files: | - example.com/shop/...
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   2.1s  $0.00032  Which packages in example.com/shop import example.com/ledger/journal?
         got  Three packages in example.com/shop import example.com/ledger/journal: | - example.com/shop/orders | - examp...
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   1.8s  $0.00031  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  Two functions in example.com/shop call `example.com/ledger/journal.Post`: | - `example.com/shop/orders.Plac...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   2.2s  $0.00034  Which functions call Save from example.com/shop/internal/db?
         got  Two functions call `Save` from `example.com/shop/internal/db`: | - `example.com/shop/orders.Place` | - `exa...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.3s  $0.00025  Which packages make up the module example.com/ledger?
         got  The module example.com/ledger has three packages: | - example.com/ledger/audit | - example.com/ledger/journ...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   1.5s  $0.00027  Which packages import example.com/shop/catalog?
         got  Only one package imports `example.com/shop/catalog`: **`example.com/shop/orders`**.
         want example.com/shop/orders
  PASS   1.6s  $0.00028  Which packages in example.com/shop import example.com/shop/orders?
         got  Two packages in example.com/shop import example.com/shop/orders: | - example.com/shop/checkout | - example....
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  PASS   1.5s  $0.00029  What is the qualified name of the function Format in example.com/shop?
         got  The function is `example.com/shop/receipt.Format`, declared at `example.com/shop/receipt/receipt.go:11`.
         want example.com/shop/receipt.Format
  FAIL   4.5s  $0.00092  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  Five packages in example.com/shop import nothing from example.com/ledger in their non-test files: | - `exam...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
         Rows: 4 of 4 rows, extra example.com/shop/payments
  FAIL   5.7s  $0.00091  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  Three shop functions reach `journal.Post` only indirectly: | - **`example.com/shop/checkout.Run`**: calls `...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 3 of 4 rows, missing example.com/shop/api.Handle, extra example.com/ledger/audit.Replay
  PASS   5.9s  $0.00110  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  Five functions call `db.Save`, either directly or through other functions: | - **Directly:** `example.com/s...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
  PASS   9.3s  $0.00158  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  `example.com/shop/api` depends on two packages in `example.com/ledger`, both indirectly: | - **example.com/...
         want example.com/ledger/journal, example.com/ledger/money

+ instructions passed 13 of 15, +5 on the step before. That's one run of a live model, so the change may be noise.
This step cost $0.00775, and the walk has spent $0.01603.
The next step runs the same 15 cases, so expect about $0.00775 more.
```

### 3. + stuck policy

A stuck detector watches for a call repeated three times, and adds a note to the next model call saying so. It matters when callers refuses a bare name and the model asks again.

**Inputs:**

- `watch` = `all`

```
  + 1 step stage(s), run before each model call

Running 15 cases:
  FAIL   1.5s  $0.00028  What is the qualified name of the function Format in example.com/ledger?
         got  The qualified name is **example.com/ledger/money.Format**, declared at `example.com/ledger/money/money.go:1...
         want example.com/ledger/money.Format
         Rows: 1 of 1 rows, extra example.com/shop/receipt.Format
  PASS   1.2s  $0.00025  What is the qualified name of the type Amount?
         got  The qualified name of the type Amount is `example.com/ledger/money.Amount`. It is declared at `example.com/...
         want example.com/ledger/money.Amount
  PASS   1.7s  $0.00031  Which packages does example.com/shop/orders import?
         got  `example.com/shop/orders` imports these packages: | - `errors` | - `example.com/ledger/journal` | - `exampl...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   1.7s  $0.00031  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  Three packages in example.com/shop import example.com/ledger/money from non-test files: | - example.com/sho...
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   2.0s  $0.00033  Which packages in example.com/shop import example.com/ledger/journal?
         got  Three packages in example.com/shop import example.com/ledger/journal: | - example.com/shop/orders | - examp...
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   2.0s  $0.00035  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  Two functions in example.com/shop call `example.com/ledger/journal.Post`: | - `example.com/shop/orders.Plac...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.8s  $0.00031  Which functions call Save from example.com/shop/internal/db?
         got  Two functions call `Save` from `example.com/shop/internal/db`: | - `example.com/shop/orders.Place` | - `exa...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.2s  $0.00025  Which packages make up the module example.com/ledger?
         got  The module example.com/ledger has three packages: | - example.com/ledger/audit | - example.com/ledger/journ...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   1.4s  $0.00026  Which packages import example.com/shop/catalog?
         got  Only one package imports `example.com/shop/catalog`: **example.com/shop/orders**.
         want example.com/shop/orders
  PASS   2.1s  $0.00033  Which packages in example.com/shop import example.com/shop/orders?
         got  Two packages in example.com/shop import `example.com/shop/orders`: | - `example.com/shop/checkout` | - `exa...
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  FAIL   1.6s  $0.00029  What is the qualified name of the function Format in example.com/shop?
         got  The qualified name is **`example.com/shop/receipt.Format`**, declared at `example.com/shop/receipt/receipt....
         want example.com/shop/receipt.Format
         Rows: 1 of 1 rows, extra example.com/ledger/money.Format
  FAIL   4.7s  $0.00094  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  Five packages in example.com/shop import nothing from example.com/ledger in non-test files: | - **api**: im...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
         Rows: 1 of 4 rows, missing example.com/shop/api, example.com/shop/cmd/shopd, example.com/shop/internal/db, ...
  FAIL   9.7s  $0.00175  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  Four functions in example.com/shop reach `journal.Post` only indirectly: | - **`checkout.Run`** calls `orde...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 0 of 4 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   5.6s  $0.00107  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  Eight functions call `example.com/shop/internal/db.Save`, directly or indirectly: | **Direct callers:** | -...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 2 of 5 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  PASS   7.9s  $0.00129  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  `example.com/shop/api` depends on two packages in `example.com/ledger`, both transitively: | - **`example.c...
         want example.com/ledger/journal, example.com/ledger/money

+ stuck policy passed 10 of 15, -3 on the step before. That's one run of a live model, so the change may be noise.
This step cost $0.00832, and the walk has spent $0.02435.
The next step runs the same 15 cases, so expect about $0.00832 more.
```

### 4. + answer check

A turn-end hook refuses an answer unless every line is a row the index knows, and sends the model back for rows alone.

**Inputs:**

- `watch` = `all`

```
  + 1 turn-end hook(s), which can send an answer back

Running 15 cases:
  PASS   1.9s  $0.00037  What is the qualified name of the function Format in example.com/ledger?
         got  example.com/ledger/money.Format
         want example.com/ledger/money.Format
  PASS   2.5s  $0.00043  What is the qualified name of the type Amount?
         got  example.com/ledger/money.Amount
         want example.com/ledger/money.Amount
  PASS   9.1s  $0.00167  Which packages does example.com/shop/orders import?
         got  errors | example.com/ledger/journal | example.com/ledger/money | example.com/shop/catalog | example.com/sho...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.8s  $0.00050  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  example.com/shop/catalog | example.com/shop/orders | example.com/shop/receipt
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   2.6s  $0.00048  Which packages in example.com/shop import example.com/ledger/journal?
         got  example.com/shop/orders | example.com/shop/payments | example.com/shop/refunds
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   2.8s  $0.00049  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   2.9s  $0.00047  Which functions call Save from example.com/shop/internal/db?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.9s  $0.00039  Which packages make up the module example.com/ledger?
         got  example.com/ledger/audit | example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   2.3s  $0.00039  Which packages import example.com/shop/catalog?
         got  example.com/shop/orders
         want example.com/shop/orders
  PASS   2.6s  $0.00045  Which packages in example.com/shop import example.com/shop/orders?
         got  example.com/shop/checkout | example.com/shop/cmd/shopd
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  PASS   2.8s  $0.00047  What is the qualified name of the function Format in example.com/shop?
         got  example.com/shop/receipt.Format
         want example.com/shop/receipt.Format
  PASS   9.2s  $0.00166  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  example.com/shop/api | example.com/shop/checkout | example.com/shop/cmd/shopd | example.com/shop/internal/db
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
  FAIL   9.5s  $0.00174  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  example.com/shop/checkout.Run | example.com/shop/api.Handle | example.com/shop/cmd/shopd.main
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 3 of 4 rows, missing example.com/shop/refunds.Issue
  PASS   7.3s  $0.00143  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge | example.com/shop/checkout.Run | example....
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
  PASS  11.1s  $0.00176  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/journal, example.com/ledger/money

+ answer check passed 14 of 15, +4 on the step before. That's one run of a live model, so the change may be noise.
This step cost $0.01272, and the walk has spent $0.03706.
```

### 5. The table

```
codeq: 15 cases
step            pass   change  usd/correct  sec/correct  
tools only      8/15           $0.0010      5.88         
+ instructions  13/15  ~+5     $0.00060     3.40         
+ stuck policy  10/15  ~-3     $0.00083     4.61         
+ answer check  14/15  ~+4     $0.00091     5.10         

~ one run of a live model shows no spread, so the change may be noise. Running each case more than once measures it.
```

