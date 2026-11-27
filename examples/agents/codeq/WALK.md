# codeq on haiku

A walk through codeq's ladder, one step at a time: what each step adds to the agent, what it passes on 20 cases, and what it costs.

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
5. + routing

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

Running 20 cases:
  FAIL   1.9s  $0.00028  What is the qualified name of the function Format in example.com/ledger?
         got  The qualified name is **`example.com/ledger/money.Format`**, declared in `example.com/ledger/money/money.go...
         want example.com/ledger/money.Format
         Rows: 1 of 1 rows, extra example.com/shop/receipt.Format
  PASS   1.5s  $0.00023  What is the qualified name of the type Amount?
         got  The type Amount is declared at `example.com/ledger/money.Amount`, in `example.com/ledger/money/money.go` at...
         want example.com/ledger/money.Amount
  PASS   1.5s  $0.00027  Which packages does example.com/shop/orders import?
         got  `example.com/shop/orders` imports these packages: | - `errors` | - `example.com/ledger/journal` | - `exampl...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.2s  $0.00034  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  Three packages in example.com/shop import example.com/ledger/money in non-test files: | - example.com/shop/...
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   1.8s  $0.00032  Which packages in example.com/shop import example.com/ledger/journal?
         got  Three packages in example.com/shop import example.com/ledger/journal: | - example.com/shop/orders | - examp...
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   2.0s  $0.00029  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  Two functions in example.com/shop call `example.com/ledger/journal.Post`: | - `example.com/shop/orders.Plac...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.7s  $0.00027  Which functions call Save from example.com/shop/internal/db?
         got  Two functions call `Save` from `example.com/shop/internal/db`: | 1. `example.com/shop/orders.Place` | 2. `e...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.2s  $0.00023  Which packages make up the module example.com/ledger?
         got  The module example.com/ledger contains three packages: | - example.com/ledger/audit | - example.com/ledger/...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   1.6s  $0.00025  Which packages import example.com/shop/catalog?
         got  Only one package imports `example.com/shop/catalog`: **`example.com/shop/orders`**. I searched both modules...
         want example.com/shop/orders
  PASS   1.7s  $0.00028  Which packages in example.com/shop import example.com/shop/orders?
         got  Two packages in `example.com/shop` import `example.com/shop/orders`: | - `example.com/shop/checkout` | - `e...
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  FAIL   2.1s  $0.00030  What is the qualified name of the function Format in example.com/shop?
         got  The qualified name is **`example.com/shop/receipt.Format`**, declared in `example.com/shop/receipt/receipt....
         want example.com/shop/receipt.Format
         Rows: 1 of 1 rows, extra example.com/ledger/money.Format
  FAIL   7.8s  $0.00136  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  Four packages in example.com/shop import nothing from example.com/ledger in their non-test files: | - **exa...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
         Rows: 4 of 4 rows, extra example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money, ex...
  FAIL   9.9s  $0.00161  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  Four functions in example.com/shop reach `journal.Post` only through intermediate calls: | - **`checkout.Ru...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 0 of 4 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   5.8s  $0.00108  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  Six functions call `db.Save`, directly or indirectly: | **Direct callers of `db.Save`:** | - `example.com/s...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 2 of 5 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   7.5s  $0.00121  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  `example.com/shop/api` depends on two packages in `example.com/ledger`, both transitively: | - **`example.c...
         want example.com/ledger/journal, example.com/ledger/money
         Rows: 2 of 2 rows, extra example.com/shop/orders, example.com/shop/payments, example.com/shop/receipt
  FAIL   5.8s  $0.00093  Which functions in example.com/shop reach example.com/shop/receipt.Format only through other functions, without calling it directly?
         got  Two functions in `example.com/shop` reach `receipt.Format` only indirectly: `checkout.Run` and `api.Handle`...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run
         Rows: 0 of 2 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run
  FAIL   6.6s  $0.00111  Which functions in example.com/shop call example.com/ledger/money.Add, directly or through other functions?
         got  Four functions in example.com/shop reach `money.Add`: | - **`orders.Place`** calls `money.Add` directly. | ...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 0 of 4 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  PASS   4.4s  $0.00074  Which packages in example.com/ledger does example.com/shop/refunds depend on, directly or through other packages?
         got  `example.com/shop/refunds` depends on three packages in `example.com/ledger`: | - **`example.com/ledger/aud...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  FAIL   6.6s  $0.00107  Which packages in example.com/shop does example.com/shop/cmd/shopd depend on, directly or through other packages?
         got  The `example.com/shop` packages that `cmd/shopd` depends on, directly or transitively, are: | - **`example....
         want example.com/shop/catalog, example.com/shop/internal/db, example.com/shop/orders, example.com/shop/payments
         Rows: 4 of 4 rows, extra example.com/ledger/journal, example.com/ledger/money
  FAIL   8.7s  $0.00134  Which packages in example.com/shop depend on example.com/ledger/money only through other packages, without importing it directly, counting only non-test files?
         got  Five packages in example.com/shop reach `example.com/ledger/money` only through other packages: | - **api**...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/payments, exa...
         Rows: 0 of 5 rows, missing example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, exa...

tools only passed 10 of 20.
This step cost $0.01351, and the walk has spent $0.01351.
The next step runs the same 20 cases, so expect about $0.01351 more.
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

Running 20 cases:
  PASS   1.3s  $0.00025  What is the qualified name of the function Format in example.com/ledger?
         got  The qualified name of `Format` in example.com/ledger is **example.com/ledger/money.Format**, declared at ex...
         want example.com/ledger/money.Format
  PASS   1.3s  $0.00025  What is the qualified name of the type Amount?
         got  The qualified name of the type Amount is `example.com/ledger/money.Amount`. It is declared at `example.com/...
         want example.com/ledger/money.Amount
  PASS   1.7s  $0.00030  Which packages does example.com/shop/orders import?
         got  `example.com/shop/orders` imports these packages: | - `errors` | - `example.com/ledger/journal` | - `exampl...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.0s  $0.00035  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  Three packages in example.com/shop import example.com/ledger/money in non-test files: | - example.com/shop/...
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   1.9s  $0.00032  Which packages in example.com/shop import example.com/ledger/journal?
         got  Three packages in example.com/shop import example.com/ledger/journal: | - example.com/shop/orders | - examp...
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   1.9s  $0.00033  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  Two functions in example.com/shop call `example.com/ledger/journal.Post`: | - `example.com/shop/orders.Plac...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   2.1s  $0.00033  Which functions call Save from example.com/shop/internal/db?
         got  Two functions call `Save` from `example.com/shop/internal/db`: | - `example.com/shop/orders.Place` | - `exa...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.2s  $0.00025  Which packages make up the module example.com/ledger?
         got  The module example.com/ledger has three packages: | - example.com/ledger/audit | - example.com/ledger/journ...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   1.4s  $0.00027  Which packages import example.com/shop/catalog?
         got  Only one package imports `example.com/shop/catalog`: **`example.com/shop/orders`**.
         want example.com/shop/orders
  PASS   1.7s  $0.00030  Which packages in example.com/shop import example.com/shop/orders?
         got  Two packages in example.com/shop import example.com/shop/orders: | - example.com/shop/checkout | - example....
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  PASS   1.3s  $0.00025  What is the qualified name of the function Format in example.com/shop?
         got  The function Format in example.com/shop is **example.com/shop/receipt.Format**, declared at receipt/receipt...
         want example.com/shop/receipt.Format
  FAIL   6.6s  $0.00123  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  Four packages in example.com/shop import nothing from example.com/ledger in non-test files: | - **example.c...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
         Rows: 4 of 4 rows, extra example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money, ex...
  FAIL  10.3s  $0.00172  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  Four functions in example.com/shop reach `journal.Post` only through other functions: | - **`example.com/sh...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 4 of 4 rows, extra example.com/ledger/audit.Replay
  FAIL   6.0s  $0.00112  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  Eight functions call `db.Save`, directly or indirectly: | **Direct callers:** | - `example.com/shop/orders....
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 2 of 5 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   9.8s  $0.00171  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  Yes, `example.com/shop/api` depends on two ledger packages, `example.com/ledger/journal` and `example.com/l...
         want example.com/ledger/journal, example.com/ledger/money
         Rows: 2 of 2 rows, extra example.com/ledger/audit
  PASS   6.5s  $0.00116  Which functions in example.com/shop reach example.com/shop/receipt.Format only through other functions, without calling it directly?
         got  Two functions in example.com/shop reach `receipt.Format` only indirectly: | - **`example.com/shop/checkout....
         want example.com/shop/api.Handle, example.com/shop/checkout.Run
  FAIL   6.4s  $0.00116  Which functions in example.com/shop call example.com/ledger/money.Add, directly or through other functions?
         got  Four functions in example.com/shop reach `money.Add`, all through `orders.Place`: | - **`example.com/shop/o...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 4 of 4 rows, extra example.com/ledger/journal.Balance
  PASS   3.8s  $0.00071  Which packages in example.com/ledger does example.com/shop/refunds depend on, directly or through other packages?
         got  `example.com/shop/refunds` depends on three packages in `example.com/ledger`: | - **`example.com/ledger/aud...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   5.2s  $0.00092  Which packages in example.com/shop does example.com/shop/cmd/shopd depend on, directly or through other packages?
         got  `example.com/shop/cmd/shopd` depends on four packages in `example.com/shop`: | - `example.com/shop/orders` ...
         want example.com/shop/catalog, example.com/shop/internal/db, example.com/shop/orders, example.com/shop/payments
  FAIL   8.3s  $0.00133  Which packages in example.com/shop depend on example.com/ledger/money only through other packages, without importing it directly, counting only non-test files?
         got  Five packages in example.com/shop reach `example.com/ledger/money` only through other packages: | - **api**...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/payments, exa...
         Rows: 1 of 5 rows, missing example.com/shop/api, example.com/shop/cmd/shopd, example.com/shop/payments, exa...

+ instructions passed 14 of 20, +4 on the step before. That's one run of a live model, so the change may be noise.
This step cost $0.01425, and the walk has spent $0.02776.
The next step runs the same 20 cases, so expect about $0.01425 more.
```

### 3. + stuck policy

A stuck detector watches for a call repeated three times, and adds a note to the next model call saying so. It matters when callers refuses a bare name and the model asks again.

**Inputs:**

- `watch` = `all`

```
  + 1 step stage(s), run before each model call

Running 20 cases:
  FAIL   1.6s  $0.00028  What is the qualified name of the function Format in example.com/ledger?
         got  The qualified name is **`example.com/ledger/money.Format`**, declared at `example.com/ledger/money/money.go...
         want example.com/ledger/money.Format
         Rows: 1 of 1 rows, extra example.com/shop/receipt.Format
  PASS   1.5s  $0.00027  What is the qualified name of the type Amount?
         got  The qualified name is `example.com/ledger/money.Amount`. It is declared in `example.com/ledger/money/money....
         want example.com/ledger/money.Amount
  PASS   1.8s  $0.00031  Which packages does example.com/shop/orders import?
         got  `example.com/shop/orders` imports these packages: | - `errors` | - `example.com/ledger/journal` | - `exampl...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.1s  $0.00035  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  Three packages in example.com/shop import example.com/ledger/money in non-test files: | - example.com/shop/...
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   1.7s  $0.00030  Which packages in example.com/shop import example.com/ledger/journal?
         got  Three packages in example.com/shop import example.com/ledger/journal: | - example.com/shop/orders | - examp...
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   2.3s  $0.00037  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  Two functions in example.com/shop call `example.com/ledger/journal.Post`: | - `example.com/shop/orders.Plac...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   2.6s  $0.00038  Which functions call Save from example.com/shop/internal/db?
         got  Two functions call `Save` from `example.com/shop/internal/db`: | - `example.com/shop/orders.Place` | - `exa...
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.3s  $0.00025  Which packages make up the module example.com/ledger?
         got  The module example.com/ledger has three packages: | - example.com/ledger/audit | - example.com/ledger/journ...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   1.3s  $0.00026  Which packages import example.com/shop/catalog?
         got  Only one package imports `example.com/shop/catalog`: **example.com/shop/orders**.
         want example.com/shop/orders
  PASS   1.5s  $0.00027  Which packages in example.com/shop import example.com/shop/orders?
         got  Two packages in example.com/shop import example.com/shop/orders: | - example.com/shop/checkout | - example....
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  FAIL   1.7s  $0.00029  What is the qualified name of the function Format in example.com/shop?
         got  The qualified name is `example.com/shop/receipt.Format`, declared at `example.com/shop/receipt/receipt.go:1...
         want example.com/shop/receipt.Format
         Rows: 1 of 1 rows, extra example.com/ledger/money.Format
  PASS   6.2s  $0.00113  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  Four packages in example.com/shop import nothing from example.com/ledger in non-test files: | - `example.co...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
  FAIL   7.7s  $0.00140  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  Three functions in example.com/shop reach `journal.Post` only through other functions: | - **`checkout.Run`...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 0 of 4 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   5.6s  $0.00112  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  Five functions call `example.com/shop/internal/db.Save`, directly or indirectly: | **Direct callers:** | - ...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 2 of 5 rows, missing example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd...
  FAIL   6.7s  $0.00125  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  The ledger packages that `example.com/shop/api` depends on are **journal** and **money**. | The chain runs ...
         want example.com/ledger/journal, example.com/ledger/money
         Rows: 0 of 2 rows, missing example.com/ledger/journal, example.com/ledger/money, extra example.com/shop/che...
  PASS   6.0s  $0.00099  Which functions in example.com/shop reach example.com/shop/receipt.Format only through other functions, without calling it directly?
         got  Two functions in example.com/shop reach `receipt.Format` only indirectly: | - **`example.com/shop/checkout....
         want example.com/shop/api.Handle, example.com/shop/checkout.Run
  FAIL   7.0s  $0.00129  Which functions in example.com/shop call example.com/ledger/money.Add, directly or through other functions?
         got  Three functions in example.com/shop call `money.Add`, all through `orders.Place`: | - **`example.com/shop/o...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 4 of 4 rows, extra example.com/ledger/journal.Balance
  PASS   3.8s  $0.00072  Which packages in example.com/ledger does example.com/shop/refunds depend on, directly or through other packages?
         got  The `example.com/shop/refunds` package reaches three packages in `example.com/ledger`, directly or through ...
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   5.8s  $0.00102  Which packages in example.com/shop does example.com/shop/cmd/shopd depend on, directly or through other packages?
         got  Four packages in example.com/shop are reached from `cmd/shopd`: | - **`example.com/shop/orders`**: a direct...
         want example.com/shop/catalog, example.com/shop/internal/db, example.com/shop/orders, example.com/shop/payments
  FAIL   9.8s  $0.00144  Which packages in example.com/shop depend on example.com/ledger/money only through other packages, without importing it directly, counting only non-test files?
         got  Five packages in example.com/shop depend on `ledger/money` only indirectly, through other packages: | - **a...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/payments, exa...
         Rows: 0 of 5 rows, missing example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, exa...

+ stuck policy passed 13 of 20, -1 on the step before. That's one run of a live model, so the change may be noise.
This step cost $0.01369, and the walk has spent $0.04145.
The next step runs the same 20 cases, so expect about $0.01369 more.
```

### 4. + answer check

A turn-end hook refuses an answer unless every line is a row the index knows, and sends the model back for rows alone.

**Inputs:**

- `watch` = `all`

```
  + 1 turn-end hook(s), which can send an answer back

Running 20 cases:
  PASS   2.2s  $0.00041  What is the qualified name of the function Format in example.com/ledger?
         got  example.com/ledger/money.Format
         want example.com/ledger/money.Format
  PASS   2.1s  $0.00040  What is the qualified name of the type Amount?
         got  example.com/ledger/money.Amount
         want example.com/ledger/money.Amount
  PASS   9.2s  $0.00169  Which packages does example.com/shop/orders import?
         got  errors | example.com/ledger/journal | example.com/ledger/money | example.com/shop/catalog | example.com/sho...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.9s  $0.00050  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  example.com/shop/catalog | example.com/shop/orders | example.com/shop/receipt
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   2.9s  $0.00049  Which packages in example.com/shop import example.com/ledger/journal?
         got  example.com/shop/orders | example.com/shop/payments | example.com/shop/refunds
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   2.4s  $0.00046  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   3.6s  $0.00065  Which functions call Save from example.com/shop/internal/db?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   1.9s  $0.00040  Which packages make up the module example.com/ledger?
         got  example.com/ledger/audit | example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   2.1s  $0.00040  Which packages import example.com/shop/catalog?
         got  example.com/shop/orders
         want example.com/shop/orders
  PASS   2.5s  $0.00045  Which packages in example.com/shop import example.com/shop/orders?
         got  example.com/shop/checkout | example.com/shop/cmd/shopd
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  PASS   2.6s  $0.00046  What is the qualified name of the function Format in example.com/shop?
         got  example.com/shop/receipt.Format
         want example.com/shop/receipt.Format
  PASS   9.4s  $0.00168  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  example.com/shop/api | example.com/shop/checkout | example.com/shop/cmd/shopd | example.com/shop/internal/db
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
  FAIL   9.4s  $0.00170  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  example.com/shop/checkout.Run | example.com/shop/api.Handle | example.com/shop/cmd/shopd.main
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
         Rows: 3 of 4 rows, missing example.com/shop/refunds.Issue
  PASS   7.2s  $0.00146  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge | example.com/shop/checkout.Run | example....
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
  PASS  11.2s  $0.00209  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/journal, example.com/ledger/money
  PASS   7.0s  $0.00124  Which functions in example.com/shop reach example.com/shop/receipt.Format only through other functions, without calling it directly?
         got  example.com/shop/checkout.Run | example.com/shop/api.Handle
         want example.com/shop/api.Handle, example.com/shop/checkout.Run
  PASS   7.7s  $0.00145  Which functions in example.com/shop call example.com/ledger/money.Add, directly or through other functions?
         got  example.com/shop/orders.Place | example.com/shop/checkout.Run | example.com/shop/cmd/shopd.main | example.c...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
  PASS   5.0s  $0.00095  Which packages in example.com/ledger does example.com/shop/refunds depend on, directly or through other packages?
         got  example.com/ledger/audit | example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   7.5s  $0.00128  Which packages in example.com/shop does example.com/shop/cmd/shopd depend on, directly or through other packages?
         got  example.com/shop/orders | example.com/shop/payments | example.com/shop/catalog | example.com/shop/internal/db
         want example.com/shop/catalog, example.com/shop/internal/db, example.com/shop/orders, example.com/shop/payments
  PASS  11.8s  $0.00197  Which packages in example.com/shop depend on example.com/ledger/money only through other packages, without importing it directly, counting only non-test files?
         got  example.com/shop/api | example.com/shop/checkout | example.com/shop/cmd/shopd | example.com/shop/payments |...
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/payments, exa...

+ answer check passed 19 of 20, +6 on the step before. That's one run of a live model, so the change may be noise.
This step cost $0.02016, and the walk has spent $0.06160.
The next step runs the same 20 cases, so expect about $0.02016 more.
```

### 5. + routing

**Inputs:**

- `watch` = `all`

```
  + 1 step stage(s), run before each model call

Running 20 cases:
  PASS   2.7s  $0.00048  What is the qualified name of the function Format in example.com/ledger?
         got  example.com/ledger/money.Format
         want example.com/ledger/money.Format
  PASS   3.3s  $0.00054  What is the qualified name of the type Amount?
         got  example.com/ledger/money.Amount
         want example.com/ledger/money.Amount
  PASS  13.5s  $0.00208  Which packages does example.com/shop/orders import?
         got  errors | example.com/ledger/journal | example.com/ledger/money | example.com/shop/catalog | example.com/sho...
         want example.com/ledger/journal, example.com/ledger/money, example.com/shop/catalog, example.com/shop/internal/db
  PASS   2.8s  $0.00052  Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?
         got  example.com/shop/catalog | example.com/shop/orders | example.com/shop/receipt
         want example.com/shop/catalog, example.com/shop/orders, example.com/shop/receipt
  PASS   3.0s  $0.00053  Which packages in example.com/shop import example.com/ledger/journal?
         got  example.com/shop/orders | example.com/shop/payments | example.com/shop/refunds
         want example.com/shop/orders, example.com/shop/payments, example.com/shop/refunds
  PASS   3.2s  $0.00054  Which functions in example.com/shop call example.com/ledger/journal.Post?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   4.8s  $0.00078  Which functions call Save from example.com/shop/internal/db?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge
         want example.com/shop/orders.Place, example.com/shop/payments.Charge
  PASS   2.5s  $0.00046  Which packages make up the module example.com/ledger?
         got  example.com/ledger/audit | example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS   2.6s  $0.00046  Which packages import example.com/shop/catalog?
         got  example.com/shop/orders
         want example.com/shop/orders
  PASS   3.1s  $0.00053  Which packages in example.com/shop import example.com/shop/orders?
         got  example.com/shop/checkout | example.com/shop/cmd/shopd
         want example.com/shop/checkout, example.com/shop/cmd/shopd
  PASS   2.9s  $0.00052  What is the qualified name of the function Format in example.com/shop?
         got  example.com/shop/receipt.Format
         want example.com/shop/receipt.Format
  PASS   9.7s  $0.00166  Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?
         got  example.com/shop/api | example.com/shop/checkout | example.com/shop/cmd/shopd | example.com/shop/internal/db
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/internal/db
  PASS  18.3s  $0.00292  Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?
         got  example.com/shop/refunds.Issue | example.com/shop/checkout.Run | example.com/shop/cmd/shopd.main | example....
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
  PASS  11.8s  $0.00182  Which functions call example.com/shop/internal/db.Save, directly or through other functions?
         got  example.com/shop/orders.Place | example.com/shop/payments.Charge | example.com/shop/checkout.Run | example....
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
  PASS  18.8s  $0.00245  Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?
         got  example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/journal, example.com/ledger/money
  PASS  13.2s  $0.00181  Which functions in example.com/shop reach example.com/shop/receipt.Format only through other functions, without calling it directly?
         got  example.com/shop/checkout.Run | example.com/shop/api.Handle
         want example.com/shop/api.Handle, example.com/shop/checkout.Run
  PASS  15.1s  $0.00230  Which functions in example.com/shop call example.com/ledger/money.Add, directly or through other functions?
         got  example.com/shop/orders.Place | example.com/shop/checkout.Run | example.com/shop/api.Handle | example.com/s...
         want example.com/shop/api.Handle, example.com/shop/checkout.Run, example.com/shop/cmd/shopd.main, example.com/sh...
  PASS  12.1s  $0.00159  Which packages in example.com/ledger does example.com/shop/refunds depend on, directly or through other packages?
         got  example.com/ledger/audit | example.com/ledger/journal | example.com/ledger/money
         want example.com/ledger/audit, example.com/ledger/journal, example.com/ledger/money
  PASS  15.8s  $0.00228  Which packages in example.com/shop does example.com/shop/cmd/shopd depend on, directly or through other packages?
         got  example.com/shop/orders | example.com/shop/payments | example.com/shop/catalog | example.com/shop/internal/db
         want example.com/shop/catalog, example.com/shop/internal/db, example.com/shop/orders, example.com/shop/payments
  FAIL  29.2s  $0.00388  Which packages in example.com/shop depend on example.com/ledger/money only through other packages, without importing it directly, counting only non-test files?
         got  example.com/shop/refunds | example.com/shop/checkout | example.com/shop/cmd/shopd | example.com/shop/api
         want example.com/shop/api, example.com/shop/checkout, example.com/shop/cmd/shopd, example.com/shop/payments, exa...
         Rows: 4 of 5 rows, missing example.com/shop/payments

+ routing passed 19 of 20, +0 on the step before. That's one run of a live model, so the change may be noise.
This step cost $0.02816, and the walk has spent $0.08976.
```

### 6. The table

```
codeq: 20 cases
step            pass   change  usd/correct  sec/correct  
tools only      10/20          $0.0014      8.24         
+ instructions  14/20  ~+4     $0.0010      5.77         
+ stuck policy  13/20  ~-1     $0.0011      6.01         
+ answer check  19/20  ~+6     $0.0011      5.82         
+ routing       19/20  ~+0     $0.0015      9.92         

~ one run of a live model shows no spread, so the change may be noise. Running each case more than once measures it.
```

