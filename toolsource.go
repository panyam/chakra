package agent

import (
	"context"
	"errors"

	"github.com/panyam/mcpkit/core"
)

// ErrUnknownTool means a Call named a tool that the source does not have.
// ToolSource implementations wrap it. Aggregators use it to tell a
// stale-index miss, which is worth one refresh, from a definitive dispatch
// failure. The Runner uses it to feed a not-found back to the model.
var ErrUnknownTool = errors.New("agent: unknown tool")

// ToolSource is the Runner's view of callable tools, whatever their origin.
// The origin may be a connected MCP server, a host-local function, or an
// aggregation of other sources. The Runner may dispatch parallel tool calls
// against one source, so implementations must be safe for concurrent use.
type ToolSource interface {
	// Tools returns the definitions that the model should see. The
	// returned slice is a snapshot. Implementations decide their own
	// caching and refresh policy. Every returned name must be callable via
	// Call, and names within one source must be unique. Aggregators treat
	// a repeated name from the same source as one claim, and the first
	// definition wins. Only cross-source collisions get disambiguation.
	Tools(ctx context.Context) ([]core.ToolDef, error)

	// Call dispatches one tool invocation by name. A non-nil error means
	// the dispatch itself failed, from an unknown tool, a transport
	// failure, or an unresolved ambiguity. A tool that ran and failed
	// reports through ToolResult.IsError instead. Then the Runner can feed
	// the failure back to the model rather than aborting the turn.
	Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error)
}
