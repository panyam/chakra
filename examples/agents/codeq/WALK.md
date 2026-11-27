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
  FAIL   2.0s  $0.00027  What is the qualified name of the function Format in example.com/ledger?
         got  The function is **`example.com/ledger/money.Format`**, declared at `example.com/ledger/money/money.go:13`. ...
         want example.com/ledger/money.Format
         Rows: 1 of 1 rows, extra example.com/shop/receipt.Format
  PASS   1.5s  $0.00025  What is the qualified name of the type Amount?
         got  The qualified name of the type `Amount` is **`example.com/ledger/money.Amount`**. It's declared at `example...
         want example.com/ledger/money.Amount
  PASS   1.4s  $0.00027  Which packages does example.com/shop/orders import?
         got  `example.com/shop/orders` imports these packages: | - `errors` | - `example.com/ledger/journal` | - `exampl...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.4s  $0.00035  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  Three packages in example.com/shop import example.com/ledger/money in non-test files: | - example.com/shop/...
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   2.1s  $0.00031  Which packages in example.com/shop import example.com/ledger/journal?
         got  Three packages in example.com/shop import example.com/ledger/journal: | - example.com/shop/orders | - examp...
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   1.9s  $0.00029  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  Two functions in example.com/shop call `example.com/ledger/journal.Post`: | - `example.com/shop/orders.Plac...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.7s  $0.00029  Which functions call Save from example.com/shop/internal/db?
         got  Two functions call `Save` from `example.com/shop/internal/db`: | 1. `example.com/shop/orders.Place` | 2. `e...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.0s  $0.00023  Which packages make up the module example.com/ledger?
         got  The module example.com/ledger contains three packages: | - example.com/ledger/audit | - example.com/ledger/...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   1.9s  $0.00026  Which packages import example.com/shop/catalog?
         got  Only one package imports `example.com/shop/catalog`: `example.com/shop/orders`. The lookup covered every mo...
         want example.com/shop/orders
  PASS   1.7s  $0.00024  Which packages in example.com/shop import example.com/shop/orders?
         got  Two packages in example.com/shop import example.com/shop/orders: | - example.com/shop/checkout | - example....
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  FAIL   1.5s  $0.00025  What is the qualified name of the function Format in example.com/shop?
         got  The function Format in example.com/shop is `example.com/shop/receipt.Format`, declared at example.com/shop/...
         want example.com/shop/receipt.Format
         Rows: 1 of 1 rows, extra example.com/ledger/money.Format
  FAIL   6.9s  $0.00118  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  Four packages in example.com/shop import nothing from example.com/ledger in their non-test files: | - **api...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
         Rows: 1 of 4 rows, missing example.com/shop/api, example.com/shop/cmd/shopd, example.com/shop/internal/db, ...
  FAIL   8.9s  $0.00144  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  Three shop functions reach `journal.Post` only through other functions: | - **`example.com/shop/checkout.Ru...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 3 of 4 rows, missing example.com/shop/refunds.Issue, extra example.com/ledger/audit.Replay
  FAIL   5.9s  $0.00110  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  Eight functions reach `example.com/shop/internal/db.Save`, either directly or through other functions: | - ...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 2 of 5 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  PASS   6.9s  $0.00117  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  The shop API depends on two packages in example.com/ledger, both through the shop's `checkout` package: | -...
         want example.com/ledger/journal, example.com/ledger/money

tools only passed 10 of 15.
This step cost $0.00790, and the walk has spent $0.00790.
The next step runs the same 15 cases, so expect about $0.00790 more.
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
  PASS   1.5s  $0.00027  What is the qualified name of the function Format in example.com/ledger?
         got  The qualified name is **`example.com/ledger/money.Format`**, declared at `example.com/ledger/money/money.go...
         want example.com/ledger/money.Format
  PASS   1.3s  $0.00025  What is the qualified name of the type Amount?
         got  The type Amount is qualified as `example.com/ledger/money.Amount`. It is declared in `example.com/ledger/mo...
         want example.com/ledger/money.Amount
  PASS   1.9s  $0.00031  Which packages does example.com/shop/orders import?
         got  `example.com/shop/orders` imports these packages: | - `errors` | - `example.com/ledger/journal` | - `exampl...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.2s  $0.00035  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  Three packages in example.com/shop import example.com/ledger/money in non-test files: | - example.com/shop/...
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   2.0s  $0.00034  Which packages in example.com/shop import example.com/ledger/journal?
         got  Three packages in example.com/shop import example.com/ledger/journal: | - example.com/shop/orders | - examp...
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   2.2s  $0.00036  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  Two functions in example.com/shop call `example.com/ledger/journal.Post`: | - `example.com/shop/orders.Plac...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   3.4s  $0.00055  Which functions call Save from example.com/shop/internal/db?
         got  Two functions call `example.com/shop/internal/db.Save`: | - `example.com/shop/orders.Place` | - `example.co...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.2s  $0.00025  Which packages make up the module example.com/ledger?
         got  The module example.com/ledger has three packages: | - example.com/ledger/audit | - example.com/ledger/journ...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   1.4s  $0.00027  Which packages import example.com/shop/catalog?
         got  Only one package imports `example.com/shop/catalog`: **`example.com/shop/orders`**.
         want example.com/shop/orders
  PASS   1.8s  $0.00029  Which packages in example.com/shop import example.com/shop/orders?
         got  Two packages in example.com/shop import example.com/shop/orders: | - example.com/shop/checkout | - example....
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  FAIL   1.8s  $0.00031  What is the qualified name of the function Format in example.com/shop?
         got  The qualified name is **`example.com/shop/receipt.Format`**, declared at `example.com/shop/receipt/receipt....
         want example.com/shop/receipt.Format
         Rows: 1 of 1 rows, extra example.com/ledger/money.Format
  FAIL   8.3s  $0.00142  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  Four packages in example.com/shop import nothing from example.com/ledger in their non-test files: | - **exa...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
         Rows: 4 of 4 rows, extra example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money, ex...
  FAIL   7.7s  $0.00134  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  Three functions in example.com/shop reach `journal.Post` only through other functions: | - **`example.com/s...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 3 of 4 rows, missing example.com/shop/refunds.Issue, extra example.com/ledger/audit.Replay
  FAIL   6.0s  $0.00109  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  Eight functions call `example.com/shop/internal/db.Save`, directly or indirectly: | **Direct callers:** | -...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 2 of 5 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   7.4s  $0.00129  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  The shop/api package depends on two ledger packages, **journal** and **money**. It does not depend on **aud...
         want example.com/ledger/journal, example.com/ledger/money
         Rows: 0 of 2 rows, missing example.com/ledger/journal, example.com/ledger/money, extra example.com/shop/che...

+ instructions passed 10 of 15, +0 on the step before.
This step cost $0.00869, and the walk has spent $0.01659.
The next step runs the same 15 cases, so expect about $0.00869 more.
```

### 3. + stuck policy

A stuck detector watches for a call repeated three times, and adds a note to the next model call saying so. It matters when callers refuses a bare name and the model asks again.

**Inputs:**

- `watch` = `all`

```
  + 1 step stage(s), run before each model call

Running 15 cases:
  PASS   1.3s  $0.00025  What is the qualified name of the function Format in example.com/ledger?
         got  The function Format in example.com/ledger is **example.com/ledger/money.Format**, declared at example.com/l...
         want example.com/ledger/money.Format
  PASS   1.4s  $0.00027  What is the qualified name of the type Amount?
         got  The type `Amount` is `example.com/ledger/money.Amount`. It is declared in `example.com/ledger/money/money.g...
         want example.com/ledger/money.Amount
  PASS   1.8s  $0.00031  Which packages does example.com/shop/orders import?
         got  `example.com/shop/orders` imports these packages: | - `errors` | - `example.com/ledger/journal` | - `exampl...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.1s  $0.00035  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  Three packages in example.com/shop import example.com/ledger/money in non-test files: | - example.com/shop/...
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   1.9s  $0.00033  Which packages in example.com/shop import example.com/ledger/journal?
         got  Three packages in example.com/shop import example.com/ledger/journal: | - example.com/shop/orders | - examp...
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   2.3s  $0.00036  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  Two functions in example.com/shop call `example.com/ledger/journal.Post`: | - `example.com/shop/orders.Plac...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   2.1s  $0.00035  Which functions call Save from example.com/shop/internal/db?
         got  Two functions call `Save` from `example.com/shop/internal/db`: | - `example.com/shop/orders.Place` | - `exa...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.3s  $0.00025  Which packages make up the module example.com/ledger?
         got  The module example.com/ledger has three packages: | - example.com/ledger/audit | - example.com/ledger/journ...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   1.5s  $0.00026  Which packages import example.com/shop/catalog?
         got  Only one package imports `example.com/shop/catalog`: **`example.com/shop/orders`**.
         want example.com/shop/orders
  PASS   1.6s  $0.00027  Which packages in example.com/shop import example.com/shop/orders?
         got  Two packages in example.com/shop import example.com/shop/orders: | - example.com/shop/checkout | - example....
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  FAIL   1.6s  $0.00029  What is the qualified name of the function Format in example.com/shop?
         got  The qualified name is **`example.com/shop/receipt.Format`**, declared at `example.com/shop/receipt/receipt....
         want example.com/shop/receipt.Format
         Rows: 1 of 1 rows, extra example.com/ledger/money.Format
  FAIL   6.1s  $0.00106  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  Five packages in example.com/shop import nothing from example.com/ledger in non-test files: | - **api**: im...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
         Rows: 1 of 4 rows, missing example.com/shop/api, example.com/shop/cmd/shopd, example.com/shop/internal/db, ...
  FAIL   9.5s  $0.00166  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  Four functions in example.com/shop reach `journal.Post` only through other functions: | - **`checkout.Run`*...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 0 of 4 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   5.7s  $0.00106  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  Eight functions reach `db.Save`, either directly or through other callers: | **Direct callers of `db.Save`:...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 2 of 5 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   7.8s  $0.00138  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  The shop/api package depends on two ledger packages, `example.com/ledger/journal` and `example.com/ledger/m...
         want example.com/ledger/journal, example.com/ledger/money
         Rows: 2 of 2 rows, extra example.com/ledger/audit, example.com/shop/checkout

+ stuck policy passed 10 of 15, +0 on the step before.
This step cost $0.00844, and the walk has spent $0.02503.
The next step runs the same 15 cases, so expect about $0.00844 more.
```

### 4. + answer check

A turn-end hook refuses an answer unless every line is a row the index knows, and sends the model back for rows alone.

**Inputs:**

- `watch` = `all`

```
  + 1 turn-end hook(s), which can send an answer back

Running 15 cases:
  PASS   2.4s  $0.00044  What is the qualified name of the function Format in example.com/ledger?
         got  example.com/ledger/money.Format
         want example.com/ledger/money.Format
  PASS   3.0s  $0.00046  What is the qualified name of the type Amount?
         got  example.com/ledger/money.Amount
         want example.com/ledger/money.Amount
  PASS  15.8s  $0.00203  Which packages does example.com/shop/orders import?
         got  errors | example.com/ledger/journal | example.com/ledger/money | example.com/shop/catalog | example.com/sho...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.7s  $0.00050  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  example.com/shop/catalog | example.com/shop/orders | example.com/shop/receipt
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   2.6s  $0.00048  Which packages in example.com/shop import example.com/ledger/journal?
         got  example.com/shop/orders | example.com/shop/payments | example.com/shop/refunds
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   2.6s  $0.00048  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   3.8s  $0.00063  Which functions call Save from example.com/shop/internal/db?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   2.2s  $0.00039  Which packages make up the module example.com/ledger?
         got  example.com/ledger/audit | example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   2.2s  $0.00041  Which packages import example.com/shop/catalog?
         got  example.com/shop/orders
         want example.com/shop/orders
  PASS   2.6s  $0.00044  Which packages in example.com/shop import example.com/shop/orders?
         got  example.com/shop/checkout | example.com/shop/cmd/shopd
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  PASS   2.2s  $0.00042  What is the qualified name of the function Format in example.com/shop?
         got  example.com/shop/receipt.Format
         want example.com/shop/receipt.Format
  PASS   8.5s  $0.00155  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  example.com/shop/api | example.com/shop/checkout | example.com/shop/cmd/shopd | example.com/shop/internal/db
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
  PASS  16.2s  $0.00257  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  example.com/shop/checkout.Run | example.com/shop/api.Handle | example.com/shop/cmd/shopd.main | example.com...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
  PASS   7.1s  $0.00140  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge | example.com/shop/checkout.Run | example....
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
  PASS   8.3s  $0.00160  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/journal, example.com/ledger/money

+ answer check passed 15 of 15, +5 on the step before.
This step cost $0.01380, and the walk has spent $0.03883.
```

### 5. The table

```
codeq: 15 cases
step            pass   change  usd/correct  sec/correct  
tools only      10/15          $0.00079     4.78         
+ instructions  10/15  +0      $0.00087     5.02         
+ stuck policy  10/15  +0      $0.00084     4.79         
+ answer check  15/15  +5      $0.00092     5.47         
```

