package host

import (
	"context"
	"fmt"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// availabilitySource wraps a server's ToolSource. Suppose a call fails
// because the server is unreachable right now. The client hit a transport
// error and could not heal it by reconnecting. The wrapper then returns
// agent.ErrNotAvailableNow with a model-friendly message instead of a raw
// "connection refused". The Runner turns ErrNotAvailableNow into a non-fatal
// EventToolUnavailable, and the turn continues. The model can retry, route
// around it, or tell the user.
//
// Tools() is delegated unchanged. The defs remain listed even while the
// server is down (keep-the-defs, docs/AGENT_SERVER_STATE.md), so the tool set
// does not shrink mid-turn and surprise the model. This wrapper reacts to
// failures. A proactive health poll could mark a server down before a call
// reaches it, and it can be added later without changing this.
type availabilitySource struct {
	inner    agent.ToolSource
	serverID string
}

func newAvailabilitySource(inner agent.ToolSource, serverID string) *availabilitySource {
	return &availabilitySource{inner: inner, serverID: serverID}
}

func (s *availabilitySource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return s.inner.Tools(ctx)
}

func (s *availabilitySource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	res, err := s.inner.Call(ctx, name, args)
	// A transient (network-level) error means the server is unreachable. The
	// client already tried to reconnect and could not. A tool that ran and
	// failed reports via ToolResult.IsError, with no error here. A
	// server-side JSON-RPC / 4xx error is not transient. Both fall through
	// unchanged.
	if err != nil && client.IsTransientError(err) {
		return nil, fmt.Errorf("%w: tool %q is temporarily unavailable — server %q is not reachable right now (%v); retry, use another approach, or tell the user",
			agent.ErrNotAvailableNow, name, s.serverID, err)
	}
	return res, err
}
