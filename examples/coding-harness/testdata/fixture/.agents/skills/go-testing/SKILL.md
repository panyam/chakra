---
name: go-testing
description: How to run and read this repository's Go tests. Load it before running or fixing tests.
---
# Go tests in this repository

- Run `go test ./...` from the repository root. It needs no network.
- A failure prints the test name and the expected and actual values; fix the code
  under test, never the test.
- Run the tests again after a change and report whether they passed.
