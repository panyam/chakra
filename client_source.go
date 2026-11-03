package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// ErrInputRequired is returned by ClientSource.Call when the server asks for
// SEP-2322 mid-call input and no InputHandler was configured. Install one with
// WithInputHandler, as the ElicitationCoordinator example in elicitation.go
// shows. Callers can detect the condition with errors.Is.
var ErrInputRequired = errors.New("agent: tool requires mid-call input and no InputHandler is configured")

// ClientSource adapts one connected client.Client into a ToolSource.
//
// Calls go through the client's MRTR-aware path. So ctx cancellation reaches
// the wire, and mid-call input requests surface deterministically. With the
// default configuration, an input-required round fails with ErrInputRequired
// instead of hanging or guessing.
type ClientSource struct {
	c            *client.Client
	handler      client.InputHandler
	onTaskStatus func(*core.DetailedTask)

	taskGrace  time.Duration
	onDetach   func(*client.BackgroundTask)
	onComplete func(*client.BackgroundTask)
}

// ClientSourceOption configures a ClientSource.
type ClientSourceOption func(*ClientSource)

// WithInputHandler installs the SEP-2322 input handler used when a tool call
// returns input_required mid-dispatch. The elicitation seam wires the host UI
// through this option; tests can use it to script input rounds.
func WithInputHandler(h client.InputHandler) ClientSourceOption {
	return func(s *ClientSource) { s.handler = h }
}

// WithTaskStatusHook observes every polled task snapshot during task-backed
// tool calls. It is passed through to the client-level WaitOptions.OnStatus,
// so surfaces can render progress between tool-begin and tool-end.
func WithTaskStatusHook(fn func(*core.DetailedTask)) ClientSourceOption {
	return func(s *ClientSource) { s.onTaskStatus = fn }
}

// WithTaskGrace opts task-backed calls into background detach. Imagine a call
// is still working when the window expires. It returns immediately with a
// model-visible "running in the background" result, and the client's poll
// continues on a detached goroutine. The window holds while an input pause is
// active. So an interactive task that parks in input_required within the grace
// does not detach. Zero or unset keeps the synchronous wait-to-terminal
// contract.
// The detach mechanism itself lives in client.WaitForTaskOrBackground.
func WithTaskGrace(d time.Duration) ClientSourceOption {
	return func(s *ClientSource) { s.taskGrace = d }
}

// WithTaskDetachHook observes each detach and delivers the
// client.BackgroundTask handle. Registries, transcript lines, and cancellation
// surfaces use that handle.
func WithTaskDetachHook(fn func(*client.BackgroundTask)) ClientSourceOption {
	return func(s *ClientSource) { s.onDetach = fn }
}

// WithTaskCompletionHook fires when a DETACHED task reaches its terminal
// state. Inline completions return through Call as usual. The agent watches
// the handle's Done channel and invokes this from a watcher goroutine. A host
// that turns completions into events or proactive turns does that here.
func WithTaskCompletionHook(fn func(*client.BackgroundTask)) ClientSourceOption {
	return func(s *ClientSource) { s.onComplete = fn }
}

// NewClientSource wraps a connected client. The client must already be
// initialized; ClientSource performs no connection management.
func NewClientSource(c *client.Client, opts ...ClientSourceOption) *ClientSource {
	s := &ClientSource{
		c: c,
		handler: func(ctx context.Context, reqs core.InputRequests) (core.InputResponses, error) {
			return nil, ErrInputRequired
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Tools lists the server's tools in model-facing form (server-side
// visibility filtering applied, per ListToolsForModel).
func (s *ClientSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return s.c.ListToolsForModel(ctx)
}

// Call dispatches tools/call with automatic MRTR rounds via the configured
// InputHandler. Call waits for a task-creating response to reach a terminal
// state. Polling honors the server's interval hints. input_required pauses
// resolve through the SAME InputHandler, so task input reaches the elicitation
// seam with no extra wiring. The terminal snapshot maps back onto the sync
// contract. A completed task returns its ToolResult, including tool-side
// IsError, and a failed or cancelled task is a dispatch error.
func (s *ClientSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	res, err := client.CallToolWithInputs(ctx, s.c, name, args, s.handler)
	if err != nil {
		return nil, err
	}
	switch {
	case res.Sync != nil:
		return res.Sync, nil
	case res.Task != nil:
		return s.waitTask(ctx, name, res.Task.TaskID)
	default:
		return nil, fmt.Errorf("agent: unexpected tool call result shape for %q", name)
	}
}

func (s *ClientSource) waitTask(ctx context.Context, name, taskID string) (*core.ToolResult, error) {
	dt, bt, err := client.WaitForTaskOrBackground(ctx, s.c, taskID, name, s.handler, s.taskGrace,
		client.WaitOptions{OnStatus: s.onTaskStatus})
	if err != nil {
		return nil, err
	}
	if bt != nil {
		// Detached. Hand the client-owned handle to the host, and watch its
		// Done for the completion hook. Only detached tasks get that hook.
		// Then give the model a result so the turn finishes.
		if s.onDetach != nil {
			s.onDetach(bt)
		}
		if s.onComplete != nil {
			go func() {
				<-bt.Done()
				s.onComplete(bt)
			}()
		}
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "task " + taskID + " (" + name +
			") is still running and has moved to the background; the user can keep working and will be told when it finishes."}}}, nil
	}
	return mapTerminalTask(dt, name, taskID)
}

func mapTerminalTask(dt *core.DetailedTask, name, taskID string) (*core.ToolResult, error) {
	switch dt.Status {
	case core.TaskCompleted:
		if dt.Result == nil {
			return nil, fmt.Errorf("agent: task %s (%s) completed without a result", taskID, name)
		}
		return dt.Result, nil
	case core.TaskFailed:
		msg := "unknown error"
		if dt.Error != nil {
			msg = dt.Error.Message
		}
		return nil, fmt.Errorf("agent: task %s (%s) failed: %s", taskID, name, msg)
	case core.TaskCancelled:
		return nil, fmt.Errorf("agent: task %s (%s) was cancelled", taskID, name)
	default:
		return nil, fmt.Errorf("agent: task %s (%s) ended in unexpected status %q", taskID, name, dt.Status)
	}
}
