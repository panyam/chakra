# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Every module in the repo is tagged at
the same version.

## [v0.3.2] - 2026-11-09

Full notes in [docs/releases/v0.3.2.md](docs/releases/v0.3.2.md).

### Added
- `FreeResult`: a tool answer the model reads as a result, which costs no step under the free-refusal rule. `AddFunc` and `AddAsyncFunc` handlers can return it, `DenyTool` or `DenyToolFree` themselves (PR 321).
- `NewRepeatCache`: a tool middleware that answers a repeated idempotent call with the turn's first answer, for free (PR 321).
- `NewSlotsWith` and `SlotsConfig`: the slots' opening line, and sending them as a user message (PR 321).
- `RetrievalInfo.Block`: the retrieval stage's injected text, on `EventRetrieved` (PR 321).
- `Capabilities.Sendable`: the penalty kinds a provider takes, with the rest dropped (PR 321).

### Changed
- 16 names in `host`, `surfaces`, `surfaces/web`, `ext/lsp` and `ext/exec` that nothing outside their package used are unexported (PR 309). The release notes list them.

### Fixed
- `tbench-lock.sh` claims its lock with an exclusive create of the pid file, since uutils `mkdir` let two racing callers both win (PR 311).

## [v0.3.1] - 2026-11-08

Full notes in [docs/releases/v0.3.1.md](docs/releases/v0.3.1.md).

### Added
- `agent.Slots`: named state slots shown after the history on every call and kept out of compaction, with `write_slot`, `EventSlot` and `RestoreSlots`. `update_plan` can write the plan slot (PR 307).
- `StuckConfig.Window` and `WindowRepeats`: the stuck detector's `window` pattern, for cycles of three or more actions (PR 303).
- `NewRetrievalStage` and `Retriever`: context gathered before a turn's first call, within one token budget (PR 290).
- Free refusals: `DenyToolFree` and a free turn-end `Continue` don't use up a step, capped by `RunnerConfig.MaxFreeRefusals` (PR 292).
- `provider/anthropic`, a new module on the official Anthropic SDK with prompt caching (PR 288), which also sends thinking back while the history before it is unchanged (PR 293). `host.ConnectionTypes` and `agent.ToolNames` (PR 288).
- `host.SlotsExtension`, `NewPlanSlotExtension` and `RunRestorer`, plus `"slots"` in the workspace file. The coding profile and Terminal-Bench turn on the plan and a facts slot (PR 307).
- The Terminal-Bench matrix on GitHub Pages (PR 304). `tbench-stale` runs one batch at a time and counts other checkouts' jobs, and ingest waits for a job to finish (PRs 291, 294, 297).

### Changed
- The no-SDK `AnthropicProvider` sends a `ResponseSchema` as `output_config.format`, in place of a forced synthetic tool (PR 293).

### Fixed
- The host declares the agents extension on server clients, which mcpkit v0.8.17 servers require for `agents/list` (PR 305).
- Every module requires mcpkit v0.8.17 (PRs 302, 306).

## [v0.3.0] - 2026-11-06

Full notes in [docs/releases/v0.3.0.md](docs/releases/v0.3.0.md).

### Breaking
- The root package's 14 `Default*` consts and `SpawnStatus` are unexported. Leave a field zero to get its default, which its doc comment gives (PR 272).
- `ext/files` unexports its edit and patch engines, `HashLen`, `DefaultListLimit` and `DefaultSearchLimit`. The tools don't change (PR 272).
- `ErrTurnStopped`'s message is now "agent: turn stopped". `errors.Is` is unaffected (PR 284).

### Added
- `GenerationParams.Penalties`: repeat, frequency and presence penalties and DRY, with `Capabilities.Penalties` and `CanPenalize`. llama.cpp sends all four kinds, and OpenAI and Ollama send frequency and presence. An unsupported kind is refused before the request leaves (PR 283).
- `NewStuckDetector`: a step stage that finds a repeat, an alternation or the same error at the end of a turn, emits `EventStuck`, and runs a policy. A host key reads text-action steps too (PR 284).
- Step stages get `StepInfo.Turn`, `StepInfo.Emit` and `StepView.Generation`, and can stop a turn with `ErrTurnStopped` (PR 284).
- The coding profile runs the stuck detector, with a key that ignores timings (PR 284).
- `EventStep` on every model call, so a killed turn's usage still shows (PR 269).
- `ConnectionConfig.MaxTokens` for Anthropic connections (PR 275).
- Offloaded tool results go in the session store whatever turned offload on, and `surfaces.ToolResultStoreFromSpec` (PR 270).
- `make api-report` and `make api-update` (PR 268).
- Terminal-Bench: a quota check, reference pricing and `tbench-ref` (PR 271), Claude models (PR 275), `TB_SETUP_TIMEOUT` (PR 286), and `make mission-tbench` (PRs 273, 263).

## [v0.2.9] - 2026-11-04

### Added
- Runner: a request stage sets the grammar for one call through `StepView.Guided`. It starts as the turn's `ResponseGuided`, a request stage may replace or clear it, and the next step starts from the turn's grammar again. A step with no tools sends it. A step that offers tools finalizes under its own grammar, and a cleared one skips the finalize. A stage's grammar is checked against the provider that sends it before the call (PR 260).
- `examples/terminal-bench`: `scripts/tbench_matrix.py` keeps a task by setup matrix of the last runs, and runs only the stale cells (PR 261).

### Changed
- Runner: a session or turn stage that changes `StepView.Guided` fails the turn. A stage that returns a `StepView` built from scratch, rather than the one it was given, now clears that call's grammar (PR 260).
- Runner: the error for a routed provider that can't guide now reads "step N's grammar (the step is routed to ...)". It still wraps `ErrUnsupportedParam` (PR 260).

## [v0.2.8] - 2026-11-03

### Added
- `ext/exec`: a later session lists, tails and kills the detached processes an earlier session started in the same workspace. A registry under `$XDG_STATE_HOME/chakra/detached/` records each one with its pid start time, so a reused pid isn't mistaken for it. `ProcessConfig.StateDir` moves the registry, and a detached process's exit code reaches the registry when it ends (PR 254).
- `ext/agentsmd`: an `@name` mention in a message loads that rule. `Profile.GlobalRules` reads a user rule folder, and the `claude` preset reads `~/.claude/rules`. `Profile.NestedRules` reads rule folders under subdirectories, and the `cursor` preset reads nested `.cursor/rules`. A rule edited mid-session reaches the model in the next turn's changes notice (PR 255).
- agentweb: coding sessions persist in the session store like chat ones. `--coding-state` holds a folder per session, closing a session removes its folder when no store is set, and the roster names each session's kind. `web.Profile` gains `Close`, `coding.Profile` gains `RunStore`, and `WebSession` gains `profile` and `path` (PR 256).
- agentweb: coding sessions get the header model picker (PR 256).

### Fixed
- `ext/exec`: polling a detached process no longer panics on its output buffer (PR 254).
- agentweb: `/code/` works under `--session-store` (PR 256).

## [v0.2.7] - 2026-11-03

### Added
- `host`: an extension contributes turn-end hooks through `Extension.TurnEndHooks`, and the main agent's Runner runs them. `BaseExtension` returns none (PR 253).
- `agentchat exec` exits 4 when a turn-end hook stops the turn (PR 253).

### Changed
- Breaking: `host.Extension` gains `TurnEndHooks() []agent.TurnEndHook`. An extension that embeds `BaseExtension` needs no change (PR 253).
- `host`: a turn that ends with `agent.ErrTurnStopped` keeps its messages in history and the run store, with the refused answer last, as a step-capped turn does (PR 253).

## [v0.2.6] - 2026-11-03

### Added
- `eval`: `SuiteCase.Setup` runs once before a case's turns and returns the tools, instructions and history the run uses, plus what it spent. `CaseReport` gains `Usage`, `SetupUsage`, `Elapsed`, `SetupElapsed` and `Cost`. `Suite.Prices` prices tokens per model, and `SuiteReport.CostPerCorrect` and `DimensionReport.CostPerPass` roll them up. An unpriced model leaves the cost unknown (PR 251).
- `eval/longmemeval`: `DatasetAdapter.Arm` prepares each instance for a memory layer that isn't a `MemoryStore`. `OfficialJudge` grades with LongMemEval's own prompt per question type, copied from upstream's `evaluate_qa.py` (PR 251).
- agentweb: `agentweb --coding <dir>` serves the coding agent at `/code/` beside chat, with one session manager and one Sessions panel. The coding profile moves into `surfaces/coding`, and `CreateSessionRequest` gains `profile` (PR 245).
- agentweb: a tty process's output is drawn in xterm.js at the pty's size. `ProcessOutput` carries `TTY`, `Cols` and `Rows` (PR 250).
- A tty process's terminal fits the view and takes keystrokes. `host.ProcessController` (`ResizeProcess`, `WriteProcess`) lets a surface resize a running process's pty and write to its input, and agentweb gains the `ResizeProcess` and `WriteProcess` RPCs. agentchat sizes each tty process to its own terminal. What the user types never reaches the model (PR 252).
- `ext/exec`: `exec_command` takes `detach: true` when `ProcessConfig.AllowDetach` is set, and the command keeps running after the session ends. Every detached call asks for approval (PR 237).
- `ext/agentsmd`: `Profile.Rules` reads rule folders such as `.cursor/rules` and `.claude/rules`, with always, glob and description triggers, and a `cursor` preset (PR 241).
- Skills: a skill's `allowed-tools` grants are listed in the `load_skill` ask. Approving the load approves them for the rest of the turn (PR 235).
- Terminal-Bench: chakra runs on the Responses API, `TB_EFFORT` pins reasoning effort for every agent, and `TB_SET=hard` picks eight hard tasks (PR 247).
- agentweb: a Playwright check for the header model picker, run in CI as `test-web-e2e` (PR 232).

### Changed
- `TurnResult.UsageByModel` counts a routed structured or guided turn's finalize call under the name a stage last gave a step on the configured provider. Before, it was counted under `""` (PR 229).
- `ext/exec` starts a tty at 120x30, where it used to be 0x0, and `done` waits up to 2s for a tty's output to drain, in place of a fixed 20ms (PR 250). A surface can then resize the tty (PR 252).
- Comments and markdown across the repo were rewritten so no sentence makes the reader wait (PRs 227, 230, 231, 233, 236, 238, 242).

### Fixed
- `ext/agentsmd`: a negative `Profile.MaxBytes` means no cap, as documented. Before, it loaded no instruction files at startup (PR 248).

## [v0.2.5] - 2026-11-03

### Added
- `TurnEndVerdict.Stop` ends the turn on the refused answer without another step. The turn returns an error wrapping the new `ErrTurnStopped` with the reason, and a partial `TurnResult` whose messages end with the refused answer (#222).
- `TurnEndVerdict.AsUser` sends a `Continue` note as a user message instead of a system note, for chat templates that reject a system message after the first (#222).
- `Consensus.Prior` counts answers the caller already has in the vote without redrawing them. They come first in `ConsensusResult.Draws`, marked `Prior`, and win ties (#222).
- Host: a connection's `"responseSchema": false` asks an OpenAI-wire provider for `json_object` and puts the schema in the system message, for servers like DeepSeek that answer 400 to `json_schema`. The Runner's validate-and-retry still holds the reply to the schema. The DeepSeek connections in the harness, dogfood and host-exercise configs set it (PR 226).
- agentchat: a routed turn prints a dim `↳ step N on <connection>` after each step, in both the line renderer and the notebook (PR 226).
- `surfaces/chat/README.md` documents the whole `connections` block (PR 226).

### Fixed
- The Responses provider's stream errors carry the server's code and message. OpenAI nests them under `error`, and a quota failure showed as `agent: stream error : ` (PR 221).

## [v0.2.4] - 2026-11-03

### Added
- Host: `"sampling": {"enabled": true}` answers servers' `sampling/createMessage` requests, asking the user first with the server, system prompt and messages shown (unless approval is `allow`). `connection`, `maxTokens` and `reviewResponse` pick the model, cap the reply and ask again before it goes back (PR 212).
- `ext/exec`: long-running processes. `ShellConfig.Processes` adds `exec_command`, `write_stdin`, `list_processes` and `kill_process`, with an optional pseudo-terminal (`tty: true`). Processes belong to the session, are capped per session and per output, and die with it (PR 213).
- `ext/exec`: per-domain network egress. `Config.Network` runs a local HTTP and SOCKS5 proxy that every command goes through, allowing listed hosts and, with `ask`, asking once per host per session (PR 217).
- `ext/exec`: a running process's output streams to surfaces as `process-output` events while it prints, shown live in agentweb's Tools panel and as `[p1]` lines in agentchat. The model's tool results are unchanged (PR 219).
- `StepView.Provider` and `StepView.ProviderName`: a request-scoped `StepStage` can send one model call to another provider, and name it. The next step starts back on `RunnerConfig.Provider`. A guided step checks the routed provider can send the constraint (#185).
- `EventStep`, with `Event.Model` and `Event.Usage`: one per named step, after its deltas. Steps no stage names emit none (#185).
- `TurnResult.UsageByModel`: the turn's usage split by step name, nil when no stage named a step (#185).
- Host: `connections.stepRoutes` and `StepRouteStage` route ranges of each turn's steps to other connections; unrouted steps stay on the active connection (#185).
- agentweb: a model picker in the header beside the model label, listing the session's connections; `GetStatusResponse` gains `providers` and `active_provider` (#185).
- Coding harness: `--route-to`, `--route-from` and `--route-model`, with tokens per model on the metrics line and in the parity table (#185).
- Host: MCP servers over stdio. `ServerConfig.Command` (with `args`, `dir`, `env`, `envPass` and `startupTimeoutSec`) launches a server that speaks MCP on stdin and stdout, in a clean environment that holds only `PATH`, `HOME` and what's named. A crash fails the call in flight as not available, and the next call starts a fresh process (#161).
- Host: `ServerConfig.CallTimeoutSec` ends a tool call on any server after that many seconds (#161).

## [v0.2.3] - 2026-11-03

### Added
- Runner: `TurnEnd` hooks, a hook point when the model answers without calling a tool. A hook accepts the answer or refuses it with a note, and the loop takes another step. With `ResponseSchema` or `ResponseGuided` set, hooks judge the constrained answer, with the model's free answer on `TurnEndInfo.Draft` (PRs 188, 210).
- Runner: `ResponseGuided` on `RunnerConfig` and `TurnRequest` holds a turn's answer to a grammar, regex or JSON schema the server enforces. A step with no tools carries it, and after a step with tools one constrained call with no tools writes the answer. `TurnEndInfo.Draft` and `TurnEndInfo.StructuredError` let hooks see the free answer and a document's validation errors (PR 210).
- `Consensus` draws a turn several times with seeds 0..n-1 and votes on a key the caller supplies, reporting the winner, agreement, whether the vote split, and whether the draws were seeded at all. Draws share the Runner's tree budget and report events as `DrawEvent` (PR 211).
- `NewResponsesProvider` for OpenAI's Responses API. It chains tool steps with `previous_response_id`, so a reasoning model keeps its reasoning between steps, and sends full history when the history no longer matches a stored response, saying why through `ResponsesConfig.OnFullHistory` (PR 198).
- `Capabilities.Temperature`: whether a provider expects its model to take a temperature. True on `OpenAIProvider` unless `OpenAIConfig.NoTemperature`, false on `ResponsesProvider` unless `ResponsesConfig.Temperature`, and false on Anthropic. It's advisory, and a set temperature is still forwarded (PR 207).
- `agent.PlanTool` (`update_plan`) and `EventPlan`, so a model can publish its plan and surfaces can show it. `host.NewPlanExtension()` and `"plan": true` in a workspace file turn it on (PRs 199, 206).
- `agentchat exec`: one headless task with `--json`, `--output-schema`, `--output-last-message` and exit codes that tell failures apart. The Runner now validates a structured answer against its schema, retries once with the errors, and reports `TurnResult.StructuredError` (PR 189).
- Host: `App.Submit` steers a running turn. Input sent mid-turn reaches the next model call, in agentweb, the TUI and notebook mode (PR 190).
- Host: skills from local folders (`.agents/skills`, `.claude/skills`, `~/.agents/skills`) join the skill catalog beside MCP-served ones (PR 192).
- Host: `Config.Roots` declares the workspace roots to MCP servers through `roots/list`, with `list_changed` from `App.SetRoots` on the session wire (PR 197).
- Host: `openai-responses` connection type with `reasoningEffort`, and `ConnectionConfig.Temperature` to override the temperature default per connection. Full-history notices arrive as `session-warn` events (PRs 198, 207).
- `ext/agentsmd` loads `AGENTS.md` and its relatives, with codex, claude and chakra presets (PR 183).
- `ext/files`: `apply_patch`, a multi-file patch tool in the `*** Begin Patch` format (PR 193).
- `ext/git`: read-only status, diffs against a base and log; opt-in commit, branch and worktree tools; a per-turn diff for `/diff`; and a review sub-agent that returns structured findings. The host gains `host.TurnEnder` and `host.SubAgentUser` (PRs 194, 196).
- Surfaces show the loaded instruction files and name the model in the status line, and agentweb can serve the coding agent (PR 186).
- `examples/terminal-bench`: chakra as a Harbor agent, `make tbench` and a side-by-side report against Codex (PR 204). `make coding-harness-parity` runs chakra over `openai-responses`, matching Codex's wire (PR 200).

### Fixed
- The OpenAI, Responses and Anthropic providers retry 429 and 5xx responses, waiting as long as the endpoint asks, instead of failing the turn on the first one (PR 208).
- The static Linux `agentchat` carries fallback root certificates for containers that have none (PR 208).
- With exec unconfined, the shell tool no longer offers escalation that headless `exec` would deny (PR 208).

## [v0.2.2] - 2026-11-03

### Added
- `NewLlamaCppProvider` and `NewOllamaProvider`, presets of `OpenAIProvider` for local servers. llama.cpp's sends grammars and JSON schemas in its dialect with `cache_prompt` on, and needs no model name. Ollama's sends a JSON schema as `response_format`, its only constraint, through the new `GuidedDialectOllama` (PR 182).
- `CapabilitiesOf(p)` and the optional `CapabilityReporter`: which guided kinds a provider can send and whether a seed. OpenAI, Anthropic, `FailoverProvider` and the thinking wrapper report them (PR 182).
- Host: a `llamacpp` connection type with an optional model, and `llamacpp` and `ollama` connections built through the presets (PR 182).

### Changed
- An `ollama` connection now sends a `Guided` JSON schema rather than refusing it. Requests without one are unchanged (PR 182).

## [v0.2.1] - 2026-11-02

### Added
- `Usage.CachedInputTokens`, the part of `InputTokens` a server's prompt cache served. Read from `prompt_tokens_details.cached_tokens` (OpenAI, llama.cpp), llama.cpp's `timings.cache_n` on older builds, and Anthropic's `cache_read_input_tokens`. The Runner sums it per turn (PR 176).

### Changed
- On AnthropicProvider, `Usage.InputTokens` now includes cache reads and writes, so it means the whole prompt on both providers. Nothing changes today, since chakra sends no `cache_control` (PR 176).

### Fixed
- The v0.2.0 commit tracked a built `examples/coding-harness` binary, which failed the CI gates. It is untracked and ignored. Module zips were unaffected (PR 177).

## [v0.2.0] - 2026-11-02

Full notes in [docs/releases/v0.2.0.md](docs/releases/v0.2.0.md).

### Breaking
- `AgentSourceConfig.InputSchema` and `AsyncAgentSourceConfig.InputSchema` are `core.RawJSON`, no longer `json.RawMessage`. Wrap with `core.NewRawJSON(raw)` (PR 48).
- `AddFunc` / `AddAsyncFunc` take a trailing `opts ...FuncOption`. Calls compile unchanged (PR 100).

### Added
- Per-request seed, guided decoding (llama.cpp, vLLM) and `OpenAIConfig.ExtraBody`, with `ErrUnsupportedParam` for parameters a provider can't honor (PR 170).
- A step chain before every model call, carrying compaction and the step budget (PR 128).
- Host: server instructions in the system prompt, input rewriting by extensions, fresh server-advertised agents, event hints, stateless `list_changed`, a skills catalog (PRs 101, 107, 63, 135, 138, 136).
- Extensions: `delete_file`, per-hunk edit approval, `run_test_one`, a repo map without opening files, turn-by-turn `/undo` (PRs 118, 123, 94, 133, 103).
- agentweb: bring-your-own-key sessions, phone layout, App Engine deployment (PRs 168, 167, 166).
- `make coding-harness-exercise` and `make host-exercise` (PRs 173, 115).

### Fixed
- Invalid JSON tool arguments no longer break encoding (PR 120). A step-capped turn keeps its messages (PR 119). `/undo` across directories and restarts (PR 72). Checkpoint restore keeps file modes (PR 132).

## [v0.1.0] - 2026-08-21

First release after the move out of mcpkit. Tag only, with no notes.
