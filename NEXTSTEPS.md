# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #57 (`mission_dogfood_coding`): 1/8 closed. The only exercise entry (ef8bcb5) checked wiring without a model. Next: run `make dogfood` with a model, then #59.
- This run: dropped chore/chakra-main-layout (PR #62 merged). Deleted every merged remote branch; only `main` remains. `--workspace-config` is now documented in `surfaces/chat/README.md` (committed directly to main).

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`; every go.mod replaces mcpkit with `../../mcpkit/main` (one more `../` per module depth). Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- No exercise has run with a model yet. `make dogfood` needs `ANTHROPIC_API_KEY` or `/provider local` (LM Studio). Log the result on #57 before picking past #59, since it may reorder the queue.

