# Benchmark arms for `agentchat exec`

`agentchat.json` holds three arms that differ only in the model. `--active` picks one:

```
agentchat exec --config examples/benchmark/agentchat.json --active sonnet \
  --approve read-only-auto --json -p "..." > run.jsonl
```

- `gpt-5.5` runs on the Responses API at `reasoningEffort: medium`.
- `sonnet` sets `maxTokens`, since an `anthropic` connection without it stops each reply at
  4096 tokens.
- `qwen-14b` is whatever model `llama-server` serves on `localhost:8080`. Start it with
  `--jinja` so it parses the model's native tool calls. It has no `price`, so its cost
  frames carry `usd: null` and its tokens still count.

`--approve read-only-auto` runs only the tools whose server marks them `readOnlyHint`, and
denies the rest. A denied call still counts in the summary's `toolCalls`, and in
`toolCallsDenied`. If the server under test leaves the hint off, use `--approve allow`.

Point the `mcp` server at the server under test. A stdio server takes `command` and `args`
in place of `url`. If it's down, exec exits 5 before the first model call.

The prices are list prices in US dollars per million tokens, as of 2026-11, and match
`PRICES` in `scripts/tbench_matrix.py`. Update both together.

The frames a driver reads are in
[`surfaces/chat/README.md`](../../surfaces/chat/README.md#driving-a-benchmark-with-exec).
