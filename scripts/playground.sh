#!/usr/bin/env bash
# Playground launcher (make pg): boots a demo MCP server and launches
# agentchat's TUI wired to it. Needs a local OpenAI-compatible model; see
# examples/playground/README.md.
#
# The server was mcpkit's getting-started example until the agent SDK was
# extracted to this repo. It is now examples/agent-async's app-domain server,
# which is here and serves events + tasks + send_email, so the playground
# exercises more than a single greet tool.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA="${AGENTCHAT_PG_DIR:-$HOME/.agentchat}"
PORT="${PG_PORT:-8787}"
mkdir -p "$DATA" "$DATA/pg-blobs"

echo "==> booting demo MCP server (agent-async app domain) on :$PORT"
( cd "$ROOT/examples/agent-async" && go run . --serve --addr ":$PORT" ) &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT

# Wait for the port via a TCP probe — NOT an HTTP GET. A GET to /mcp opens the
# server's SSE stream and never returns, so a curl-based check would hang the
# launch forever.
for _ in $(seq 1 60); do
	(exec 3<>/dev/tcp/localhost/"$PORT") 2>/dev/null && { exec 3>&-; break; }
	sleep 0.5
done

echo "==> launching agentchat playground (TUI). Edit examples/playground/playground.json for your model."
cd "$ROOT/surfaces/chat"
go run . \
	--config "$ROOT/examples/playground/playground.json" \
	--session-store "sqlite://$DATA/pg.db" \
	--offload-dir "$DATA/pg-blobs" \
	--offload-threshold 4096 \
	--ui tui
