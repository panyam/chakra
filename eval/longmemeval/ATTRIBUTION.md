# Attribution

The mcpkit project **hand-authored** the scenarios in this package. They adapt
the task categories from **LongMemEval**, in its spirit:

> LongMemEval: Benchmarking Chat Assistants on Long-Term Interactive Memory.
> https://github.com/xiaowu0162/LongMemEval

We borrow the *shape* of the benchmark, meaning its five skill categories:
information extraction, multi-session reasoning, knowledge updates, temporal
reasoning and abstention. We grade with our own `eval` harness, and that
harness is the differentiator. We do **not** vendor the LongMemEval dataset.
The cases in `cases.go` are original, short scenarios. They exercise the same
skills against mcpkit's working-memory tools.

`SmokeScenarios()` is deliberately a coarse regression signal, not a
published-comparable score.

## The dataset loader

`DatasetAdapter` reads the **actual** released corpus and adapts each question
into an `eval.Scenario`. The data is **not vendored**. Point
`LONGMEMEVAL_DATA_PATH` at a downloaded file, the same way the conformance
suites point at an external checkout via `MCPCONFORMANCE_*_PATH`.

The corpus is released under the **MIT license** at
`huggingface.co/datasets/xiaowu0162/longmemeval-cleaned` as
`longmemeval_s.json`, `longmemeval_m.json`, and `longmemeval_oracle.json`,
500 instances each.

```bash
export LONGMEMEVAL_DATA_PATH=/path/to/longmemeval_s.json
export LONGMEMEVAL_LIMIT=20          # a full pass is 500 x ~115k tokens
go test -tags eval_llm ./eval/longmemeval/ -run TestLive -v
```

There are **two modes**. They measure different systems, so their numbers
can't be compared with each other. The default mode puts each haystack session
into a `MemoryStore` and grades whether the agent retrieves the right one. This
repo ships that stack, and a small-context model can only compete in this mode.
`LongContext: true` seeds every session into the conversation instead.
Published baselines mostly report this mode. When a haystack exceeds the
model's window, the case fails rather than scores.

There are also two ways to grade. By default every answer is graded against
one generic rubric of ours. `OfficialJudge: true` uses LongMemEval's own judge
prompts instead, one per question type. We copied them verbatim from
`src/evaluation/evaluate_qa.py` at commit `9e0b455`, under the same MIT
license. `testdata/evaluate_qa.py` holds the upstream function, and a test
checks our copy against it. Use the official prompts when the numbers will sit
beside published ones.

A host whose memory isn't a `MemoryStore` sets `Arm`. It runs once per
question, ingests the haystack its own way, and returns the tools and
instructions the question runs with. The suite report keeps the tokens and time
of that ingestion apart from the question's, and with `Suite.Prices` set it
gives a cost per correct answer.

Numbers from either mode are ours, not the benchmark authors'. Any comparison
to published results should say which mode produced it.

Issue 974 also names two sibling benchmarks. BFCL relevance detection would
exercise the approval gate via `NotDenied`, and tau-bench user simulation would
exercise the elicitation / MRTR input loop. Both are separate follow-ups and
are not adapted here.
