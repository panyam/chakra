package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// FuncSource serves host-local Go functions as tools. So an agent can offer
// small utilities, such as dates, math or environment lookups, without running
// an MCP server for them. AddFunc registers them with types, and
// core.GenerateSchema builds each schema from the input struct.
type FuncSource struct {
	mu       sync.RWMutex
	defs     []core.ToolDef
	handlers map[string]func(ctx context.Context, args map[string]any) (*core.ToolResult, error)
}

// NewFuncSource returns an empty source; register tools with AddFunc or
// AddToolFunc before handing it to a Runner.
func NewFuncSource() *FuncSource {
	return &FuncSource{
		handlers: make(map[string]func(ctx context.Context, args map[string]any) (*core.ToolResult, error)),
	}
}

// FuncOption adjusts the tool definition that AddFunc or AddAsyncFunc builds.
type FuncOption func(*core.ToolDef)

// ReadOnly sets readOnlyHint on the tool. An approval policy such as
// TieredApproval in ModeReadOnlyAuto or ModeReversibleAuto then runs it without
// asking, and the Runner treats it as non-destructive and idempotent. Leave it
// off for anything that writes, sends, or starts work. The spec's default for
// an unannotated tool is the cautious one.
func ReadOnly() FuncOption {
	return func(d *core.ToolDef) {
		if d.Annotations == nil {
			d.Annotations = map[string]any{}
		}
		d.Annotations["readOnlyHint"] = true
	}
}

func funcToolDef[In any](name, description string, opts []FuncOption) (core.ToolDef, error) {
	var schema any
	if err := json.Unmarshal(core.GenerateSchema[In](), &schema); err != nil {
		return core.ToolDef{}, fmt.Errorf("agent: schema generation for %q: %w", name, err)
	}
	def := core.ToolDef{Name: name, Description: description, InputSchema: schema}
	for _, o := range opts {
		o(&def)
	}
	return def, nil
}

// AddFunc registers a typed function as a tool. Arguments are decoded into
// In via JSON round-trip, and that decoding is the only validation. The
// returned string becomes a single text content item. A handler error becomes
// an IsError tool result, so the model sees the failure. It is not a dispatch
// error. Two errors pass through to the Runner unchanged, since they are
// decisions rather than failures: a ToolDeniedError (DenyTool, DenyToolFree)
// and a FreeResultError (FreeResult).
// Registering a duplicate name returns an error. Options adjust the
// definition, for instance ReadOnly for a tool that changes nothing.
func AddFunc[In any](s *FuncSource, name, description string, fn func(ctx context.Context, in In) (string, error), opts ...FuncOption) error {
	def, err := funcToolDef[In](name, description, opts)
	if err != nil {
		return err
	}
	return s.AddToolFunc(def,
		func(ctx context.Context, args map[string]any) (*core.ToolResult, error) {
			var in In
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, fmt.Errorf("agent: encode args for %q: %w", name, err)
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return errorToolResult(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			out, err := fn(ctx, in)
			if err != nil {
				if passThrough(err) {
					return nil, err
				}
				return errorToolResult(err.Error()), nil
			}
			return &core.ToolResult{Content: []core.Content{{Type: "text", Text: out}}}, nil
		})
}

// AddToolFunc registers a tool with full control over the definition and the
// result shape. Use this when the tool needs structured content, a custom
// output schema, or non-text content items.
func (s *FuncSource) AddToolFunc(def core.ToolDef, fn func(ctx context.Context, args map[string]any) (*core.ToolResult, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.handlers[def.Name]; exists {
		return fmt.Errorf("agent: tool %q already registered", def.Name)
	}
	s.defs = append(s.defs, def)
	s.handlers[def.Name] = fn
	return nil
}

// Tools returns the registered definitions in registration order.
func (s *FuncSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

// Call dispatches to the registered handler. Unknown names are dispatch
// errors. When a handler registered via AddFunc fails, Call returns an
// IsError result.
func (s *FuncSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	s.mu.RLock()
	fn, ok := s.handlers[name]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	return fn(ctx, args)
}

func errorToolResult(msg string) *core.ToolResult {
	return &core.ToolResult{
		IsError: true,
		Content: []core.Content{{Type: "text", Text: msg}},
	}
}

// AsyncResult is what an AddAsyncFunc handler returns. It holds an immediate
// acknowledgment, and the model sees that now as the tool result. It may also
// hold a completion. When the background work finishes, the completion's
// event is injected.
type AsyncResult struct {
	// Ack is the model-facing text returned immediately (e.g. "job started").
	Ack string
	// Await runs in a goroutine when non-nil. When it finishes, its
	// returned event goes to onComplete. Nil means fire-and-forget.
	Await func(ctx context.Context) (IncomingEvent, error)
}

// AddAsyncFunc registers a host-local tool that does background work without
// a server-side task. Every tool call needs one response, so the handler
// returns an ack immediately. The handler may also supply an Await. That runs
// on a goroutine, and its completion event goes to onComplete. A host wires
// onComplete to its injection policy, so the result reaches the model on a
// later turn. That matches the shape of a task.completed event, without a task
// runtime. For model-visible poll/cancel on local work, use a real server-side
// task instead.
func AddAsyncFunc[In any](s *FuncSource, name, description string,
	fn func(ctx context.Context, in In) (AsyncResult, error),
	onComplete func(IncomingEvent, error), opts ...FuncOption) error {
	def, err := funcToolDef[In](name, description, opts)
	if err != nil {
		return err
	}
	return s.AddToolFunc(def,
		func(ctx context.Context, args map[string]any) (*core.ToolResult, error) {
			var in In
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, fmt.Errorf("agent: encode args for %q: %w", name, err)
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return errorToolResult(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			res, err := fn(ctx, in)
			if err != nil {
				if passThrough(err) {
					return nil, err
				}
				return errorToolResult(err.Error()), nil
			}
			if res.Await != nil {
				go func() {
					ev, err := res.Await(context.WithoutCancel(ctx))
					if onComplete != nil {
						onComplete(ev, err)
					}
				}()
			}
			return &core.ToolResult{Content: []core.Content{{Type: "text", Text: res.Ack}}}, nil
		})
}
