#!/usr/bin/env bash
# make coding-harness-web: the coding agent by hand, in agentweb's browser UI.
# It is plain agentweb over examples/coding-harness's config and workspace
# files, on a fresh copy of the fixture unless CH_WORKSPACE names a directory.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EX="$ROOT/examples/coding-harness"
ADDR="${CH_ADDR:-localhost:8091}"

if [ -n "${CH_WORKSPACE:-}" ]; then
	WS="$(cd "$CH_WORKSPACE" && pwd)"
else
	WS="$(mktemp -d)/repo"
	cp -R "$EX/testdata/fixture" "$WS"
	# workspace.json turns on ext/git, which needs a repository.
	git -C "$WS" init -q -b main && git -C "$WS" add -A && git -C "$WS" -c user.name=fixture -c user.email=fixture@example.com commit -qm fixture
fi
# Snapshots live outside the workspace, so the agent can't read or edit them.
CKPT="$(mktemp -d)"

echo "coding agent on http://$ADDR/ working in $WS"
cd "$ROOT/surfaces/web"
exec go run ./cmd/agentweb --addr "$ADDR" \
	--config "$EX/config.json" --active "${CH_ACTIVE:-openai}" ${CH_MODEL:+--model "$CH_MODEL"} \
	--workspace "$WS" --workspace-config "$EX/workspace.json" --checkpoint-store "$CKPT" \
	${CH_INSTRUCTIONS:+--instructions "$CH_INSTRUCTIONS"}
