# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Every module in the repo is tagged at
the same version.

## [Unreleased]

### Added
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
