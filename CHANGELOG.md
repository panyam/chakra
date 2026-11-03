# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Every module in the repo is tagged at
the same version.

## [Unreleased]

### Added
- `NewResponsesProvider`, a provider for OpenAI's Responses API. It chains tool steps with `previous_response_id` so a reasoning model keeps its reasoning between steps, and sends full history, reported through `ResponsesConfig.OnFullHistory`, when the history no longer matches a stored response (compaction, fork, resume, provider switch) (#157).
- `Capabilities.Temperature`: whether a provider expects its model to take `ProviderRequest.Temperature`. True on `OpenAIProvider` unless `OpenAIConfig.NoTemperature` (OpenAI's reasoning models refuse one), false on `ResponsesProvider` unless `ResponsesConfig.Temperature`, false on `AnthropicProvider`. `FailoverProvider` intersects it and the wrappers pass it through. It only changes what is reported; a set Temperature is still forwarded (#191).
- Host: `ConnectionConfig.Temperature` overrides that default per connection (#191).
- Host: an `openai-responses` connection type, with `reasoningEffort`. Its full-history notices arrive as `session-warn` events (#157).

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
