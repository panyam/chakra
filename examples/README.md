# examples/agents

Agent-focused examples grouped together, sharing a common demo harness.

- **`agent-async`** manages async work (events and tasks) through chat.
- **`multi-agent`** covers Phase 3 composition, with sub-agents as tools and handoff.
- **`critic`** has a second model watch the primary agent's turns and inject graded steering notes.
- **`deep-agent-supervisor`** supervises a server-advertised roster of specialist agents.
- **`kitchen-sink`** wires every agent feature at once.

## One config per example, shared across surfaces

Each example has **one host config**, and all its surfaces load it:

| Recipe | Surface | What it does |
|---|---|---|
| `just agent` | scripted | deterministic StubProviders, no LLM (the golden test) |
| `just demo` | CLI (live) | a live model against the same server; resolves model/endpoint from `llm.json` |
| `just serve` | server | boots the demo MCP server for the live surfaces to point at |
| `just chat` | CLI (`agentchat`) | the terminal surface, `--config <config>.json` |
| `just web` | browser (`agentweb`) | the browser surface, `--config <config>.json --addr :8090` |

`chat` and `web` are shared recipes in `common.just`, parameterized by `CONFIG`
(default `config.json`) and `ADDR`. Three examples were already config-driven:
`deep-agent-supervisor`, `kitchen-sink` and `examples/playground`. They have
their own variable-driven justfiles, and each gained the same `web` surface.

The three code-driven examples (`agent-async`, `multi-agent`, `critic`) each
ship a `config.json` too. Their deterministic scenario loads that same
`config.json` and adds in code only what JSON cannot express. That means the
scripted `StubProvider`, and for `multi-agent` and `critic` the hand-wired
composition as well. Each example's README documents its own split.

## `llm.json` holds providers, not secrets

`llm.json` lists named connections (local, cloud, a router) in the same shape as
the host `ConnectionsConfig`. It holds **only** the endpoint, the model and the
*name* of the env var with the key (`apiKeyEnv`). It **never** holds a key, so
it is safe to commit. The active connection is a local model, so `just demo`
works offline against a running LM Studio or Ollama with nothing to configure.
A model router (OpenRouter, LiteLLM, a gateway) is just another connection, so
point `baseURL` at it. For machine-specific overrides, copy `llm.json` to
`llm.local.json` (gitignored).

## Multi-agent via host config (agentchat)

`agentchat-multi-agent.json` is a sample host config. It declares **sub-agent
personas** in `subAgents`. Each persona is a specialist, and the main agent
delegates to it as a tool. A persona runs on the same provider, sees a filtered
view of the same server tools, and has its own instructions. Run it:

```bash
agentchat --config examples/agents/agentchat-multi-agent.json   # needs the demo server running
```

The sub-agents' activity renders **nested** under the main agent's turn
(`HostSubAgentEvent`). This config is the declarative counterpart to the
`multi-agent` example. That example wires the same `AgentSource`/`Team`
primitives by hand.
