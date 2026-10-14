# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #57 (`mission_dogfood_coding`): 0/8 tickets closed. Its exercise (`make dogfood`) has never run. Next ready: #58, in PR #60.
- chore/chakra-main-layout: PR #62. Next: merge it, then rebase #60.
- feat/58-dogfood-exec: PR #60, CI green. Next: merge after #62, run `make dogfood`, log the result on #57.
- This run: first NEXTSTEPS. Retriage applied (labels, mission #57); PR #56 merged.

## Across threads

- The checkout lives at `~/newstack/chakra/<branch>`; every go.mod replaces mcpkit with `../../mcpkit/main` (one more `../` per module depth). Worktrees go at `~/newstack/chakra/<name>`. See CLAUDE.md Traps and NOTES.md "Tracking mcpkit main".
- Still pending on GitHub because auto mode blocked them: close PR #55 as superseded by #56, and post the #56 mission log on #57 ("could not run: make dogfood doesn't exist yet").

## chore/chakra-main-layout

- **Last touched**: 2026-10-14 11:36 (Mac:~/newstack/chakra/main)
- **Ticket**: no issue; PR #62
- **Why**: the repo moved from `~/newstack/chakra` to `~/newstack/chakra/main` to mirror mcpkit's layout. Replaces and CI paths gain one `../`.
- **Where it stopped**: pushed; `make vet` clean from the new path. Also carries this checkpoint (NOTES.md, CLAUDE.md, NEXTSTEPS.md).
- **Next action**: wait for CI on #62, then merge.

## feat/58-dogfood-exec

- **Last touched**: 2026-10-14 11:36 (Mac:~/newstack/chakra/main)
- **Ticket**: #58, PR #60 (CI green)
- **Why**: makes the #57 exercise runnable by adding `--workspace-config` (exec, LSP, repo map), `make dogfood` and `scripts/test-pkg.sh`.
- **Where it stopped**: complete. A sandboxed `run_test`/`run_vet` smoke passed on darwin. It has never run with a real model, because `ANTHROPIC_API_KEY` wasn't set.
- **Next action**: after #62 merges, rebase. It touches only intra-repo lines in the surfaces go.mod files, so it should be clean. Then merge, run `make dogfood` with a key, and post the mission log on #57. Each failure becomes a ticket.
- **Open questions**: should the agentchat README document `--workspace-config`? It's covered today only by the flag help and `examples/dogfood/README.md`.
