#!/usr/bin/env bash
# make coding-harness-parity: the same task, fixture and model through codex
# exec and through chakra's coding harness, side by side. Each side works on its
# own copy of examples/coding-harness/testdata/fixture.
#
# Both sides bill OPENAI_API_KEY. codex gets it as CODEX_API_KEY, which applies
# to exec only and leaves a ChatGPT login alone; --ignore-user-config keeps
# ~/.codex/config.toml out of the comparison. Codex speaks the Responses API and
# chakra chat completions until #157, so the wire differs even with the model
# fixed. PARITY_INSTRUCTIONS=codex makes chakra load AGENTS.md the way codex
# does, for a like-for-like run; the default is workspace.json's choice.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EX="$ROOT/examples/coding-harness"
MODEL="${PARITY_MODEL:-gpt-5.5}"
PROMPT="The tests in this repository fail. Make them pass."
OUT="${PARITY_OUT:-$(mktemp -d)/parity}"
mkdir -p "$OUT/codex" "$OUT/chakra"

: "${OPENAI_API_KEY:?coding-harness-parity needs OPENAI_API_KEY}"
command -v codex >/dev/null || { echo "coding-harness-parity needs the codex CLI on PATH"; exit 1; }

echo "==> codex exec ($MODEL)"
cp -R "$EX/testdata/fixture" "$OUT/codex/repo"
start=$(date +%s)
(
	cd "$OUT/codex/repo"
	CODEX_API_KEY="$OPENAI_API_KEY" codex exec --json --ephemeral --ignore-user-config \
		--skip-git-repo-check -s workspace-write -m "$MODEL" \
		--output-schema "$EX/result.schema.json" -o "$OUT/codex/last.json" \
		"$PROMPT" </dev/null >"$OUT/codex/events.jsonl" 2>"$OUT/codex/stderr.txt"
) || echo "codex exec exited $? (see $OUT/codex/stderr.txt)"
echo $(( $(date +%s) - start )) >"$OUT/codex/wall"

echo "==> chakra coding-harness ($MODEL)"
(
	cd "$EX"
	go run . --live --active openai --model "$MODEL" ${PARITY_INSTRUCTIONS:+--instructions "$PARITY_INSTRUCTIONS"} \
		--jsonl "$OUT/chakra/events.jsonl" --metrics "$OUT/chakra/metrics.json"
) >"$OUT/chakra/table.txt" 2>&1 || echo "coding-harness exited $? (see $OUT/chakra/table.txt)"

python3 "$ROOT/scripts/coding_harness_parity.py" "$EX/testdata/fixture" "$OUT"
echo
echo "runs kept in $OUT"
