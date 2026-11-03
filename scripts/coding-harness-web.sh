#!/usr/bin/env bash
# make coding-harness-web: agentweb with chat at / and the coding agent at
# /code/ (#178), both from examples/coding-harness's config. Each coding session
# works in a fresh copy of the fixture, unless CH_WORKSPACE names a directory
# for every session to share.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EX="$ROOT/examples/coding-harness"
ADDR="${CH_ADDR:-localhost:8091}"

if [ -n "${CH_WORKSPACE:-}" ]; then
	WS_FLAG=(--coding-workspace "$(cd "$CH_WORKSPACE" && pwd)")
else
	WS_FLAG=(--coding-fixture "$EX/testdata/fixture")
fi

echo "chat on http://$ADDR/ and the coding agent on http://$ADDR/code/"
cd "$ROOT/surfaces/web"
exec go run ./cmd/agentweb --addr "$ADDR" \
	--config "$EX/config.json" --active "${CH_ACTIVE:-openai}" ${CH_MODEL:+--model "$CH_MODEL"} \
	--coding "$EX" "${WS_FLAG[@]}" --coding-active "${CH_ACTIVE:-openai}" ${CH_MODEL:+--coding-model "$CH_MODEL"} \
	${CH_INSTRUCTIONS:+--coding-instructions "$CH_INSTRUCTIONS"}
