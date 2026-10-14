# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #57 (`mission_dogfood_coding`): #58 closed by PR #60. `make dogfood` exists now and hasn't run with a model yet. Next ready: #59.
- chore/chakra-main-layout: PR #62, rebased onto main after #60. Next: merge it.
- This run: dropped feat/58-dogfood-exec (PR #60 merged); PR #55 closed.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`; every go.mod replaces mcpkit with `../../mcpkit/main` (one more `../` per module depth). Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- Mission log on #57 is behind. Neither #56 nor #60 has an entry. After #62 merges, run `make dogfood` (needs `ANTHROPIC_API_KEY` or `/provider local`) and post one entry covering both, quoting the exercise result. Undecided: should the agentchat README document `--workspace-config`? Today only the flag help and `examples/dogfood/README.md` cover it.

## chore/chakra-main-layout

- **Last touched**: 2026-10-14 11:39 (Mac:~/newstack/chakra/main)
- **Ticket**: no issue; PR #62
- **Why**: the repo moved from `~/newstack/chakra` to `~/newstack/chakra/main` to mirror mcpkit's layout. Replaces and CI paths gain one `../`.
- **Where it stopped**: rebased onto main after #60. The three surfaces go.mod conflicts were resolved by taking main and deepening the mcpkit replace. All replace paths resolve, `make vet` is clean, and the surfaces tests pass. Also carries the checkpoint (NOTES.md, CLAUDE.md, NEXTSTEPS.md).
- **Next action**: wait for CI on #62, then merge.
