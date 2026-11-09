package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// ToolMiddleware wraps the dispatch of one tool call. It is the single
// interception seam. Permission gates, argument redaction, rate and budget
// limits, blocklists, result caching, retries, audit trails, and marking
// untrusted tool output are all middleware rather than separate mechanisms.
//
// This is deliberately not a lifecycle hook, and mcpkit does not have those.
// The event stream reports what a turn does. The Runner already emits
// EventToolBegin, EventToolEnd, EventToolError, EventToolDenied,
// EventToolCancelled, and EventToolUnavailable. So every outcome shape is
// observable without registering anything here. Reach for middleware only to
// *change* behaviour. It can alter what runs, what comes back, or whether the
// call happens at all. A middleware that only observes should be an event
// subscriber instead.
//
// The shape is the familiar wrapping one, and it matches client.Client's own
// call middleware. A middleware receives the call, does work, and invokes
// next to continue. Everything it does happens around that invocation.
//
//   - Rewrite arguments: edit info.Call.Args, then call next.
//   - Transform the result: call next, then edit what it returned.
//   - Deny: return DenyTool(reason) without calling next.
//   - Serve from cache: return a result without calling next.
//   - Retry or time the call: invoke next more than once, or measure around it.
//
// Order is RunnerConfig.ToolMiddleware order, outermost first. The first entry
// wraps the second, and the last entry wraps the real dispatch. So a
// permission gate belongs *last*. There it sees the arguments after every
// earlier entry has rewritten them, and nothing can edit them behind its
// back. agent/host appends TieredApproval last for exactly that reason.
//
// A middleware that does not call next has decided the call, and nothing
// further down the chain runs. Implementations must be safe for concurrent
// use, because a turn dispatches parallel tool calls through the same chain.
type ToolMiddleware func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error)

// ToolCallFunc dispatches a tool call. Middleware receives one as next. It
// runs the rest of the chain, which ends in the real ToolSource.Call.
type ToolCallFunc func(ctx context.Context, info ToolCallInfo) (*core.ToolResult, error)

// ToolCallInfo is the call travelling down the chain. Middleware may modify
// it before passing it on. The innermost dispatch executes the copy that
// reaches it.
type ToolCallInfo struct {
	// Step is the number of the model call that requested this dispatch,
	// counting from one within the turn.
	Step int

	// Call is the tool invocation. It has the name, the provider-assigned ID,
	// and the arguments. Earlier middleware may have rewritten the arguments,
	// so they can differ from what the model produced. Args is core.RawJSON
	// per A5, so a middleware can Bind for typed inspection without a second
	// parse.
	Call ToolCall

	// Scope says where in the run this call sits. It has the agent ancestry,
	// the remaining sub-agent call budget, and the turn tree's usage against
	// its caps. The Runner fills it in at dispatch. See RunScope.
	//
	// A middleware needs it to behave correctly at depth. A checkpoint that
	// ignores it snapshots once per nested call. A budget-aware middleware
	// that ignores it can't back off before exhaustion.
	Scope RunScope

	// ReadOnly reflects the tool's readOnlyHint annotation, false when the
	// tool declares none. It is a hint from the server, not a guarantee that
	// the call is free of side effects.
	ReadOnly bool

	// Destructive reports whether the call's effect is irreversible or hard
	// to undo, from the tool's destructiveHint annotation. It is a hint from
	// the server, not a guarantee.
	//
	// It is true when the tool declares nothing, because the spec's default
	// for an absent destructiveHint on a writing tool is destructive. That is
	// the opposite of the Go zero value. So a hand-built ToolCallInfo{} reads
	// as non-destructive, and only a value from the Runner gives a meaningful
	// answer. It is normalized against ReadOnly, which the spec says wins. A
	// read-only tool reports false whatever it annotated.
	Destructive bool

	// Idempotent reports whether repeating the call with the same arguments
	// has no additional effect, from the tool's idempotentHint annotation. It
	// is a hint from the server, not a guarantee.
	//
	// It is false when the tool declares nothing, which matches the spec
	// default. So here the zero value is the conservative reading. Read-only
	// tools report true.
	Idempotent bool

	// Class is the gate's classifier verdict on this call. It is nil when no
	// classifier claimed the call. The gate sets it before deciding, so the
	// ask and its renderer see why the gate is asking about a call. By then
	// ReadOnly, Destructive and Idempotent already reflect it. Middleware
	// before the gate always sees nil.
	Class *CallClass
}

// classified returns info with c applied. It sets Class. Unless c has no
// opinion, it also replaces the safety hints with c's disposition.
func (info ToolCallInfo) classified(c CallClass) ToolCallInfo {
	info.Class = &c
	switch c.Disposition {
	case DispositionReadOnly:
		info.ReadOnly, info.Destructive, info.Idempotent = true, false, true
	case DispositionReversible:
		info.ReadOnly, info.Destructive = false, false
	case DispositionDestructive, DispositionAlwaysAsk:
		info.ReadOnly, info.Destructive = false, true
	}
	return info
}

// ToolDeniedError is how a middleware refuses a call. The Runner unwraps it
// specially. The model is told the call was not permitted, surfaces get
// EventToolDenied, and the turn continues. It is not a turn abort and not a
// tool failure. So the model can choose differently, and it doesn't see an
// error it can't act on.
type ToolDeniedError struct {
	// Reason is the short explanation surfaces show. Keep it to one legible
	// line, because agent/host renders it inline against the denied call and
	// truncates what does not fit.
	Reason string

	// ModelReason is what the model is told, when that has to differ from
	// what the surface shows. Empty means the model is told Reason. That is
	// right for a middleware that writes its reason in its own prose.
	//
	// Imagine a reason that quotes something which read attacker-controlled
	// input. A denial reaches the model in the policy layer's voice. That
	// voice would give the quoted attacker more authority than the tool
	// result the text came from. So the text needs a fence before the model
	// reads it. A fence is several lines, and a one-line surface render
	// can't hold it. So the two reasons differ. NewCritiqueGate quotes a
	// critic model, and this field exists for that case.
	ModelReason string

	// Free marks the refusal as one the turn doesn't pay a step for (#281).
	// Use it for a cheap check that catches a malformed call before it runs,
	// such as an edit that doesn't parse. A step counts as free only when
	// every call in it was refused free, and only up to
	// RunnerConfig.MaxFreeRefusals per turn. Its tokens still count.
	Free bool
}

func (e *ToolDeniedError) Error() string {
	if e.Reason == "" {
		return "tool call denied"
	}
	return "tool call denied: " + e.Reason
}

// DenyTool builds the error a middleware returns to refuse a call. Return it
// without calling next; an empty reason gets a default.
//
// Sometimes the model needs to see something other than the surfaced reason.
// Then the middleware builds a ToolDeniedError directly and sets ModelReason.
func DenyTool(reason string) error { return &ToolDeniedError{Reason: reason} }

// DenyToolFree is DenyTool for a refusal that doesn't use up a step. See
// ToolDeniedError.Free.
func DenyToolFree(reason string) error { return &ToolDeniedError{Reason: reason, Free: true} }

// freeDenial reports whether err is a denial marked Free.
func freeDenial(err error) bool {
	var d *ToolDeniedError
	return errors.As(err, &d) && d.Free
}

// FreeResultError is an answer to a tool call that costs the turn no step
// (#316). Imagine a model that calls a tool with arguments it already tried.
// A middleware or the tool answers it with the earlier result, and the
// repeat shouldn't use up a step. A free denial would cost nothing too, but
// the model would read "not permitted" and may stop calling the tool.
//
// The model reads Text as the call's result, with no prefix. Surfaces get
// EventToolEnd with Text as the result, as for any call that ran. The step
// counts as free under the same rule as ToolDeniedError.Free. Every call in
// the step has to be answered or refused free, and the turn has to be under
// RunnerConfig.MaxFreeRefusals. Past the cap the model still reads Text, and
// the step counts.
//
// It travels as an error, so a middleware can return it without calling
// next, and an AddFunc or AddAsyncFunc handler can return it from its
// (string, error) result. It may be wrapped with %w.
type FreeResultError struct {
	// Text is what the model reads as the result.
	Text string
	// Reason says why the call was free. It goes on EventRefusalFree. Empty
	// means "free result".
	Reason string
}

func (e *FreeResultError) Error() string { return "free result: " + e.reason() }

func (e *FreeResultError) reason() string {
	if e.Reason == "" {
		return "free result"
	}
	return e.Reason
}

// FreeResult builds the error a middleware or a tool returns to answer a
// call with text that costs no step. See FreeResultError.
func FreeResult(text string) error { return &FreeResultError{Text: text} }

// passThrough reports whether a FuncSource handler's error is a decision for
// the Runner, a denial or a free result, rather than a failure to show the
// model.
func passThrough(err error) bool {
	var d *ToolDeniedError
	var f *FreeResultError
	return errors.As(err, &d) || errors.As(err, &f)
}

// deniedReason reports whether err is a denial and the reason to surface.
func deniedReason(err error) (string, bool) {
	var d *ToolDeniedError
	if !errors.As(err, &d) {
		return "", false
	}
	if d.Reason == "" {
		return "denied by policy", true
	}
	return d.Reason, true
}

// deniedModelReason returns what the model is told about a denial. That is
// ModelReason when the middleware set one for the model, and otherwise the
// surfaced reason. See ToolDeniedError.
func deniedModelReason(err error, surfaced string) string {
	var d *ToolDeniedError
	if errors.As(err, &d) && d.ModelReason != "" {
		return d.ModelReason
	}
	return surfaced
}

// chainToolMiddleware wraps base so mw[0] is outermost and the last entry
// wraps base itself.
func chainToolMiddleware(mw []ToolMiddleware, base ToolCallFunc) ToolCallFunc {
	for i := len(mw) - 1; i >= 0; i-- {
		next, m := base, mw[i]
		base = func(ctx context.Context, info ToolCallInfo) (*core.ToolResult, error) {
			return m(ctx, info, next)
		}
	}
	return base
}

// BeforeToolCall adapts a plain inspect-or-rewrite function into middleware,
// for the common case that does not need to see the result. Returning an
// error refuses the call. Use DenyTool for an intentional refusal. Returning
// nil continues with whatever the function left in info.
func BeforeToolCall(fn func(ctx context.Context, info *ToolCallInfo) error) ToolMiddleware {
	return func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
		if err := fn(ctx, &info); err != nil {
			return nil, err
		}
		return next(ctx, info)
	}
}

// AfterToolCall adapts a plain result-transforming function into middleware.
// A redaction or an untrusted-output marker takes this shape. fn runs only
// when the call succeeded. A failed or denied call has no result to
// transform, and its error passes straight through.
func AfterToolCall(fn func(ctx context.Context, info ToolCallInfo, res *core.ToolResult) (*core.ToolResult, error)) ToolMiddleware {
	return func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
		res, err := next(ctx, info)
		if err != nil {
			return nil, err
		}
		out, ferr := fn(ctx, info, res)
		if ferr != nil {
			return nil, fmt.Errorf("tool middleware: %w", ferr)
		}
		if out != nil {
			return out, nil
		}
		return res, nil
	}
}
