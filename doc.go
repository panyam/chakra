// Package agent is the agentic loop that turns a set of connected MCP servers
// and an LLM into a working agent. It sits above mcpkit and uses it as an
// ordinary dependency.
//
// docs/AGENT_DESIGN.md has the design, and the tracking epic has the delivery
// order. The module provides four seams:
//
//   - Provider: streaming LLM access with tool-call support
//   - Runner: the multi-step tool loop, emitting wire-serializable events
//   - ToolSource: tool aggregation across MCP servers and host-local functions
//   - Policy hooks: context injection and event-initiated turns
//
// It leaves out sessions, chat transports, and persistence on purpose.
// Applications that embed it own those, such as CLIs, web hosts, and native
// shells.
//
// It lives in its own repository, so LLM-provider dependencies never reach
// mcpkit's core, server, or client packages.
package agent
