package agent

import (
	"context"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// Approval is one ToolMiddleware among others rather than a mechanism of its
// own. A permission gate is middleware that declines to call next; that it
// asks a human first is an implementation detail, not a different kind of
// interception. See ToolMiddleware for the ordering contract, and
// RunnerConfig.ToolMiddleware for why a gate belongs last in the chain.

// ApprovalMode is the default disposition TieredApproval applies to a call
// that no per-tool rule covers.
type ApprovalMode int

const (
	// ModeAlwaysAsk asks for every uncovered call. The safe default.
	ModeAlwaysAsk ApprovalMode = iota
	// ModeReadOnlyAuto auto-allows calls whose tool declares readOnlyHint
	// and asks for the rest. The "read-only → auto-edit" rung of the ladder.
	ModeReadOnlyAuto
	// ModeReversibleAuto auto-allows a call that reads, and a call that
	// writes something the tool declares it can undo, asking only when the
	// effect is irreversible. The rung above ModeReadOnlyAuto: it separates
	// "does this write?" from "can this be taken back?", so an editing tool
	// stops prompting while a send or a delete still does.
	//
	// It keys on ToolCallInfo.Destructive, so a tool that annotates nothing
	// is asked about rather than assumed reversible. Against servers that
	// skip destructiveHint entirely this behaves exactly like ModeAlwaysAsk,
	// which is the intended failure direction.
	ModeReversibleAuto
	// ModeAlwaysAllow runs every uncovered call without asking (full-auto /
	// "yolo"). Per-tool Deny rules still apply on top.
	ModeAlwaysAllow
)

// ToolRule is the per-tool override that takes precedence over the mode.
type ToolRule int

const (
	// RuleAsk forces an ask for this tool regardless of mode.
	RuleAsk ToolRule = iota
	// RuleAllow auto-allows this tool regardless of mode.
	RuleAllow
	// RuleDeny refuses this tool outright, without asking.
	RuleDeny
)

// AskFunc presents a yes/no approval prompt and returns the user's choice.
// ElicitationCoordinator.Confirm satisfies it, which is how the "ask" outcome
// reuses the existing FIFO UI seam instead of introducing a second one. A nil
// AskFunc makes every ask resolve to a refusal (fail-closed).
type AskFunc func(ctx context.Context, info ToolCallInfo) (bool, error)

// ChooseFunc answers an ask with more than yes or no: it may approve part of
// a call, such as some of an edit's hunks. It runs where the AskFunc would and
// replaces it for any call it claims, so the user is still asked once. It does
// its own presenting, which is why it is a separate seam rather than a richer
// AskFunc: the gate cannot know what a "part" of an arbitrary call is.
type ChooseFunc func(ctx context.Context, info ToolCallInfo) (Choice, error)

// Choice is a ChooseFunc's answer about one call.
type Choice struct {
	// Claimed false means the chooser has nothing to say about this call, and
	// the gate falls back to the AskFunc. The other fields are ignored.
	Claimed bool

	// Approved false declines the whole call, as a refused ask does.
	Approved bool

	// Args, when non-empty, replaces the call's arguments with the part the
	// user approved. Only the arguments can change. The tool and call ID stay
	// what the model asked for, so a chooser can narrow a call but not turn
	// it into a different one.
	Args core.RawJSON

	// Note is appended to the tool result as a text block, so the model can
	// tell the user what was left out ("hunk 2 declined by the user") instead
	// of reporting the narrowed call as if it were the one it made.
	Note string
}

// CallDisposition is how much one call can change, as the tool that owns it
// judges from the call's arguments.
type CallDisposition int

const (
	// DispositionUnknown means the classifier has no opinion about the
	// effect, and the tool's annotations decide it as before. A classifier can
	// still claim the call to name its remember key and reason.
	DispositionUnknown CallDisposition = iota
	// DispositionReadOnly means the call changes nothing. It auto-allows under
	// ModeReadOnlyAuto even when the tool as a whole is destructive, which is
	// the point: "git status" through a tool that can also run "git push".
	DispositionReadOnly
	// DispositionReversible means the call writes something that can be
	// undone, so ModeReversibleAuto allows it.
	DispositionReversible
	// DispositionDestructive means the call's effect is hard to take back. It
	// asks under every mode but ModeAlwaysAllow.
	DispositionDestructive
	// DispositionForbidden refuses the call without asking. It wins over a
	// remembered approval, a RuleAllow and ModeAlwaysAllow, so no setting can
	// turn it into a yes.
	DispositionForbidden
	// DispositionAlwaysAsk asks a human under every mode, ModeAlwaysAllow
	// included, and is never remembered: each call gets its own answer. It is
	// for a call that steps outside what the operator set up, such as running
	// a command outside the sandbox. RuleDeny and a forbidden verdict still
	// refuse it, and with no AskFunc it is refused like any other ask.
	DispositionAlwaysAsk
)

// CallClass is a classifier's verdict on one call.
type CallClass struct {
	// Disposition is the call's effect, or DispositionUnknown to leave that to
	// the tool's annotations.
	Disposition CallDisposition

	// Key names what approving this call covers, for the remember cache, such
	// as a command prefix ("git status"). It is scoped to the tool, so two
	// tools can't share an approval through equal keys. Empty means the call
	// is never remembered: falling back to the tool name would let one
	// approved "rm foo" cover every later rm.
	Key string

	// Reason is a short phrase saying why, such as "recursive delete". It is
	// the denial reason for a forbidden call and is available to renderers
	// through ToolCallInfo.Class for an ask.
	Reason string
}

// ClassifyFunc classifies one call from its arguments. The bool is "this call
// is mine"; false leaves the call to the tool's annotations and the default
// remember key, exactly as if no classifier were set.
//
// It runs inside the gate, on the arguments as they will execute, after every
// earlier middleware has rewritten them. Classifying before the chain would
// let a rewrite from a harmless call to a destructive one go unexamined. A
// classifier is trusted to judge the tools it claims, so it should claim only
// its own.
type ClassifyFunc func(ctx context.Context, info ToolCallInfo) (CallClass, bool)

// TieredApproval is the batteries-included permission gate: a default mode, a
// map of per-tool rules that override it, an optional ask seam, and an
// optional session-scoped cache that remembers a tool the user approved so
// later calls to it skip the prompt. Safe for concurrent use by the Runner's
// parallel dispatch.
type TieredApproval struct {
	mode     ApprovalMode
	rules    map[string]ToolRule
	ask      AskFunc
	choose   ChooseFunc
	remember bool
	key      RememberKeyFunc
	classify ClassifyFunc

	mu         sync.Mutex
	remembered map[string]bool
}

// TieredOption configures a TieredApproval.
type TieredOption func(*TieredApproval)

// WithDefaultMode sets the disposition for calls no per-tool rule covers.
// Without it, the mode is ModeAlwaysAsk.
func WithDefaultMode(m ApprovalMode) TieredOption {
	return func(t *TieredApproval) { t.mode = m }
}

// WithToolRule pins a per-tool override that wins over the mode. Call it once
// per tool; a later rule for the same name replaces an earlier one.
func WithToolRule(tool string, rule ToolRule) TieredOption {
	return func(t *TieredApproval) { t.rules[tool] = rule }
}

// WithAsk supplies the seam that presents an approval prompt. Pass
// coord.Confirm to route asks through the shared ElicitationCoordinator.
func WithAsk(ask AskFunc) TieredOption {
	return func(t *TieredApproval) { t.ask = ask }
}

// WithChooser supplies the seam that can approve part of a call. A call it
// claims is decided by it alone; everything else still goes to the AskFunc.
func WithChooser(choose ChooseFunc) TieredOption {
	return func(t *TieredApproval) { t.choose = choose }
}

// RememberKeyFunc names what an approval of this call covers, for the
// remember cache. ok false means the call can't be remembered at all, so the
// user is asked every time. The default key is the tool name, which is right
// for a tool whose arguments don't change what it does to trust. A tool whose
// arguments name content that can change under the same name, such as a skill
// whose files rotate, needs a key that changes with the content, or one
// approval would cover whatever is served later.
type RememberKeyFunc func(info ToolCallInfo) (key string, ok bool)

// WithRememberKey sets how remembered approvals are keyed. Nil keeps the
// default, the tool name.
func WithRememberKey(fn RememberKeyFunc) TieredOption {
	return func(t *TieredApproval) { t.key = fn }
}

// WithClassifier sets the per-call classifier the gate consults before
// anything else. Nil keeps every call's disposition at its tool's annotations.
func WithClassifier(fn ClassifyFunc) TieredOption {
	return func(t *TieredApproval) { t.classify = fn }
}

// rememberKey is the cache key for info, or ok false when it can't be cached.
// A classified call is keyed by its tool and the classifier's key, and only by
// those; RememberKeyFunc is for calls no classifier claimed.
func (t *TieredApproval) rememberKey(info ToolCallInfo) (string, bool) {
	if info.Class != nil {
		if info.Class.Key == "" || info.Class.Disposition == DispositionAlwaysAsk {
			return "", false
		}
		return info.Call.Name + "\x00" + info.Class.Key, true
	}
	if t.key == nil {
		return info.Call.Name, true
	}
	return t.key(info)
}

// WithRememberApprovals turns on the session cache: once the user approves a
// tool through an ask, subsequent calls to that same tool auto-allow for the
// life of this policy. A denial is never remembered.
func WithRememberApprovals(remember bool) TieredOption {
	return func(t *TieredApproval) { t.remember = remember }
}

// NewTieredApproval builds a TieredApproval. With no options it asks for every
// call and, lacking an AskFunc, refuses them all (fail-closed) — supply
// WithAsk to make asking meaningful.
func NewTieredApproval(opts ...TieredOption) *TieredApproval {
	t := &TieredApproval{
		mode:       ModeAlwaysAsk,
		rules:      map[string]ToolRule{},
		remembered: map[string]bool{},
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// SetDefaultMode changes the default mode at runtime (the seam a host's
// "/approve <mode>" or "/yolo" command uses). Concurrency-safe against calls
// in flight; per-tool rules and the remember-cache are unaffected.
func (t *TieredApproval) SetDefaultMode(m ApprovalMode) {
	t.mu.Lock()
	t.mode = m
	t.mu.Unlock()
}

// DefaultMode reports the current default mode.
func (t *TieredApproval) DefaultMode() ApprovalMode {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mode
}

// WrapToolCall applies, in order: the classifier (a forbidden call is refused
// here), a remembered approval, a per-tool rule, then the default mode, which
// reads the classifier's disposition in place of the tool's annotations. An ask that the user accepts is remembered when the cache
// is on. A refusal (rule Deny, an ask returning false, or an ask with no
// AskFunc wired) denies with a Reason; the Runner feeds that back to the model
// and continues the turn.
//
// It rewrites arguments only when a ChooseFunc narrows a call to the part the
// user approved. Every other rewrite is left to earlier middleware, which is
// what lets this one run last and still see exactly what will execute; the one
// exception is the user's own answer, and what runs is what they chose.
func (t *TieredApproval) WrapToolCall(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
	if t.classify != nil {
		if c, ok := t.classify(ctx, info); ok {
			if c.Disposition == DispositionForbidden {
				reason := "forbidden by policy"
				if c.Reason != "" {
					reason += ": " + c.Reason
				}
				return nil, DenyTool(reason)
			}
			info = info.classified(c)
			if c.Disposition == DispositionAlwaysAsk {
				if r, ok := t.rules[info.Call.Name]; ok && r == RuleDeny {
					return nil, DenyTool("denied by approval policy")
				}
				return t.doAsk(ctx, info, next)
			}
		}
	}

	t.mu.Lock()
	mode := t.mode
	key, keyed := t.rememberKey(info)
	remembered := t.remember && keyed && t.remembered[key]
	t.mu.Unlock()
	if remembered {
		return next(ctx, info)
	}

	if rule, ok := t.rules[info.Call.Name]; ok {
		switch rule {
		case RuleAllow:
			return next(ctx, info)
		case RuleDeny:
			return nil, DenyTool("denied by approval policy")
		case RuleAsk:
			return t.doAsk(ctx, info, next)
		}
	}

	switch mode {
	case ModeAlwaysAllow:
		return next(ctx, info)
	case ModeReadOnlyAuto:
		if info.ReadOnly {
			return next(ctx, info)
		}
		return t.doAsk(ctx, info, next)
	case ModeReversibleAuto:
		if info.ReadOnly || !info.Destructive {
			return next(ctx, info)
		}
		return t.doAsk(ctx, info, next)
	default: // ModeAlwaysAsk
		return t.doAsk(ctx, info, next)
	}
}

func (t *TieredApproval) doAsk(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
	if t.choose != nil {
		c, err := t.choose(ctx, info)
		if err != nil {
			return nil, err
		}
		if c.Claimed {
			return t.doChoice(ctx, info, c, next)
		}
	}
	if t.ask == nil {
		return nil, DenyTool("no approval UI available")
	}
	ok, err := t.ask(ctx, info)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, userDeclined("")
	}
	if t.remember {
		t.mu.Lock()
		if key, ok := t.rememberKey(info); ok {
			t.remembered[key] = true
		}
		t.mu.Unlock()
	}
	return next(ctx, info)
}

// doChoice runs what a ChooseFunc approved. Only a whole approval is
// remembered: a user who accepted part of one call has not agreed to every
// future call to that tool.
func (t *TieredApproval) doChoice(ctx context.Context, info ToolCallInfo, c Choice, next ToolCallFunc) (*core.ToolResult, error) {
	if !c.Approved {
		return nil, userDeclined(c.Note)
	}
	narrowed := len(c.Args.Raw()) > 0
	if narrowed {
		info.Call.Args = c.Args
	} else if t.remember {
		t.mu.Lock()
		if key, ok := t.rememberKey(info); ok {
			t.remembered[key] = true
		}
		t.mu.Unlock()
	}
	res, err := next(ctx, info)
	if err != nil || res == nil || c.Note == "" {
		return res, err
	}
	res.Content = append(res.Content, core.Content{Type: "text", Text: c.Note})
	return res, nil
}

// userDeclined is the denial for a call the user said no to. Surfaces show the
// short reason. The model is told more, because "not permitted: declined by
// user" alone was read as a pending question: in #57's runs the model replied
// "say the word" and only mentioned the decline a turn later.
func userDeclined(note string) error {
	reason := "declined by user"
	if note != "" {
		reason += ": " + note
	}
	return &ToolDeniedError{
		Reason:      reason,
		ModelReason: reason + ". The call did not run. Tell the user plainly that it was declined, and don't ask to run it again unless they bring it up.",
	}
}
