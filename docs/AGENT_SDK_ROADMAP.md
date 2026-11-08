# mcpkit Agent SDK — Competitive Status & Gap Analysis

This document is a living assessment of the mcpkit `` host layer. It compares the layer against (a)
general agent frameworks (Mastra, Eino, Genkit-Go, langchaingo, swarmgo, agno-Go) and (b) real
coding-agent loops (Claude Code, Cursor, Gemini CLI, aider, Codex, OpenCode, and the Pi / oh-my-pi
line). The first edition (below the fold) framed these as "what would it take to build a complete
agent SDK." **Most of that roadmap has now shipped.** This edition re-baselines to *where we
actually stand* and *what is genuinely still missing*, and separates **tracked** gaps (open issues)
from **untracked** ones.

This snapshot reads `main` as of 2026-07, after the Phase 0–3 stack (issues #929–#1044). We
inventoried the `` source directly and swept the open issues. Verdicts respect `CONSTRAINTS.md` (A1
dependency direction, A2 wire-serializability, A6 mechanism-vs-policy layering).

---

## 1. Headline: how are we doing?

**Very well.** The first edition sketched a four-phase build-out, and almost all of it shipped over
~140 commits. The agent SDK today is no longer "a minimal loop". It has approval gating, a native
Anthropic provider, in-loop structured output, an eval harness, durable persistence with
fork/rewind, per-tool-call cancellation, tiered memory (working + semantic + compaction),
tool-result offloading, sub-agents + team handoff, and a real host surface (slash commands,
connection registry, TUI). The distinctive strengths still hold and now have better support. They
are the **async control plane** (triggers/events/tasks as model-facing meta-tools), **MCP-native
wire-agnosticism**, **A2 wire-serializability**, and **zero-overhead SEP-414 tracing**.

We evaluated ~~Phase 4, durable workflows (#928)~~ and **dropped it (2026-08-04, not-planned).** A
code-driven workflow engine has no AI in it, and it is a commodity (Temporal / Step Functions
territory). It is the *dual* of the model-driven agent loop rather than an extension of it. The
canonical workflow patterns already build on shipped primitives (§7). Constraint **A8** in
`CONSTRAINTS.md` records the decision. The remaining phases are still open as epics. Constraint A9
**re-scoped Phase 5**. Its Anthropic caching/thinking child #953 was dropped as loop-invisible,
logprob #1053 is still a loop-visible agent capability, and grammar #1054 is a marginal deferred
enhancement. The open phases are **Phase 5, provider control & decoding fidelity** (#1050: logprob
#1053, grammar decoding #1054), **Phase 6, test-time compute & routing reliability** (#1051:
sampling/vote #1056, confidence-gated cascades #1057; adjacent routing #991), and **Phase 7, safety
& guardrails** (#1052: prompt-injection spotlighting #1058). The previous edition listed some items
as *untracked*, namely logprob/grammar, guardrails, sampling/vote, cascade trigger, and
coding-surface. All of them are now **tracked phase children** (see §5a). Nothing identified is left
untracked, since the two opt-in Phase-7 extensions are now filed as #1060/#1061. Beyond the phases,
a rich refinement backlog remains on the shipped primitives, and most of it is tracked.

In 2026-07 we re-surveyed coding agents, starting with oh-my-pi (`can1357/oh-my-pi`). It is the
strongest current open coding agent, a TS+Rust batteries-included fork of Pi. The pass confirms the
picture. It also found two genuinely new **agent-layer** primitives we lack, **mid-turn stream-rule
steering** (#1147) and a **critic/observer model role** (#1148), and both are now filed. The rest of
that product's moat is the hash-anchored edit format, LSP-in-writes, DAP, a ~55k-LoC in-process Rust
tool core, and a browser. All of those belong to the **coding-surface** layer. We deliberately scope
that layer to a coding agent built *on* ``, not to `` itself (#1059). oh-my-pi is the clearest
evidence yet that #1059 is a real decision, and that harness/tool quality is a first-order
differentiator. See §4.

---

## 1a. Product frame (2026-08-09)

The SDK exists to support **the best agent experience, CLI and UI, for coding or any task**. That
frame decides what belongs here, and it settled #1059.

**`agentchat` and `agentweb` are the general-purpose harness. Coding is an extension over them, not
a separate module.** We worked through what a coding agent actually is, and essentially none of it
is loop machinery. Edit/search/bash tools are a `ToolSource`, sandboxing is a wrapper, a repo map is
a `PromptSection`, LSP is a tool plus an `AfterToolCall` middleware, and checkpointing is middleware
plus a command. Roughly, a coding agent is 60% tools, 25% prompts and 15% harness, and `` already is
the harness slice. §4a reached the same place from the other side. oh-my-pi's moat was a large
in-process tool core, not a better loop.

The host blocked it, not the loop. `host.App` has 52 fields and thirteen `AppOption`s, and **every
option replaces something already pluggable. None adds tools, middleware, or commands.**
`CommandRegistry.Register` has no caller outside the package (#1244 audit). A feature had nowhere
else to go, so every feature went into the struct.

Working order, ahead of the phase blocks:

1. **#1026 / #1024**, the pre-turn context pipeline. It is the fifth extension seam. Memory and
   event injection contribute per-turn context, and they cannot leave `App` without it. It was
   re-scoped from polish to prerequisite.
2. **#1250**, the `Extension` contract. Features cannot move out of `App` until it exists.
3. **#1251**, collapse `App`'s 52 fields (C2). It is independent and mechanical, and can merge
   first.
4. Migrate features out of `App` one at a time. Approval is the cheapest proof, since #1248 already
   made it middleware.
5. **#1252**, the coding extension.

Two issues run alongside these. **#1253** makes approval rendering pluggable, so an edit ask shows a
hunk instead of truncated JSON. **#1254** adds an input transform for `@file` references.

Some issues don't serve this goal. They are *hosted-deployment* concerns, and we parked them
deliberately rather than neglecting them. The parked ones are #752 cross-replica sessions, #483
middleware precompute, #1018 faster cosine, and to a lesser degree #999 RunStore GC. They matter for
running an agent as a multi-tenant service. They do little for a CLI on a laptop or a single-session
UI. Phase 5 is demoted to its one live child (#1053), and Phase 6 is parked. See the comments on
#1050 / #1051.

Two issues are still first-order for the experience regardless. **#1007** is the token-accurate
estimator. `len/4` is worst exactly on code and tool-result JSON, and compaction is calibrated off
it. ~~#1006~~ is mid-turn compaction. It shipped as chakra#7, so `Compactor` now runs before every
step.

---

## 2. Status scorecard

Legend: ✅ shipped · 🟡 partial/shipped-with-follow-ups · ⏳ tracked (open issue) · ❌ untracked gap.

> **Before marking a row ✅, check that the loop can reach the primitive. A seam that declares it is
> not enough.** §7 once credited `ToolChoice` forced-tool as shipped for Best-of-N and judge panels.
> `ProviderRequest` did declare it, and both providers rendered it onto the wire. But the Runner's
> step loop never populated it, so no caller could set it on a turn. #1239 fixed that. A seam can
> look complete while nothing drives it, and then reading the type definition confirms the wrong
> half. The doc line was the smaller cost. We scoped and sized #1056 against that row, and the
> estimate was off by the size of a missing contract decision. When a row's evidence is a type
> declaration, grep for a non-test assignment before marking it.

| Area | Status | As-built (file) | Remaining |
|---|---|---|---|
| **Tool-call interception (hooks) + permission ladder** | ✅ #929 | `toolhook.go` — `ToolHook` (`BeforeTool` may rewrite args or deny, `AfterTool` may rewrite the result), `RunnerConfig.ToolHooks`, ordered, deny short-circuits. `approval.go` — `TieredApproval` is a hook, not a parallel seam: `ApprovalMode` {AlwaysAsk/ReadOnlyAuto/AlwaysAllow} + per-tool `RuleAsk/Allow/Deny`; "ask" routes through `ElicitationCoordinator.Confirm`; `EventToolDenied`; host `/approve`, appended last | — |
| **Anthropic-native provider** | ✅ #930 | `anthropic_provider.go` — no-SDK, content-block↔Delta, `thinking_delta`→reasoning; structured output via `output_config.format` (#287; it forced a synthetic tool before) | Deliberately **minimal** (constraint A9). Caching/extended-thinking **dropped (not-planned, #953)** — provider-client features below the loop; wrap the official SDK if ever needed. |
| **Structured output in the loop** | ✅ #931 | finalizing `Generate` (`runner.go` `finalizeStructured`, retry×2); `RunnerConfig.ResponseSchema` → `TurnResult.Structured` | — |
| **Eval / scorer harness** | 🟡 #974,#932 | `eval/` — `Case`/`Scorer`/`Suite`/`Scenario`; 8 deterministic scorers; `Judge` (build-tagged); LongMemEval *smoke* scenarios | external-benchmark adapter **⏳ #1015**; real LongMemEval loader **⏳ #1014** |
| **Persistence (RunStore) + fork/rewind** | ✅ #960,#962,#963,#986 | `runstore.go` — full interface + `InMemoryRunStore`; redis + gorm (pg/sqlite) backends; `ForkRun{AtMessage}` checkpoint fork; `ListRuns`; `Message.Timestamp` | retention/GC **⏳ #999** |
| **Per-tool-call cancellation / interrupt** | ✅ #936,#937 | `runner.go` `TurnRequest`/`Control` channel (per-`CallID`); `EventToolCancelled` | — |
| **Working memory** | ✅ #938,#1003,#1140 | `memory.go` — `MemorySource` remember/recall/forget; `Summary`/`RecallRelevant`; `InMemoryMemoryStore`; durable + per-request `Namespace` backends (redis/gorm, #1003); session-scoping by run id (#1140) | sub-agent memory model (injection over shared store) **#1151** |
| **Semantic recall (vector)** | ✅ #940,#1019 | `embedder.go` (`Embedder`, `OpenAIEmbedder`), `semantic_memory.go` (`InMemorySemanticStore`), gorm **pgvector** store; pre-turn recall auto-injection | standalone doc-RAG VectorStore **⏳ #1021**; reranker **⏳ #1020**; auto-distillation **⏳ #1022** |
| **Compaction / summarization** | 🟡 #939,#1011 | `compaction.go` — `SummarizingCompactor`, `TokenEstimator`/`CharTokenEstimator`, `EventCompaction`; runs before every step (step chain, chakra#7) with an `ErrContextBudget` floor; budgeted summary injection | real tokenizer **⏳ #1007** |
| **Tool-result offloading (context mgmt)** | ✅ #966,#971,#972 | `offloading_source.go` + `ToolResultStore` (mem/redis/gorm); `read_tool_result` (offset/limit/grep + two-sided context, bounded) | streaming/handle-based very-large results **⏳ #980,#979** |
| **Sub-agents (agent-as-tool)** | ✅ #941,#942,#943,#1031,#1032,#1033,#1035,#1036,#1042 | `agent_source.go` (`AgentSource`, depth+budget caps, structured I/O), `async_agent_source.go` (`AsyncAgentSource` Task form, #1035), `fanout_source.go` (`FanOutSource`, #1033), `team.go` (`Team` handoff + host wiring + tagging, #1042), `tree_budget.go` (`TreeBudget` aggregate cap, #1032), `signal.go` + `agent_pool.go` (upward signals + runner-control pool + interruptible turn, #1036), `SubAgentEvent` nesting, declarative host personas | dynamic catalog #1038, nested config #1043, interaction mediator #1157, map-style fan-out (unfiled) |
| **Host surface** | ✅ #984–#992 | slash-command registry, `ConnectionRegistry` + runtime `/provider`, `HostEvent`/`Observer` render seam, notebook renderer (#1001), interactive `/mcp` + `/sessions` overlay (#1095, `focusLayer`/`modalHost` seam + `client.Group.Reconnect`), per-server tool view (#1117) + oauth login action / authorization-code auth type (#1116, #907), dialog stack for nested-overlay back-nav (#1124), color accessibility (#1125), bubbletea TUI, playground | context-assembly pipeline **⏳ #1024,#1026**; remaining TUI-track items (dimmed-base compositing, grapheme width, `WindowSizeMsg` fan-out) **⏳ #1063** |
| **Observability** | ✅ | SEP-414 tracing (`agent.turn/step/tool`, `agent.memory.recall`) + OTel **metrics** (#1023 — turn/step/token/tool counters + duration histograms via `RunnerConfig.MeterProvider`, `host.WithMeterProvider`, agentchat `SetupMeter`, `mcpkit-agent` Grafana dashboard) | — |
| **Durable workflows / graphs (Phase 4)** | ❌ **dropped (not-planned, 2026-08-04)** | — | Not building an engine (constraint A8). Workflow patterns build on shipped primitives (§7); durable orchestration → integrate a dedicated engine. #928/#944/#945/#946 closed not-planned. |
| **Provider routing / cascades** | ⏳ #991 | only `FailoverProvider` (failure+cooldown) today | per-turn/per-role routing **#991**, router presets **#1044**, confidence-gated cascade **#1057** (Phase 6) |
| **Provider control & decoding fidelity (Phase 5)** | ⏳ #1050 (re-scoped by A9) | structured output via finalizing `Generate`; generation parameters reachable from a turn via `RunnerConfig.Generation` / `TurnRequest.Generation` (#1239 — temperature, token cap, tool choice; before it, three of the four `ProviderRequest` parameters could not be set on a Runner at all); per-request seed and guided decoding for llama.cpp and vLLM **✅ #169 / #19**, with local-server presets and provider capabilities **✅ #180** | logprob/token-confidence **#1053** — **loop-visible** (routing/abstention/cascade), kept as agent-SDK work; OpenAI-wire/local, not Anthropic. Guided decoding on SGLang: no dialect yet. Anthropic caching/thinking **dropped (#953, loop-invisible)**. |
| **Test-time compute (Phase 6)** | ⏳ #1051 | `#1033` `FanOutSource` already gives N concurrent runs, member ordering, failure isolation, and a pluggable `Aggregate` hook | sampling/vote **#1056** — re-scoped down to two aggregators over that hook (see §5a); `FailoverProvider` quality trigger **#1057**, blocked on #1053 |
| **Safety & guardrails (Phase 7)** | 🟡 #1052 | approval ladder (#929) + reversibility as an axis distinct from `readOnlyHint` **✅ #1260** (`ModeReversibleAuto`); `ToolMiddleware` is the interception seam guardrails attach to; prompt-injection spotlighting **✅ #1058**, now provenance-labelled rather than a trusted bool **✅ #1262** (only `operator` exempts — trusting a relay is not trusting what it relayed); reversal seam + file checkpoints + model-proposed undo **✅ #1267** (constraint A11: only a true inverse runs unattended); provenance derived from a tool's source rather than restated in config **✅ #1268** (`operator` is a closed allowlist — an extension is never vouched for by derivation) | efficacy against a live model is unproven by unit tests — needs the AgentDojo suite **#1060**, which is also what would show whether differentiated fences beat one fence, and what would make **#1269** (provenance as an approval key) worth building; constitutional gate **#1061**; no label for in-process-but-unvouched output **#1273** |
| **Coding surface (sandboxing, hooks, repo map, LSP)** | 🟡 **decided** — #1059 closed | Hooks shipped in `` as `ToolMiddleware` (#1248). The rest are an **extension** over the general harness, not a separate module: epic **#1252**. First child shipped: file checkpoint / rewind **✅ #1267** in `ext/checkpoint`, the first real `host.Extension`. Building it showed that the contract had no lifecycle hook, so `TurnStart` was added. Anchored edit primitive **✅ #1275** in `ext/files`: content anchors locate a hunk, a content hash detects the file changing underneath, and neither substitutes for the other. It is named `files` rather than `coding` because nothing in it is code-aware. That name splits the epic's children along a line worth keeping. **✅ #1284** completed the toolset with `write_file`, `list_files` and `search_files`, so an agent can find the files it edits. `write_file` has no create-or-overwrite mode, and `expect_hash` alone separates create from replace | LSP-in-loop **✅ #1301** shipped as `ext/lsp`. It is named after the mechanism rather than the use case, for the same reason #1275 is `files` rather than `coding`. It is the first `ContextStages` consumer and the first extension to own a subprocess, so it added `host.Extension.Close`. Building it showed that **`ContextStage` is per-turn while an edit-check-fix loop is per-step**, so diagnostics ship on two paths. #1240 records that gap and three smaller seam gaps. Repo map **✅ #1311** could not be its own module, because C4 blocks `ext/repomap` from importing `ext/lsp`. Working out why showed that the two would run two language servers over the same tree. So the map is a second contribution from `ext/lsp`, gated by `Config.RepoMap`. Command execution and its sandbox **✅ #1312** shipped as `ext/exec`. It moved ahead of the test loop because the epic shipped the dangerous half first. It also corrected the epic twice. First, sandboxing cannot live in a `ToolSource` wrapper, because most tools are in-process functions or MCP calls with no subprocess to confine. Second, the approval gate is the primary defense, not the sandbox. Only the darwin `sandbox-exec` backend ships, with Linux carved out to #1320. The remaining #1252 child is the test/build loop (#1313), now unblocked. Diff-level review (#1288) is still **blocked**. Host decomposition **#1251** runs in parallel |

Phases 0–3 are effectively **done**. We evaluated Phase 4 (workflows) and **dropped** it. It is a
deliberate non-goal (constraint A8), not a gap. Three structural phases are still open, and issues
fully scope them. They are **Phases 5–7** (provider decoding control, test-time compute, safety).
Those phases promote the former §5b "untracked" gaps into tracked epics. Everything else open is
refinement of shipped primitives.

---

## 3. Gap table A — agent-framework parity (updated)

| Capability | Leaders | mcpkit status |
|---|---|---|
| Tiered memory: working + semantic recall + compaction | Mastra, langchaingo, agno-Go | ✅ **at parity** (#938/#940/#939) + pgvector (#1019); reranker/distillation tracked (#1020/#1022) |
| Multi-agent / handoffs / sub-agents | all five + Mastra | ✅ **at parity** (AgentSource #941, Team #943); richer composition tracked (#1032–#1038) |
| Eval / scorer framework | Mastra, Genkit *(rare in Go)* | ✅ **shipped** (#974) — a **differentiator vs the Go field**; external-suite adapter tracked (#1015) |
| Native providers beyond OpenAI-compat | all | ✅ Anthropic (#930), kept minimal (A9); caching/thinking **dropped (#953)** — provider-agnostic is the thesis, OpenAI-wire is the common case |
| Structured output *inside the loop* | Mastra, Genkit, Eino | ✅ **shipped** (#931) |
| Durable suspend/resume workflows; branch/parallel | Mastra, Eino, agno-Go, Genkit | ❌ **deliberate non-goal** (A8) — not a parity gap; patterns build on shipped primitives (§7), durable orchestration is integrated, not reimplemented |
| RAG pipeline (chunk/embed/retrieve/index) | Mastra, Eino, Genkit, agno-Go | 🟡 recall path shipped; standalone doc-RAG VectorStore tracked (#1021) |
| Prompt versioning / templating | Genkit (Dotprompt) | ❌ still minimal (`Instructions` + skills) — no issue |
| Voice (STT/TTS) | Mastra only | ❌ non-goal (no Go competitor either) |

mcpkit has moved from "behind on memory/multi-agent/evals" to **at or ahead of the Go field** on
those. Only Genkit matches its proper eval harness. Durable workflows are the only area where the
TS/heavier frameworks still lead. We dropped Phase 4 under constraint A8, so we leave that gap open
on purpose.

## 4. Gap table B — coding-agent-loop UX (updated)

The three once-universal gaps are now mostly closed:

| # | Feature | mcpkit status |
|---|---|---|
| 1 | **Tiered permission ladder** | ✅ shipped (#929) — modes + per-tool rules + runtime `/approve` |
| 2 | **Checkpoint / rewind** | 🟡 **conversation-state** side shipped (fork-at-point #962, resume/`ListRuns` #986); **file-state** rewind is a coding-surface concern, not in the general SDK |
| 3 | **Mid-turn interrupt** | ✅ shipped (#936/#937) — cancel one call, turn continues |

The second tier has shipped too, with context compaction ✅ (#939), isolated-context subagents ✅
(#941), session persistence/resume/fork ✅ (#960/#962), and slash + custom commands ✅ (#985).
**Hooks** shipped as `ToolMiddleware` (#1248). `ToolMiddleware` also absorbed the approval policy
rather than sitting beside it. Seven items are now children of the coding-extension epic **#1252**.
They are **sandboxing**, **repo map**, **LSP-in-loop**, a soft **tool-call budget gate**, an
anchored **edit primitive**, **file-state rewind**, and a **test/build feedback loop**. Each one
implements existing seams, and none is new loop machinery.

### 4a. The coding-agent field, re-surveyed against oh-my-pi

Two related things share the name. The first is **Pi**, the *minimal upstream* harness from Mario
Zechner / Earendil Inc. (pi.dev, `@earendil-works/pi-coding-agent`, `badlogic/pi-mono`). Its pitch
is "many agent harnesses but this one is yours." It has four modes (TUI/print/RPC/SDK),
tree-structured history, and mid-session steering. It **deliberately omits MCP, sub-agents,
permission popups, plan mode, todos, and background bash**, and leaves them as extension points. The
second is **oh-my-pi** (`can1357/oh-my-pi`, Can Bölük), a TS+Rust, batteries-included **fork**. It
*adds* exactly what Pi omits. Its additions include MCP, 32 tools, subagents, LSP/DAP, the
hash-anchored edit format, and a ~55k-LoC Rust core. It is currently the most feature-complete
*open* coding-agent surface.

The pairing teaches us two things. First, **Pi is the closer philosophical peer to ``**. Both are
lean, embeddable, extension-first harnesses with the same four surfaces. oh-my-pi is precisely the
*coding agent built on top*, the #1059 shape. Second, **mcpkit is already ahead of upstream Pi on
MCP-native operation and sub-agents**. Pi omits both by design. The fork had to bolt them on, and in
the fork MCP is one integration among many rather than the native fabric. So the comparison below is
really against oh-my-pi *the product*, on the coding-agent axis (Claude Code / Cursor), not against
a Mastra/Eino-style library.

oh-my-pi's explicit thesis is that *the harness, not the model, decides*, and we take it seriously.
It reports multi-fold first-attempt-edit and token-efficiency lifts on the *same weights*, purely
from a better edit format and tool ergonomics. That validates our tool-layer investment. It also
draws the line sharply. **`` ships no tool implementations at all**, because it is the SDK
substrate. So most of what makes that product good belongs to the coding-*surface* layer, and we
scope that layer to #1059.

The table maps its surface onto our status and the A6 layering line.

| oh-my-pi capability | Nature | mcpkit `` status |
|---|---|---|
| Hash-anchored edit format (content-hash anchors, stale-anchor reject) | coding-surface | out of SDK scope → **#1059** (the SDK has no edit tool; a coding agent built on it would) |
| LSP-in-writes (rename via `willRenameFiles`), DAP debugger, AST edits | coding-surface | out of scope → **#1059** |
| ~55k-LoC in-process Rust core (ripgrep/glob/shell/PTY/AST, no fork-exec) | coding-surface / perf | out of scope → **#1059**; `` is provider+loop, transport-agnostic by design |
| Browser / computer-use, web_search, GitHub-as-filesystem, `://` resource schemes | coding-surface | out of scope → tool authoring on top of the SDK |
| **Mid-turn stream-rule steering** (regex/AST over the *output* stream → abort → inject rule → retry within the turn; fired-state survives compaction) | **agent-layer** | **not shipped → newly filed #1147.** Our injection is turn-boundary only; the delta scanner (#989) is the hook |
| **Critic/observer model** (second model on its own context reviews each turn, injects graded steering: aside/concern/blocker; cannot approve/deny) | **agent-layer** | **not shipped → newly filed #1148.** Composable on Observer #992 + AgentSource #941 + Control #936, but not a role yet |
| Deterministic image-frame compaction ("snapcompact" — renders discarded history to per-model-sized PNG frames to exploit image-token billing) | agent-layer (novel) | we have `SummarizingCompactor` (#939); this is an exotic provider-billing optimization, noted under compaction depth #1006/#1007, not a parity gap |
| Subagents: agent-as-tool with **schema-validated structured results** (3-level schema precedence), per-agent model/tool/depth, peer **hub** for inter-agent messaging | agent-layer | **at rough parity behind tracked gaps** — AgentSource #941, per-Runner provider, MaxDepth/budget; structured I/O + fan-out #1033; peer messaging ≈ upward-signals #1036 |
| Memory: pluggable SQLite backends, first-turn recall injection, periodic retain, **offline cross-session consolidation into a "mental model" loaded on turn 1**, polyphonic (vector/graph/fact/temporal) recall | agent-layer | **at rough parity behind tracked gaps** — MemorySource #938, semantic recall #940/#1019, injection #1011; consolidation = auto-distillation #1022; multi-signal recall = reranker #1020; durable backends #1003 |
| Provider routing: per-role models, path-scoped model sets, fallback chains, round-robin credentials | agent-layer | **they ship features that are only tracked issues for us**. We have only `FailoverProvider` today; per-role/path routing is #991, presets #1044 |
| ACP surface (editor-drivable), RPC (NDJSON stdio), collab live-share | surface | ACP is a notable coding-surface integration we do not have; collab is out of scope |

On the *loop* primitives (memory, compaction, multi-agent, structured output), we are at parity or
behind by a tracked issue, not behind in kind. The pass found two real agent-layer gaps (#1147,
#1148), and both are filed. The product's other strengths are either coding-surface work or provider
routing that we have already scheduled. The strategic signal is about the coding surface, not the
SDK. **A large share of end-user value lives in the coding-surface layer (#1059)**, and a
product-grade coding agent on top of `` would actually compete. The SDK is necessary but not
sufficient.

---

## 5. What's still missing

### 5a. Tracked (open issues — planned work)

The structural phases are tracked as epics.
- **Phase 4, durable workflows, was DROPPED (not-planned, 2026-08-04; constraint A8).**
  #928/#944/#945/#946 are closed. A workflow engine is code-driven orchestration with no AI in it.
  It is the commodity dual of the model-driven agent loop, not an extension of it. The canonical
  workflow patterns already build on shipped primitives (§7). When real durable orchestration is
  needed, integrate a dedicated engine (Temporal, Step Functions) rather than reimplement one. #945
  had one durable idea, that the trigger machinery *is* the suspend/resume primitive.
  `TriggerPolicy` + `IncomingEvent` already realize it.
- Constraint A9 re-scoped **Phase 5, provider control & decoding fidelity**. A9 separates
  loop-visible capability from loop-invisible optimization.
  - **We KEPT logprob / token-confidence #1053 as agent-SDK work.** It is loop-visible, since token
    confidence feeds routing, abstention, and the Phase 6 confidence-gated cascade (#1057). It's an
    OpenAI-wire / local-inference capability, and Anthropic doesn't expose logprobs. So it serves
    the common providers, not a niche vendor. The `Provider` seam exposes it capability-optionally,
    with nil meaning unsupported.
  - **Grammar-constrained / guided decoding #1054 (chakra #19) shipped with seed (chakra #169).**
    jaala's translator was the concrete need. `ProviderRequest.Guided` takes a grammar, regex or
    JSON Schema, and `OpenAIConfig.GuidedDialect` maps it onto llama.cpp (`grammar` / `json_schema`)
    or vLLM 0.12+ (`structured_outputs`). `ProviderRequest.Seed` / `GenerationParams.Seed` carry a
    per-draw seed, which a vote needs. `OpenAIConfig.ExtraBody` carries server extras like
    llama.cpp's `cache_prompt`. Anthropic refuses both with `ErrUnsupportedParam`. SGLang has no
    dialect yet. `NewLlamaCppProvider` and `NewOllamaProvider` (chakra #180) preset those for local
    servers. Ollama's dialect sends a JSON schema as `response_format`, its only constraint. The
    host gains a `llamacpp` connection type. A caller holding any `Provider` can ask
    `CapabilitiesOf` which of those parameters it can send. Repeat penalties and DRY followed for
    the same translator (chakra #276). `GenerationParams.Penalties` carries them, and
    `Capabilities.Penalties` names the kinds a provider sends. llama.cpp sends all four kinds,
    OpenAI and Ollama send frequency and presence, and Anthropic and Responses refuse them.
  - **We dropped Anthropic prompt caching & extended thinking #953 (not-planned).** It is a
    loop-invisible optimization. It is also the cautionary case for growing the no-SDK client. Its
    `budget_tokens` premise already 400s on current models, an example of the API-drift treadmill.
  - The phase's original "provider control" framing was too provider-flavored, and A9 draws the
    lasting line. New fields are capability-optional, and nil keeps today's behavior
    (A2/CONSTRAINTS).
- **Phase 6, test-time compute & routing reliability**, is epic #1051. Its children are the
  sampling+aggregate helper #1056 and the `FailoverProvider` quality-score trigger for
  confidence-gated cascades #1057. It complements upfront routing #991. #991 selects a model up
  front, and Phase 6 escalates.
  - **A design pass re-scoped #1056 down (2026-08-07)**, and the reasoning generalizes. Most of its
    mechanism already ships. `FanOutConfig` provides N concurrent runs, stable member ordering,
    per-member failure isolation, and a pluggable `Aggregate` reduce hook, so the delta is two
    aggregator functions rather than a new primitive. Its premise was also not portable.
    Self-consistency is defined as sampling at `Temperature>0`, and current Anthropic models reject
    sampling parameters outright. So identical-member diversity is the one unportable case.
    Per-member providers are the portable source of diversity, and multi-model already gives us
    those free. And we could not identify a user. The issue traces to the §5b competitive sweep, not
    a request. **We should ask the same three questions of the other phase children.** What already
    ships? Does the premise hold on every provider? Who asked?
  - #1057 is blocked on #1053, because a confidence-gated cascade needs a confidence signal to gate
    on.
- **Phase 7, safety & guardrails**, is epic #1052. Its child #1058 is a prompt-injection
  spotlighting/datamarking `Transform` stage for untrusted tool output. Its opt-in extensions are
  the AgentDojo eval suite #1060 and the constitutional pre-dispatch critique gate #1061. #1059 is
  the coding-surface scope decision (sandboxing/hooks/repo map/LSP).

The refinement backlog covers shipped primitives.
- **Provider routing / cascades** has #991 (per-turn/per-role model selection over
  `ConnectionRegistry`) and #1044 (openrouter/litellm presets). Today only the failure-triggered
  `FailoverProvider` exists.
- **Prompt caching + extended thinking** (~~#953~~) was **dropped (not-planned, constraint A9)**.
  Both are provider-client features below the agent loop. The native provider is still minimal. Deep
  provider features would wrap the official SDK behind the `Provider` seam, and only when a concrete
  need appears.
- **Memory depth** has standalone doc-RAG `VectorStore` #1021, Scorer/Reranker seam #1020,
  auto-distillation write-path #1022, sub-agent memory model #1151, and faster cosine #1018.
  Durable/session-scoped MemoryStore #1003/#1140 shipped.
- **Compaction depth** has mid-turn compaction #1006 and token-accurate estimator #1007.
- **Sub-agent composition** has upward signals / runner-control meta-tools + interruptible turn
  #1036, which **✅ SHIPPED** (A #1165/PR 1168, B #1166/PR 1169, C #1167/PR 1170). Still open are the
  dynamic agent catalog + transfer graph #1038, full nested per-agent config #1043, and cross-agent
  interaction mediator #1157.
- **Context assembly** has the explicit pre-turn pipeline #1026 and a unified injection budget with
  a final arbiter #1024.
- **Eval** has the external eval/conformance adapter seam #1015 (see §6) and a real LongMemEval
  loader #1014.
- **Ops** has RunStore retention/GC #999, large/binary tool results #980/#979, thinking-hint stream
  parser #989, and session-picker paging #1000. OTel metrics seam #1023 shipped.
- **Mid-turn steering & critique** came from the coding-agent re-survey (§4a). Stream-rule steering
  #1147 scans the output stream and can abort, inject and retry within the turn, and it builds on
  the #989 scanner. #1148 adds a first-class critic/observer model role that injects graded
  steering, and it composes on Observer #992 + AgentSource #941 + Control #936. Both are agent-layer
  and A6-clean.

### 5b. Untracked (no issue yet)

Nothing. This section used to list six gaps. They were logprob exposure, grammar/guided decoding,
the sampling/vote helper, the prompt-injection guardrail, the `FailoverProvider` quality-score
trigger, and the coding-surface scoping call. All six are now tracked phase children (Phases 5–7 and
#1059; see §5a). The two Phase-7 extensions are filed too, as the AgentDojo eval suite #1060 and the
constitutional critique gate #1061. The 2026-07 coding-agent re-survey (§4a) surfaced two further
agent-layer gaps, mid-turn stream-rule steering and a critic/observer model role. We filed them on
discovery as #1147 and #1148. Everything identified is tracked, and new gaps get filed as they
surface.

---

## 6. Validation & benchmarks

This section is unchanged in substance. Issue **#1015** now tracks the recommendation below exactly,
an external eval/conformance adapter seam. The competitor projects still ship no reusable suite.
Eino has none, Mastra has a TS judge lib, and Genkit's is flow-coupled. aider's harness is
aider-coupled, but its polyglot problem set is reusable data. MCP's own conformance is wire-level
only, and **no first-party agent-level MCP conformance suite exists.** These practical stand-ins are
reachable via the existing OpenAI-compatible endpoint / an HTTP shim.

| Suite | Validates | Integration cost |
|---|---|---|
| **BFCL v3/v4** | tool-calling fidelity | lowest — OpenAI-compatible `--skip-server-setup` |
| **τ²-bench** | multi-turn tool-agent-*user* loop | one thin Python `HalfDuplexAgent` proxy → our Runner |
| **SWE-bench Verified** | end coding-task resolution | near-zero (predictions JSONL); adopt when a coding agent exists |
| **AgentDojo** | prompt-injection security | optional; pairs with the §5b spotlighting gap |

The internal `eval` harness (StubProvider + spans, ✅ shipped) is the CI gate. The
**external-benchmark adapter (#1015)** is the missing second layer, and it starts with BFCL. Only
Genkit matches mcpkit's own `eval` scorer harness in the Go field, so the harness is itself a
differentiator.

---

## 7. Advanced techniques enabled by our primitives

The technique catalog maps research through 2025 to primitives, and its analysis is unchanged. But
the shipped memory + sub-agent + structured-output work has **moved most of it from "needs a roadmap
primitive" to "buildable today."** The updated status follows.

### Now buildable on shipped primitives
- **Sleep-time compute / Generative-Agents reflection / A-MEM evolution** — trigger/injection async
  control plane + `SummarizingCompactor` + `MemorySource` (all ✅). ([2504.13171](https://arxiv.org/abs/2504.13171),
  [2304.03442](https://arxiv.org/abs/2304.03442), [2502.12110](https://arxiv.org/abs/2502.12110))
- **Semantic recall / MemGPT paging** — `Embedder` + `InMemorySemanticStore`/pgvector + injection (✅).
  ([2310.08560](https://arxiv.org/abs/2310.08560))
- **Multi-agent debate / Mixture-of-Agents / CAMEL / supervisor** — `AgentSource` + `MultiSource` +
  `Team` (✅). ([2305.14325](https://arxiv.org/abs/2305.14325), [2406.04692](https://arxiv.org/abs/2406.04692))
- **Best-of-N + verifier / CRITIC / judge panel** — `ToolChoice` forced-tool + `ResponseSchema` +
  `FuncSource` verifiers + eval scorers (✅ as of #1239). ([2110.14168](https://arxiv.org/abs/2110.14168),
  [2305.11738](https://arxiv.org/abs/2305.11738), [2404.18796](https://arxiv.org/abs/2404.18796))
  *Correction: this row read ✅ before #1239, but `ToolChoice` was unreachable from a turn. The
  Runner's step loop sent only `{Instructions, Messages, Tools}`, so `Temperature`, `MaxTokens`, and
  `ToolChoice` could not be set on a Runner at all. #1239 added `RunnerConfig.Generation` /
  `TurnRequest.Generation`, and the row is now accurate. #1056 was sized S–M because of the stale
  claim. See that issue for the re-scope.*
- **Tool retrieval via `Selector`** — narrow a big `MultiSource` per step (✅; relevance needs an
  embedder, now present). ([2410.14594](https://arxiv.org/abs/2410.14594))
- **CodeAct** — `execute_code` `FuncSource` + forced-tool + error-feedback (✅).
  ([2402.01030](https://arxiv.org/abs/2402.01030))

### Still gated on missing infra (from §5b)
- **Grammar-guided decoding** ([2307.09702](https://arxiv.org/abs/2307.09702)) — gated on §5b #2.
- **Calibrated uncertainty / abstention** ([2407.16221](https://arxiv.org/abs/2407.16221)) — gated on
  logprob exposure §5b #1.
- **Model cascades (FrugalGPT)** ([2305.05176](https://arxiv.org/abs/2305.05176)) — gated on §5b #5 /
  routing #991.
- **Spotlighting injection defense** ([2403.14720](https://arxiv.org/abs/2403.14720)) — gated on §5b #4.
- **Tree search (ToT/LATS/MCTS)** — sub-agents exist, but no search/fork controller helper (§5b #3
  neighborhood).

We re-ranked the demo shortlist for what ships today. It is (1) tool-retrieval-via-`Selector`, (2)
Best-of-N with a `FuncSource` verifier, (3) sleep-time compute (the async-control-plane showcase),
(4) judge panel (seeds the eval harness), and (5) Mixture-of-Agents / debate on
`AgentSource`+`Team`. All five now build on shipped primitives. So the "experiment with SOTA agent
strategies on mcpkit" story is available *now*, not after Phase 4.

---

## Appendix — key files (current)

| Area | Files |
|---|---|
| Loop / approval / cancel / structured | `runner.go`, `approval.go` |
| Providers | `provider.go`, `anthropic_provider.go`, `openai_provider.go`, `failover.go` |
| Memory | `memory.go`, `semantic_memory.go`, `embedder.go`, `compaction.go` |
| Persistence / offloading | `runstore.go`, `toolresultstore.go`, `offloading_source.go`, `store/{redis,gorm}` |
| Multi-agent | `agent_source.go`, `team.go` |
| Eval | `eval/` (`eval.go`, `scorer.go`, `judge.go`, `suite.go`, `scenario.go`, `longmemeval/`) |
| Tool layer / events / policies | `toolsource.go`, `multi_source.go`, `filter_source.go`, `events.go`, `injection.go`, `triggers.go`, `stages.go` |
| Host surface | `host/` (`commands.go`, `connections.go`, `render.go`, `hostevent.go`, `memory.go`, `subagents.go`, `persistence.go`), `surfaces/chat/` |
| Invariants | `docs/AGENT_DESIGN.md`, `CONSTRAINTS.md` |

---

<details>
<summary><b>First edition (2026-07, pre-implementation): "What it would take to build a solid Agent SDK"</b> — retained for provenance; superseded by the status above.</summary>

Git history preserves the original build-out roadmap at the first revisions of this file. It covered
Phases 0–4, the E/G/F/H/D/I/A/B/C area designs, and the effort/sequencing analysis. Its designs were
implemented substantially as written. See the git log for issues #929–#1044, and the scorecard in §2
for the as-built mapping. It also introduced the competitor surveys and technique catalog, and those
live on in §3, §4, §6 and §7 above, updated to current status.

</details>
