# codefix on haiku

A walk through codefix's ladder, one step at a time: what each step adds to the agent, what it passes on 5 cases, and what it costs.

## Walkthrough

### The job

codefix fixes bugs in a small Go module, example.com/shopkit: money, cart, discount and report packages, each with tests.
Each case copies the module, brings in one bug and hands the agent a bug report. It passes when go test ./... passes and no _test.go file changed.
The bugs: an off-by-one, a comparison that leaves out its threshold, a missing nil check that panics, a bug one package away from where it shows, and a report with two bugs in two files.

The steps:
1. read + edit
2. + run tests
3. + repo map
4. + plan slot
5. + test gate

### 1. read + edit

ext/files: read_file, edit_file and friends, rooted at the case's copy. The model can't run anything.

**Inputs:**

- `action` = `run`
- `case` = `1`

```
  + tools: read_file, edit_file, apply_patch, write_file, delete_file, list_files, search_files

Running 5 cases:
  PASS   7.1s  $0.00306  Cart totals come out low. money.Sum seems to miss an item.
         got  Fixed `money.Sum` in `money/money.go`: its loop started at index 1, so the first price was dropped from eve...
         want go test ./... passes, tests unchanged
  PASS   7.4s  $0.00349  An order of exactly $10.00 gets no discount, and it should.
         got  I changed the threshold check in `shopkit/discount/discount.go` from `total > Threshold` to `total >= Thres...
         want go test ./... passes, tests unchanged
  PASS   5.7s  $0.00228  A test in the cart package panics.
         got  I added a nil check to `Cart.Total` in `shopkit/cart/cart.go`, so a nil cart returns 0 instead of dereferen...
         want go test ./... passes, tests unchanged
  PASS   7.1s  $0.00390  The report's summary line shows the wrong priciest item.
         got  Fixed `money.Max` in `money/money.go`: it compared `x < best`, so it returned the smallest price instead of...
         want go test ./... passes, tests unchanged
  PASS  10.3s  $0.00397  Two things are off in the summary line: discounts round down instead of to the nearest cent, and cents under ten lose their leading zero.
         got  I changed two things, but I couldn't run `go test ./...` because I have no shell here. I checked the arithm...
         want go test ./... passes, tests unchanged

read + edit passed 5 of 5.
This step cost $0.01669, and the walk has spent $0.01669.
The next step runs the same 5 cases, so expect about $0.01669 more.
```

### 2. + run tests

ext/exec offers run_tests, which runs go test ./... and shows the output: which test failed, and where a panic happened.

**Inputs:**

- `action` = `run`
- `case` = `1`

```
  + tools: read_file, edit_file, apply_patch, write_file, delete_file, list_files, search_files, run_tests

Running 5 cases:
  PASS   3.7s  $0.00155  Cart totals come out low. money.Sum seems to miss an item.
         got  Fixed `money.Sum` in `money/money.go`: its loop started at index 1, so the first amount was dropped; it now...
         want go test ./... passes, tests unchanged
  PASS   5.0s  $0.00200  An order of exactly $10.00 gets no discount, and it should.
         got  Changed `discount/discount.go` so `Apply` uses `total >= Threshold` instead of `total > Threshold`, so an o...
         want go test ./... passes, tests unchanged
  PASS   4.5s  $0.00222  A test in the cart package panics.
         got  Added a nil-receiver guard to `Cart.Total` in `cart/cart.go` so it returns 0 for a nil cart instead of dere...
         want go test ./... passes, tests unchanged
  PASS   5.6s  $0.00266  The report's summary line shows the wrong priciest item.
         got  Fixed `money.Max` in `money/money.go`, which compared with `<` instead of `>`, so it never picked the large...
         want go test ./... passes, tests unchanged
  PASS   7.3s  $0.00278  Two things are off in the summary line: discounts round down instead of to the nearest cent, and cents under ten lose their leading zero.
         got  Fixed the discount rounding in `discount/discount.go` (`(total*pct+50)/100` rounds to the nearest cent inst...
         want go test ./... passes, tests unchanged

+ run tests passed 5 of 5, +0 on the step before.
This step cost $0.01121, and the walk has spent $0.02790.
The next step runs the same 5 cases, so expect about $0.01121 more.
```

### 3. + repo map

A retrieval stage puts a map of the module before the model's first call: each file, what it declares, and what it calls in other packages.

**Inputs:**

- `action` = `run`
- `case` = `1`

```
  + 1 step stage(s), run before each model call

Running 5 cases:
  PASS   2.9s  $0.00114  Cart totals come out low. money.Sum seems to miss an item.
         got  Fixed `money.Sum` in `money/money.go` by starting its loop at index 0 instead of 1, so the first item is no...
         want go test ./... passes, tests unchanged
  PASS   3.5s  $0.00129  An order of exactly $10.00 gets no discount, and it should.
         got  Changed the threshold check in `discount/discount.go` from `total > Threshold` to `total >= Threshold`, so ...
         want go test ./... passes, tests unchanged
  PASS   4.0s  $0.00141  A test in the cart package panics.
         got  Added a nil-receiver guard to `(*Cart).Total` in `cart/cart.go` so a nil cart returns 0, and `go test ./......
         want go test ./... passes, tests unchanged
  PASS   4.5s  $0.00139  The report's summary line shows the wrong priciest item.
         got  Fixed `money.Max` in `money/money.go`, which was returning the smallest price instead of the largest (`x < ...
         want go test ./... passes, tests unchanged
  PASS   6.1s  $0.00203  Two things are off in the summary line: discounts round down instead of to the nearest cent, and cents under ten lose their leading zero.
         got  Changed `discount.Apply` to round the discount to the nearest cent, and `report.dollars` to zero-pad cents ...
         want go test ./... passes, tests unchanged

+ repo map passed 5 of 5, +0 on the step before.
This step cost $0.00725, and the walk has spent $0.03515.
The next step runs the same 5 cases, so expect about $0.00725 more.
```

### 4. + plan slot

A plan slot the model writes with write_slot. Its text is shown on every call, so a list of changes outlives the steps that made them.

**Inputs:**

- `action` = `run`
- `case` = `1`

```
  + tools: read_file, edit_file, apply_patch, write_file, delete_file, list_files, search_files, run_tests, write_slot
  + 1 step stage(s), run before each model call

Running 5 cases:
  PASS   3.3s  $0.00122  Cart totals come out low. money.Sum seems to miss an item.
         got  Fixed `money.Sum` in `money/money.go`, whose loop started at index 1 and so skipped the first price; it now...
         want go test ./... passes, tests unchanged
  PASS   3.3s  $0.00142  An order of exactly $10.00 gets no discount, and it should.
         got  Changed the threshold check in `discount/discount.go` from `total > Threshold` to `total >= Threshold`, so ...
         want go test ./... passes, tests unchanged
  PASS   3.9s  $0.00146  A test in the cart package panics.
         got  Added a nil-receiver check to `(*Cart).Total` in `cart/cart.go` so it returns 0 for a nil cart instead of d...
         want go test ./... passes, tests unchanged
  PASS   4.7s  $0.00149  The report's summary line shows the wrong priciest item.
         got  Fixed `money.Max` in `money/money.go` so it returns the largest amount instead of the smallest (it was usin...
         want go test ./... passes, tests unchanged
  PASS   5.7s  $0.00200  Two things are off in the summary line: discounts round down instead of to the nearest cent, and cents under ten lose their leading zero.
         got  Fixed the discount to round to the nearest cent (`discount/discount.go`) and the cents to print with two di...
         want go test ./... passes, tests unchanged

+ plan slot passed 5 of 5, +0 on the step before.
This step cost $0.00758, and the walk has spent $0.04273.
The next step runs the same 5 cases, so expect about $0.00758 more.
```

### 5. + test gate

A turn-end hook runs the tests when the model says it's done. While they fail, it sends the model back with the output.

**Inputs:**

- `action` = `run`
- `case` = `1`

```
  + 1 turn-end hook(s), which can send an answer back

Running 5 cases:
  PASS   3.3s  $0.00121  Cart totals come out low. money.Sum seems to miss an item.
         got  Fixed `money.Sum` in `money/money.go` so its loop starts at index 0 instead of 1, which had been skipping t...
         want go test ./... passes, tests unchanged
  PASS   3.0s  $0.00136  An order of exactly $10.00 gets no discount, and it should.
         got  Changed `discount.Apply` to use `total >= Threshold` instead of `>`, so a $10.00 order (1000 cents) now get...
         want go test ./... passes, tests unchanged
  PASS   4.1s  $0.00148  A test in the cart package panics.
         got  Fixed `(*Cart).Total` in `cart/cart.go` to return 0 for a nil cart, matching the doc comment and `Priciest`...
         want go test ./... passes, tests unchanged
  PASS   5.3s  $0.00156  The report's summary line shows the wrong priciest item.
         got  Fixed `money.Max` in `money/money.go`: it was keeping the smallest amount (`x < best`), so it now returns t...
         want go test ./... passes, tests unchanged
  PASS   5.7s  $0.00208  Two things are off in the summary line: discounts round down instead of to the nearest cent, and cents under ten lose their leading zero.
         got  Fixed the discount to round to the nearest cent (`discount/discount.go`) and the cents formatting to zero-p...
         want go test ./... passes, tests unchanged

+ test gate passed 5 of 5, +0 on the step before.
This step cost $0.00768, and the walk has spent $0.05041.
```

### 6. The table

```
codefix: 5 cases
step         pass  change  usd/correct  sec/correct  
read + edit  5/5           $0.0033      7.58         
+ run tests  5/5   +0      $0.0022      5.30         
+ repo map   5/5   +0      $0.0014      4.23         
+ plan slot  5/5   +0      $0.0015      4.25         
+ test gate  5/5   +0      $0.0015      4.32         
```

