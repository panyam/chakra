# Where behavior hooks in

This is the map we use to decide where a new behavior goes: compaction, an
injection, a tool rewrite, a new approval rule. A turn passes through four hook
points, in order, and each one has its own shape. Most "where should this go"
questions come down to which point the behavior needs, and whether what it
produces is part of the record or only part of the model's view.

Every PR says at the top which hook it uses and why (see `CLAUDE.md`). When a
change needs a hook point this page doesn't list, that is the decision to call
out, and this page gets updated in the same PR.

## The four hook points

| Point | Runs | What hooks in today | Shape | Code |
|---|---|---|---|---|
| **Turn start** (host) | once per turn, before the Runner | `Extension.TurnStart`, `InputTransform`s, durable `ContextStage`s (event injection), the user message, transient `ContextStage`s (memory summary, recall), `PromptSection`s | one ordered pipeline with two phases | `host/app.go` `RunTurn`, `host/contextpipeline.go` |
| **Before each model call** (Runner) | every step | `Compactor` (step 1 only), tree budget, `Selector`, `GenerationParams` | separate typed fields, **no chain** (see Planned below) | `runner.go` `Run` |
| **Around each tool call** (Runner) | every tool call, in parallel | `ToolMiddleware`: spotlight, extension middleware, critique, the permission gate (with `ApprovalRenderer`s and `ApprovalChooser`s); offloading wraps the tool source the chain ends in | one ordered chain, gate last | `toolmiddleware.go`, `approval.go`, `host/app.go` |
| **Signals and events** | between steps (signals), and between turns (events) | `SignalPolicy`, `Interruptible`, `PreemptGrant`; the host's injection and trigger policies | separate fields | `signal.go`, `injection.go`, `triggers.go`, `host/events.go` |

Two things are deliberately not hooks. Forking and delegation are tools (a
sub-agent is a `ToolSource`), so the model decides when they happen, which is
what A8 asks for. Elicitation is a request/response seam (`ElicitationUI`), not
an event, so it has its own path to the surfaces.

## A turn, end to end

```mermaid
flowchart TD
  subgraph HOST["Turn start (host)"]
    H1["Extension.TurnStart"] --> H2["InputTransforms"]
    H2 --> H3["durable ContextStages: injected events"]
    H3 --> H4["user message"]
    H4 --> H5["transient ContextStages: summary, recall"]
  end

  subgraph RUN["Runner.Run"]
    R0["system prompt: Instructions / PromptSections, once"] --> R1["Compactor, step 1 only"]
    R1 --> S1
    subgraph STEP["each step: before the model call"]
      S1["tree budget: charge one step"] --> S2["list tools, Selector"]
      S2 --> S3["Provider.Stream: events out"]
    end
    S3 -->|"no tool calls"| F1["structured finalize, if ResponseSchema"]
    S3 -->|"tool calls"| T1
    subgraph TOOL["each tool call, in parallel"]
      T1["spotlight"] --> T2["extension middleware"]
      T2 --> T3["critique"]
      T3 --> T4["permission gate: rule / mode, then chooser or ask"]
      T4 --> T5["OffloadingSource: big results become stubs"]
      T5 --> T6["MultiSource: the tool"]
    end
    T6 --> G1["append tool results"]
    G1 --> G2["signals: SignalPolicy, then a durable note"]
    G2 -->|"next step, up to MaxSteps"| S1
  end

  H5 --> R0
  F1 --> P1["host: keep messages, persist, HostTurnDone"]
  G2 -.->|"MaxSteps hit: partial turn kept"| P1

  EV["server events"] -.-> INJ["injection policy: queued for the next turn"]
  INJ -.-> H3
  EV -.-> TRG["trigger policy"]
  TRG -.->|"proactive turn: same path"| H1
```

Solid arrows are one turn. Dotted ones are inputs that arrive from outside a
turn and wait for the next one.

## Record or view

Every hook that produces messages produces one of two kinds, and mixing them
up is silent and compounding.

- **Record** messages join the session's history: they persist, the next turn
  sees them, and the session store replays them. The user message, injected
  events, assistant replies, tool results and signal notes are record.
- **View** messages are what one request sends and nothing else. The memory
  summary, recall, a compaction summary and narrowed tool lists are view.

The host keeps the two apart structurally at turn start (`durable` and
`transient` in `contextPipeline`). The Runner keeps them apart by
`TurnResult.Messages`, which is everything the turn added and nothing it only
showed the model, so a compaction never shrinks the record.

A view message written into the record gets re-injected next turn, then
summarized alongside real conversation. A record message kept only in the view
loses the reason the model did something. So a new hook says which one it
produces.

## Order inside each point

The order within a point is decided, not incidental. Each one has its reason
written next to the code.

- **Turn start:** events, then the user message, then summary and recall,
  then compaction (in the Runner). Recall sits closest to the user message
  because it is the most specific to this turn. See `contextPipeline`.
- **Tool calls:** anything that rewrites arguments goes before the gate, so the
  gate and its prompt see what will actually run. Critique goes before the
  gate because it is automated, so a call it refuses never reaches the human.
  The gate goes last. Offloading wraps the tool source the chain ends in, so
  middleware that reads a result (`AfterToolCall`) sees the stub for a big one,
  not the full text.
- **Signals:** tool results are appended before any signal note, because
  providers pair a tool result with the assistant call right before it.

## Choosing a hook

Ask these in order.

1. **Does the model decide when it happens?** Then it's a tool, not a hook.
2. **Is it about the user's input, or context for the whole turn?** Turn
   start. Durable stage if the session should remember it, transient if not.
3. **Does it need to see or change what one model call sends** (messages,
   tools, budget)? Before each model call.
4. **Does it act on one tool call** (its arguments, whether it runs, its
   result)? Tool middleware, placed by the ordering rules above. A decision a
   human makes goes through the gate (`ApprovalRenderer` for the question,
   `ApprovalChooser` to approve part of a call), not through new middleware.
5. **Does it react to something outside the turn** (a server event, a child
   agent)? Signals and events.

## Planned

**A step chain at "before each model call"** (#7). Today this point is a set of
separate fields, and their order is the order of lines in `runner.go`.
Mid-turn compaction, a step budget the model can see, and mid-turn injection
all want this point, so instead of adding a field for each we're adding one
chain with the same record/view split as the host's context pipeline.
`Compactor` and `Selector` become built-in stages there, with their fields kept
as adapters. This page gets updated when it lands.
