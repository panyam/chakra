package host

import (
	"context"
	"fmt"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Extension contributes capability to an App across every seam at once.
//
// It exists because App had no way to *add* anything. Every other AppOption
// replaces something already pluggable, such as the provider, the stores, the
// observer or the prompt builder. Imagine a caller who wants their own tools
// plus the harness behaviour around them. No option took that, so App grew a
// feature per field instead of a registry per seam.
//
// Most of this is not a new mechanism. ToolSource, ToolMiddleware,
// PromptSection, Command, and ContextStage all already existed and were
// already wired. An Extension lets one package contribute across them as a
// unit. A feature is one thing with several facets, and the facets have to
// agree. Memory's recall tool and its pre-turn producer share a store. A
// checkpoint middleware and its /undo command share a snapshot directory. A
// file tool and its approval text describe the same operation. Registering
// those separately would leave the caller to keep them coherent, and nothing
// would state their ordering.
//
// Building real extensions showed two gaps in the contract, so two seams were
// added. Checkpoint needed TurnStart, and no contribution seam could express
// it. The coding surface needed ApprovalRenderers, because a caller could
// reach every part of the approval ladder except the ask text.
//
// Every method except Name is optional. Return nil for the seams you do not
// use. Embed BaseExtension to no-op all of them and override only what you
// need.
//
// # Ordering
//
// Extensions are applied in the order given to WithExtension, and within one
// extension each seam keeps the order its slice declares. Three of these
// orderings matter.
//
//   - Middleware runs before the host's own permission gate, so a gate still
//     sees the arguments an extension rewrote. See
//     agent.RunnerConfig.ToolMiddleware, where a gate belongs last.
//   - Context stages append after the built-in producers, so an extension's
//     block sits closer to the user's message than the memory summary. Later
//     is more salient; see contextPipeline.
//   - Input transforms run before the user message is built, each over the
//     previous one's output, so context stages sit beside the expanded text.
//
// # What the host owns
//
// The permission gate is not an extension and should not become one. It must
// run last, so that it decides on the arguments after every other middleware
// has rewritten them. Suppose an extension could claim that slot. It could
// then take the slot from the gate, and registration order would decide what
// runs last. Nobody should be able to change that ordering. So the host wires
// TieredApproval itself, after extension middleware. An extension that wants
// to refuse a call returns agent.DenyTool from its own middleware instead.
//
// # Naming
//
// Tools register into the same MultiSource as everything else. When a tool
// name collides with another source's, the existing disambiguation resolves
// it, and this file adds no rule of its own. Commands collide loudly.
// Registering a name the registry already holds is an error, because a
// silently shadowed /command is worse than a failed startup.
type Extension interface {
	// Name identifies the extension in errors, diagnostics, and stage names.
	// It is also the source id its tools register under, so it must be unique
	// across the extensions given to one App.
	Name() string

	// Tools returns the extension's tool source, or nil for none. An error
	// fails App construction, so an App never starts half-configured with an
	// extension that couldn't build its tools.
	Tools() (agent.ToolSource, error)

	// Middleware wraps tool dispatch. Applied before the host's permission
	// gate; see the ordering note above.
	Middleware() []agent.ToolMiddleware

	// PromptSections contribute to the per-turn system prompt, appended after
	// the host's own sections.
	PromptSections() []PromptSection

	// Commands are slash commands every surface dispatches. A name already in
	// the registry is an error.
	Commands() []*Command

	// ContextStages are per-turn context producers, appended to the transient
	// phase. Use this for context the model should see for one turn without
	// it joining the session's history.
	ContextStages() []ContextStage

	// StepStages run before every model call of the main agent, after the
	// host's own stages, ordered by scope as agent.StepStage describes. Use
	// one when the context must track what happens inside a turn, such as a
	// file the last tool call surfaced. A ContextStage runs once per turn, so
	// it only sees what was true when the turn began. Pick the scope by how
	// long the added context should live. See docs/HOOKS.md, Record or view.
	StepStages() []agent.StepStage

	// TurnEndHooks judge the main agent's answer before the turn accepts it,
	// in registration order. The first refusal
	// wins. A hook can accept, send the model back with a note, or stop the
	// turn on the refused answer with agent.ErrTurnStopped. The host keeps a
	// stopped turn in history, as it keeps a step-capped one. Use one when
	// the extension can tell the work isn't done, such as tests that are
	// still red. Sub-agents don't run these hooks. TurnEnder only hears that a
	// turn ended and can't change it. See agent.TurnEndHook and docs/HOOKS.md,
	// Turn end.
	TurnEndHooks() []agent.TurnEndHook

	// ApprovalRenderers supply the question a user is asked before a gated
	// tool call runs, for the tools this extension owns.
	//
	// A caller can configure the mode, per-tool rules, remembering and the ask
	// transport. Before this seam, the wording of the ask was fixed. That
	// works until the user has to review the tool's arguments themselves. For
	// an edit, the default renders a JSON blob trimmed to 200 characters. A
	// real change doesn't fit in that, so the user answers a question they
	// can't actually evaluate.
	//
	// Consulted in registration order; the first renderer to claim a call
	// wins, and the built-in format is the fallback. See ApprovalRenderer.
	ApprovalRenderers() []ApprovalRenderer

	// ApprovalChoosers answer an ask about a call with more than yes or no, for
	// the tools this extension owns. For example, a chooser can let the user
	// approve an edit's hunks one at a time. A chooser that claims a call
	// replaces the yes/no prompt for it, so the user is still asked once, and
	// the call that runs is narrowed to what they approved. The gate keeps its
	// place after every middleware; the chooser runs inside it. See
	// ApprovalChooser.
	ApprovalChoosers() []ApprovalChooser

	// ApprovalClassifiers judge each call to the tools this extension owns from
	// its arguments. A verdict says whether the call is read-only, reversible,
	// destructive or forbidden, which remember key an approval of it covers,
	// and why. The gate consults them before anything else, so one tool can let
	// "git status" through while asking about "git push". Consulted in
	// registration order; the first to claim a call wins. See
	// ApprovalClassifier.
	ApprovalClassifiers() []ApprovalClassifier

	// InputTransforms rewrite the user's message before the turn is built
	// (issue 27), in registration order, each over the previous one's output.
	// History keeps the result as the user message, and the model reads it. A
	// ContextStage can't do this, since it adds a block beside the message
	// rather than changing it. An error aborts the turn before any model call.
	InputTransforms() []InputTransform

	// TurnStart runs once at the top of every turn, before any context stage
	// and before history is touched, so a failure leaves nothing half-done.
	// An error aborts the turn.
	//
	// The other seams are all contributions, and none of them is a lifecycle
	// hook. Some extensions keep state scoped to a turn, such as a checkpoint
	// to restore to, a per-turn budget or a counter. Without this seam they
	// had nowhere to learn that a turn had begun. The nearest workaround was a
	// ContextStage that produces no context and exists for its side effect.
	// A stage that lies about being a producer is worse than a seam that
	// admits what it is.
	//
	// Proactive turns, such as a trigger firing, call this too. They run the
	// model over history exactly as a user turn does, so an extension that
	// skipped them would hold state scoped to the wrong thing.
	TurnStart(ctx context.Context) error

	// Close releases what the extension owns, and is called by App.Close in
	// reverse registration order so an extension is torn down before whatever
	// it was registered after.
	//
	// TurnStart had no counterpart until an extension owned something that
	// the runtime could not clean up for it. A directory handle survives being
	// forgotten, and a snapshot directory is meant to outlive the process, so
	// the first two extensions needed nothing here. A subprocess is different.
	// ext/lsp spawns a language server. If the server outlives the App, it
	// holds a workspace lock and memory that nothing accounts for.
	//
	// Errors are collected and reported rather than aborting the sweep, since
	// one extension that cannot close is not a reason to leak the rest.
	Close() error
}

// Announcer is an optional interface an Extension implements to post
// informational lines to every surface, shown where the user reads the
// conversation (a HostMessage event). The host calls SetAnnounce once, while
// applying extensions and before any turn, with a function that is safe to
// call from any goroutine. Use it for what the user should know happened and
// the model's context doesn't show them, such as which instruction files
// were loaded.
type Announcer interface {
	SetAnnounce(announce func(message string))
}

// RunRestorer is an optional interface an Extension implements to rebuild
// state it keeps for a session when the session switches runs. Imagine slots
// written during yesterday's turns (#279). The messages come back with
// Resume, but the slots live in the extension, so it reads them back from the
// run's event log. The host calls RestoreRun with the loaded run on Resume,
// on AttachRun, and on a Fork that rewinds, and with an empty run when
// AttachRun creates one. Turn locks are held, so no turn runs meanwhile.
//
// A Fork from an earlier message copies no events (see
// agent.ForkRunRequest), so the rewound run restores with none.
type RunRestorer interface {
	RestoreRun(run agent.Run)
}

// ProcessReporter is an optional interface an Extension implements to show
// the output of processes its tools started while they run, such as a dev
// server between the model's polls. The host calls SetProcessOutput once,
// while applying extensions and before any turn, with a function that is safe
// to call from any goroutine; each call becomes a HostProcessOutput event.
// It reaches the surfaces only, never the model's context, so a tool keeps
// returning the output it collects as before.
type ProcessReporter interface {
	SetProcessOutput(report func(ProcessOutput))
}

// ProcessController is an optional interface an Extension implements so a
// surface can drive its processes directly: resize a terminal to fit the
// view, or type into one (#218). The id is ProcessOutput.ID.
//
// These calls come from the user through a surface, not from the model. So
// they don't pass the approval gate, and what is typed doesn't enter the
// model's context. The model sees the process's output when it next reads
// it, as it would after any other input.
type ProcessController interface {
	// ResizeProcess sets a terminal process's size. A process that isn't a
	// terminal, or has exited, is an error.
	ResizeProcess(id string, cols, rows int) error
	// WriteProcess sends data to a running process's input.
	WriteProcess(id, data string) error
}

// TurnEnder is an optional interface an Extension implements to hear that a
// turn the host ran is over, whether it finished, stopped partway (the step cap
// or the context budget) or failed. result is nil when the turn failed
// outright, and err is nil when it finished. The host calls it with the turn
// lock held, after the turn's messages are kept and before HostTurnDone or
// HostTurnFailed. So if an Announcer posts a HostMessage from it, the message
// reaches the surface before the end-of-turn marker. Use it for work bracketed
// by TurnStart, such as reporting what changed on disk during the turn; it
// can't change the turn.
type TurnEnder interface {
	TurnEnd(ctx context.Context, result *agent.TurnResult, err error)
}

// SubAgentUser is an optional interface for an Extension whose tools run a
// sub-agent of their own, such as a review over a diff. The host calls
// UseSubAgents once, while applying extensions and before asking for the
// extension's tools, with what a child agent needs from the session. A child
// built from it renders on every surface like the host's own personas.
type SubAgentUser interface {
	UseSubAgents(SubAgentHost)
}

// SubAgentHost is what the host lends an extension's sub-agents.
type SubAgentHost struct {
	// Provider is the session's provider. With a connections registry it's
	// the runtime switch, so a child follows /provider like the main agent.
	Provider agent.Provider
	// ReadOnlyTools is the session's tool set narrowed to tools that declare
	// readOnlyHint, listed at call time, so it includes extensions applied
	// after this one. Calls go straight to the tools, outside the main
	// agent's middleware and permission gate, so the set holds only tools
	// that declare they change nothing.
	ReadOnlyTools agent.ToolSource
	// OnEvent forwards a child's events to the surfaces as HostSubAgentEvent.
	// Pass it as AgentSourceConfig.OnEvent.
	OnEvent func(agent.SubAgentEvent)
}

// Asker is an optional interface an Extension implements to ask the user a
// yes/no question outside a tool call, such as whether a command may reach a
// host its allowlist doesn't name. The host calls SetAsk once, while applying
// extensions, with a function that puts the question through the same queue
// as approval asks, so it never overlaps another dialog. A surface with
// nobody to answer (headless exec) answers no.
type Asker interface {
	SetAsk(ask func(ctx context.Context, question string) (bool, error))
}

// BaseExtension implements every optional Extension seam as a no-op, so an
// extension embeds it and overrides only what it contributes. It deliberately
// does not supply Name, because a caller never means to register an unnamed
// extension.
//
//	type coding struct{ host.BaseExtension }
//
//	func (coding) Name() string { return "coding" }
//	func (c coding) Tools() (agent.ToolSource, error) { return c.src, nil }
type BaseExtension struct{}

func (BaseExtension) Tools() (agent.ToolSource, error)      { return nil, nil }
func (BaseExtension) Middleware() []agent.ToolMiddleware    { return nil }
func (BaseExtension) PromptSections() []PromptSection       { return nil }
func (BaseExtension) Commands() []*Command                  { return nil }
func (BaseExtension) ContextStages() []ContextStage         { return nil }
func (BaseExtension) StepStages() []agent.StepStage         { return nil }
func (BaseExtension) TurnEndHooks() []agent.TurnEndHook     { return nil }
func (BaseExtension) ApprovalRenderers() []ApprovalRenderer { return nil }
func (BaseExtension) ApprovalChoosers() []ApprovalChooser   { return nil }
func (BaseExtension) ApprovalClassifiers() []ApprovalClassifier {
	return nil
}
func (BaseExtension) InputTransforms() []InputTransform { return nil }
func (BaseExtension) TurnStart(context.Context) error   { return nil }
func (BaseExtension) Close() error                      { return nil }

// ApprovalRenderer turns a pending tool call into the question a user is
// asked about it, or declines to.
//
// The bool is "this call is mine". False means the next renderer gets a look
// and the built-in format is the fallback, so a renderer can claim a subset of
// its own tools, or claim conditionally, without having to reproduce the
// default for everything else. There is no error return on purpose. A
// renderer that cannot do its job returns false, and the user still gets
// asked. At an approval prompt, asking anyway is the only useful behaviour.
//
// The info holds the call as it WILL execute. Every middleware has already
// rewritten its arguments (#1248), so the user approves the call that runs,
// not the one the model proposed. A test pins this rather than a comment.
//
// Truncation is the renderer's business. The default trims arguments so a
// large payload cannot flood the prompt. A renderer showing a diff wants the
// opposite and is trusted to decide.
//
// The context is the turn's. A renderer uses it to read state beyond what the
// arguments hold, so the question it renders is honest. For example, to show
// what a whole-file write would replace, it reads the file that is there now.
type ApprovalRenderer func(context.Context, agent.ToolCallInfo) (string, bool)

// ApprovalChooser decides a pending call that would otherwise get a yes/no
// ask, and may approve only part of it.
//
// The bool in agent.Choice.Claimed works like ApprovalRenderer's: false passes
// the call to the next chooser, and the plain ask is the fallback. A chooser
// presents its own question through present. The plain ask uses the same
// serialized elicitation seam, so the chooser's form waits its turn behind
// any other dialog and reaches every surface the same way.
//
// A surface that only shows accept and decline answers a form with no content.
// A chooser should read missing fields as approved, since the user pressed
// accept on the whole call.
//
// The info carries the call as it will execute, after every middleware, and
// the chooser narrows that call.
type ApprovalChooser func(ctx context.Context, info agent.ToolCallInfo, present agent.ElicitationUI) (agent.Choice, error)

// ApprovalClassifier classifies one pending call from its arguments, or
// declines to. The bool is "this call is mine", as with ApprovalRenderer; a
// classifier should claim only its own extension's tools, because the gate
// trusts its verdict, including a read-only verdict on a tool annotated
// destructive.
//
// The info carries the call as it will execute, after every middleware, so a
// rewrite can't turn a classified-safe call into something else. See
// agent.ClassifyFunc for what each field of the verdict does at the gate.
type ApprovalClassifier func(ctx context.Context, info agent.ToolCallInfo) (agent.CallClass, bool)

// InputTransform rewrites the user's input into the message a turn is built
// from. An error aborts the turn before history is touched or the model is
// called, so a transform that can't do its job (a referenced file is missing)
// leaves the session as it was rather than half-expanded.
type InputTransform func(ctx context.Context, input string) (string, error)

// WithExtension registers extensions with the App, applied in the order
// given. Repeated calls accumulate rather than replace, so a caller can build
// the list across several options.
func WithExtension(exts ...Extension) AppOption {
	return func(o *appOptions) { o.extensions = append(o.extensions, exts...) }
}

// renderApproval produces the question to ask about a pending call, giving
// each registered renderer a look before falling back to the built-in format.
//
// The first claim wins, in registration order. Middleware is ordered the same
// way. Commands differ, because a command's name is known at registration and
// a duplicate can be refused then. A renderer decides per call, so two
// renderers claiming the same call can't be detected until it happens. Failing
// a turn at that point would be worse than picking the first.
//
// A renderer that returns an empty string is treated as having declined,
// because an empty approval prompt asks the user to confirm nothing.
func (a *App) renderApproval(ctx context.Context, info agent.ToolCallInfo) string {
	if text, ok := a.renderSkillLoad(info); ok {
		return text
	}
	for _, r := range a.approvalRenderers {
		if r == nil {
			continue
		}
		if text, ok := r(ctx, info); ok && text != "" {
			return text
		}
	}
	return approvalPrompt(info)
}

// chooseApproval gives each registered chooser a look at a pending call, in
// registration order, and returns the first claim. No claim leaves the call to
// the plain yes/no ask.
func (a *App) chooseApproval(ctx context.Context, info agent.ToolCallInfo, present agent.ElicitationUI) (agent.Choice, error) {
	for _, c := range a.approvalChoosers {
		if c == nil {
			continue
		}
		choice, err := c(ctx, info, present)
		if err != nil || choice.Claimed {
			return choice, err
		}
	}
	return agent.Choice{}, nil
}

// classifyApproval gives each registered classifier a look at a pending call,
// in registration order, and returns the first claim.
func (a *App) classifyApproval(ctx context.Context, info agent.ToolCallInfo) (agent.CallClass, bool) {
	if class, ok := a.classifySkillLoad(info); ok {
		return class, true
	}
	for _, c := range a.approvalClassifiers {
		if c == nil {
			continue
		}
		if class, ok := c(ctx, info); ok {
			return class, true
		}
	}
	return agent.CallClass{}, false
}

// startExtensionTurns runs every extension's TurnStart, in registration
// order, stopping at the first error.
//
// Called with turnMu held and before history is touched, so an extension that
// cannot start its turn aborts before anything is half-applied. A failure
// names the extension, so a turn that dies on someone else's bookkeeping says
// whose.
func (a *App) startExtensionTurns(ctx context.Context) error {
	for _, ext := range a.extensions {
		if err := ext.TurnStart(ctx); err != nil {
			return fmt.Errorf("host: extension %q turn start: %w", ext.Name(), err)
		}
	}
	return nil
}

// endExtensionTurns tells every extension that implements TurnEnder that the
// turn is over, in registration order. Called with turnMu held.
func (a *App) endExtensionTurns(ctx context.Context, result *agent.TurnResult, err error) {
	for _, ext := range a.extensions {
		if te, ok := ext.(TurnEnder); ok {
			te.TurnEnd(ctx, result, err)
		}
	}
}

// transformInput runs every extension's input transforms over the user's
// message, in registration order, each receiving the previous one's output.
// It stops at the first error, which aborts the turn.
//
// Called with turnMu held, after TurnStart. A failure names the extension, as
// startExtensionTurns does, and the transforms after it don't run, so one
// with a side effect can't act on a turn that is being abandoned.
func (a *App) transformInput(ctx context.Context, input string) (string, error) {
	for _, ext := range a.extensions {
		for _, fn := range ext.InputTransforms() {
			if fn == nil {
				continue
			}
			out, err := fn(ctx, input)
			if err != nil {
				return "", fmt.Errorf("host: extension %q input transform: %w", ext.Name(), err)
			}
			input = out
		}
	}
	return input, nil
}

// closeExtensions closes every extension, in reverse registration order.
//
// Registration order is dependency order everywhere else. An extension
// registered later was built knowing the earlier ones were there.
// surfaces.WorkspaceExtensions relies on this when it puts checkpoint ahead
// of the tools it snapshots. Tearing down forwards would close a dependency
// out from under its dependent, so the order is reversed.
//
// Every extension is closed even after one fails. The failures are logged
// rather than returned, because App.Close is the last thing a surface does and
// has nowhere to report to. One extension that cannot close is not a reason to
// leak the others.
//
// Each extension is closed exactly once, even if App.Close is called twice.
// Surfaces do that by hand often, with a deferred Close plus an explicit one
// on a shutdown path. If every implementation had to guard against it, nobody
// could write Close simply.
func (a *App) closeExtensions() {
	exts := a.extensions
	a.extensions = nil
	for i := len(exts) - 1; i >= 0; i-- {
		if err := exts[i].Close(); err != nil && a.log != nil {
			a.log.Warn("extension close failed", "extension", exts[i].Name(), "err", err)
		}
	}
}

// applyExtensions wires every extension into the seams it contributes to.
//
// Called during construction. The built-in producers already exist, so an
// extension's contributions go after theirs. The Runner isn't built yet, so
// extension middleware reaches RunnerConfig. Middleware is returned rather
// than applied, because the caller must still append the permission gate
// after it.
func (a *App) applyExtensions(exts []Extension, provider agent.Provider) ([]agent.ToolMiddleware, error) {
	var mw []agent.ToolMiddleware
	seen := map[string]bool{}
	a.extensions = exts
	for _, ext := range exts {
		name := ext.Name()
		if name == "" {
			return nil, fmt.Errorf("host: extension has no name")
		}
		if seen[name] {
			return nil, fmt.Errorf("host: duplicate extension %q", name)
		}
		seen[name] = true

		if su, ok := ext.(SubAgentUser); ok {
			su.UseSubAgents(SubAgentHost{
				Provider: provider,
				ReadOnlyTools: agent.NewFilterSource(a.sources, func(d core.ToolDef) bool {
					ro, _ := d.Annotations["readOnlyHint"].(bool)
					return ro
				}),
				OnEvent: func(e agent.SubAgentEvent) { a.emit(HostEvent{Kind: HostSubAgentEvent, SubAgent: e}) },
			})
		}

		src, err := ext.Tools()
		if err != nil {
			return nil, fmt.Errorf("host: extension %q tools: %w", name, err)
		}
		if src != nil {
			// Deliberately not recorded as operator. An extension is
			// arbitrary code that may shell out or fetch, so the host has no
			// standing to vouch for its output; config must say so
			// explicitly. See derivedProvenance.
			if err := a.sources.Add(name, src); err != nil {
				return nil, fmt.Errorf("host: extension %q tools: %w", name, err)
			}
		}

		mw = append(mw, ext.Middleware()...)
		a.promptBuilder.Sections = append(a.promptBuilder.Sections, ext.PromptSections()...)
		a.context.transient = append(a.context.transient, ext.ContextStages()...)
		a.stepStages = append(a.stepStages, ext.StepStages()...)
		a.turnEndHooks = append(a.turnEndHooks, ext.TurnEndHooks()...)
		if ak, ok := ext.(Asker); ok && a.confirm != nil {
			ak.SetAsk(a.confirm)
		}
		if an, ok := ext.(Announcer); ok {
			an.SetAnnounce(func(msg string) { a.emit(HostEvent{Kind: HostMessage, Message: msg}) })
		}
		if pr, ok := ext.(ProcessReporter); ok {
			pr.SetProcessOutput(func(o ProcessOutput) { a.emit(HostEvent{Kind: HostProcessOutput, ProcessOutput: &o}) })
		}
		a.approvalRenderers = append(a.approvalRenderers, ext.ApprovalRenderers()...)
		a.approvalChoosers = append(a.approvalChoosers, ext.ApprovalChoosers()...)
		a.approvalClassifiers = append(a.approvalClassifiers, ext.ApprovalClassifiers()...)

		for _, cmd := range ext.Commands() {
			// Register overwrites silently, which is the wrong behaviour for
			// a command an extension did not know was taken: a shadowed
			// /command fails at use, far from the cause. Refuse instead.
			if _, taken := a.commands.Lookup(cmd.Name); taken {
				return nil, fmt.Errorf("host: extension %q command %q is already registered", name, cmd.Name)
			}
			a.commands.Register(cmd)
		}
	}
	return mw, nil
}
