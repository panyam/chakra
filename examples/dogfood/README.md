# dogfood

The exercise for the dogfood mission (issue 57): agentchat working as a coding
agent on this repository. It finds code, edits it, runs the tests, reads the
failures and fixes them, with `/undo` available if a change goes wrong.

```bash
make dogfood-web DOGFOOD_ACTIVE=deepseek   # then open http://localhost:8090/
```

The browser is where the exercise runs for now. `make dogfood` runs the same
setup in the agentchat TUI, which is parked: it hangs on parallel tool calls
and drops keystrokes (issue 66). `DOGFOOD_UI=plain` gets a line-based
agentchat if you want a terminal anyway.

## What you need

- `gopls` on PATH (`go install golang.org/x/tools/gopls@latest`). It drives the
  diagnostics after each edit and the repo map in the system prompt.
- A model. `config.json` makes Claude the active connection and reads
  `ANTHROPIC_API_KEY`. `DOGFOOD_ACTIVE` starts on another one instead:
  `make dogfood DOGFOOD_ACTIVE=deepseek` (reads `DEEPSEEK_API_KEY`),
  `openrouter` (reads `OPENROUTER_API_KEY`), or `local` (an LM Studio model on
  `http://localhost:1234/v1`). `/provider <name>` switches mid-session. Edit
  `connections` to change the models.
- macOS for the sandbox. `workspace.json` asks for the platform sandbox, and
  only darwin has one so far (issue 32). On Linux, set `"sandbox": "unconfined"`
  in a copy of the file if you accept running the commands without one.

## What is in the files

- `config.json` holds the host settings: instructions, connections, a step
  budget, and `read-only-auto` approval. `maxSteps` is 40 because a coding
  turn reads, edits, runs a test, reads the failure and fixes it, and the
  agent's default of 8 ran out halfway through that on the first run. Under that mode reads, searches and the test, vet
  and build commands run without asking, while edits and writes ask.
- `workspace.json` holds what the CLI flags cannot express. That is the exec
  allowlist (`run_test`, `run_test_one`, `run_test_all`, `run_vet`,
  `run_build`), gopls, and the repo map. The model picks one of those tools and
  can pass package paths to `run_test`, or one `./pkg:TestPattern` to
  `run_test_one`, but it can never compose a command line.

The workspace root is the repo itself, from `--workspace`. Checkpoints and
sessions live under `~/.agentchat/dogfood` (override with `DOGFOOD_DIR`) so
they sit outside anything the agent can edit.

## Running the exercise

Ask for a small, real change that has a test, then watch. Slash commands
(`/undo`, `/checkpoints`, `/provider`) work from the browser's input box.

1. Did it find the right files without being told the paths?
2. Did it run `run_test` on what it touched without being asked?
3. When a test failed, did it read the failing assertion, rather than a
   truncated middle of the output?
4. Does `make test` pass on the result when you run it yourself?
5. Does `/undo` put the files back?

Post the result on issue 57 as a mission log comment, and file each failure as
a ticket on the mission.
