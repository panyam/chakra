# examples/multi-agent

One runnable demo of mcpkit's Phase 3 multi-agent composition, in both modes.

- **Sub-agents as tools** (`agent.AgentSource`). A supervisor delegates a
  subtask to a child agent. The child runs over its **own isolated
  conversation** and returns an answer. The child's event stream surfaces to
  the transcript **nested** under the parent, via the `SubAgentEvent`
  scope/depth envelope.
- **Handoff** (`agent.Team`). A triage agent **transfers control** of the
  conversation to a specialist, and the specialist takes over the same thread.
  This is a transfer, not a call-and-return.

## Run

```bash
just agent          # deterministic StubProviders (no LLM, no network)
just test           # the golden transcript
go run . --model X  # drive the supervisor with a live OpenAI-compatible model
```

## One config, two surfaces

`config.json` is the **declarative host counterpart** to the hand-wired
composition above. In it, the two sub-agents become `subAgents` personas. They
share the main provider and a filtered view of a demo MCP server's tools. Both
the CLI (`agentchat`) and browser (`agentweb`) surfaces load it:

```bash
just serve   # the demo MCP server (web_search + run_code) on :8785
just chat    # agentchat CLI against config.json (another terminal)
just web     # agentweb browser surface against config.json (another terminal)
```

The golden scenario is still hand-wired. Each sub-agent gets its own scripted
`StubProvider` and `FuncSource`, and JSON cannot express those. `config.json`
is the config-driven version of the same supervisor → researcher/coder shape.
Handoff (`Team`) has no host-config surface in this example. It runs only under
`just agent` / `--model`.

Sample output:

```
── Supervisor (sub-agents as tools) ──
user: Research Go generics and write an example.
  [researcher] · calls web_search
  [researcher] → Go generics let you write type-parameterized functions ...
  [coder] · calls run_code
  [coder] → func Max[T cmp.Ordered](a, b T) T { ... }
supervisor → Generics add type parameters (1.18); here's a generic Max function.

── Team (handoff) ──
user: I was double-charged and need a refund.
→ handed off: triage → billing
billing → I've refunded the duplicate charge — you'll see it in 3-5 days.
```

## What to read

- `scenario.go` builds the two sub-agents, each a `Runner` plus a `FuncSource`
  tool, as `AgentSource`s under a supervisor `MultiSource`. It also builds a
  `Team` with a `transfer_to_billing` handoff. `nestedRenderer` turns
  `SubAgentEvent`s into the indented transcript.
- `scenario_test.go` has the golden transcript and runs under
  `just test-agent`.

The whole flow is scripted, so it doubles as a deterministic test. The
sub-agents use host-local `FuncSource` tools instead of an MCP server, to keep
the focus on agent *composition*. The same `AgentSource`/`Team` work over any
`ToolSource`, including MCP-server tools.
