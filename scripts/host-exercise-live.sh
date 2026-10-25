#!/usr/bin/env bash
# make host-exercise-live: boots the host-exercise demo server and points
# agentchat at it with a real model, for looking at what the scripted checks in
# examples/host-exercise test (make host-exercise). Each check's id is in the
# README there, with what to try by hand.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# The port is fixed because agentchat.json names it.
PORT=8790

echo "==> booting the host-exercise demo server on :$PORT"
# Built rather than go run, so the trap kills the server itself and not just
# the go command, which would leave the server holding the port.
BIN="$(mktemp -d)/host-exercise"
( cd "$ROOT/examples/host-exercise" && go build -o "$BIN" . )
"$BIN" --serve ":$PORT" &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true; rm -rf "$(dirname "$BIN")"' EXIT

# A TCP probe rather than an HTTP GET, which would open the SSE stream and hang
# (the same reason as scripts/playground.sh).
for _ in $(seq 1 60); do
	(exec 3<>/dev/tcp/localhost/"$PORT") 2>/dev/null && { exec 3>&-; break; }
	sleep 0.5
done

cd "$ROOT/surfaces/chat"
go run . \
	--config "$ROOT/examples/host-exercise/agentchat.json" \
	--active "${HOST_EXERCISE_ACTIVE:-deepseek}" \
	--ui plain
