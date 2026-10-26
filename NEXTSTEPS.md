# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #142 (`mission_api_v1`) is active: chakra 1.0 has a reviewed, settled exported API. Its exercise is `make api-report`, and the next ready ticket, #143, adds it. #40 (the exported-vs-internal audit) follows.
- Mission #89 (`mission_host_completion`) closed 2026-10-26: `make host-exercise` passes every check on both wires. #78, #86 and #87 stay `waiting`.
- Mission #57 (`mission_dogfood_coding`) closed 2026-10-26: the full exercise passed end to end at eb4a80f, `/undo` included. #34 moved off it, still `waiting`.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`; every go.mod replaces mcpkit with `../../mcpkit/main` (one more `../` per module depth). Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- `make host-exercise` needs no model; `make host-exercise-live` and `make dogfood-web` need one (`DOGFOOD_ACTIVE=deepseek` has been the working default). CI runs one client wire; run the host and root modules with `MCPKIT_CLIENT_MODE=stateless` too before trusting a change to server-sent data.

