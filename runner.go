package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// defaultMaxSteps bounds a turn when RunnerConfig.MaxSteps is zero. Eight
// model calls is generous for real workflows; hitting it usually means the
// model is looping on a failing tool.
const defaultMaxSteps = 8

// ErrMaxSteps is returned (wrapped) by Run when the model keeps requesting
// tool calls past the step cap. Check with errors.Is.
var ErrMaxSteps = errors.New("agent: max steps exceeded")

// ErrNotAvailableNow is returned by a ToolSource.Call when the tool exists but
// its backing server is unreachable right now. The tool itself didn't fail.
// The Runner treats it as a NON-FATAL miss. It emits EventToolUnavailable,
// feeds the wrapped error's message back to the model, and continues the turn.
// Wrap it with a message that names the server, as in fmt.Errorf("%w: ...",
// ErrNotAvailableNow, ...). The model then knows which server is down and can
// retry, route around it, or tell the user. See docs/AGENT_SERVER_STATE.md.
var ErrNotAvailableNow = errors.New("agent: tool source not available now")

// RunnerConfig assembles a Runner.
type RunnerConfig struct {
	// Provider is the LLM. Required.
	Provider Provider

	// Tools is the tool surface offered to the model. Optional: nil means
	// the model is offered no tools and any hallucinated call fails back
	// into the conversation.
	Tools ToolSource

	// Instructions is the system prompt sent on every step.
	Instructions string

	// InstructionsFunc computes each turn's system prompt. When it is non-nil,
	// the Runner calls it once at the top of each turn, and the result
	// overrides the static Instructions. The prompt can then track dynamic
	// state, such as the date, injected context, or the set of connected
	// servers whose eager skills belong in the prompt. Without it the prompt is
	// frozen at construction. The Runner calls it per turn, not per step. So
	// the prompt doesn't change within a turn, and a provider cache breaks only
	// when the value actually changes.
	//
	// It is per-Runner. Each Runner is built from its own RunnerConfig, so a
	// sub-agent's Runner has its own InstructionsFunc or none, and different
	// runners can produce different prompts. The func captures the context it
	// needs in a closure, for example the host's set of connected servers or
	// its own tool view. The Runner holds this config, so passing the Runner to
	// the func would be circular, and it is deliberately NOT passed. The ctx is
	// for cancellation and deadlines only.
	InstructionsFunc func(context.Context) string

	// MaxSteps caps model calls per turn. Zero means 8.
	MaxSteps int

	// TreeBudget caps the total steps and tokens across the whole sub-agent
	// tree. The tree includes the parent, its sub-agents, fan-out members and
	// handoff rounds. It complements the per-Runner MaxSteps. At the top of a
	// turn the Runner installs it on ctx, but ONLY if the ctx doesn't already
	// have one. So every child run inherits and shares the top-level Runner's
	// budget. Don't set it on sub-agent Runner configs, since they inherit it.
	// Zero in both dimensions means no budget. Setting it is equivalent to
	// calling WithTreeBudget on the turn ctx yourself.
	TreeBudget TreeBudget

	// TracerProvider opts the Runner into SEP 414 span emission. The Runner
	// emits agent.turn per Run, agent.step per model call, and agent.tool per
	// dispatch. It threads ctx through, so client-side dispatch spans nest
	// under these as children, and server spans nest under the dispatch spans.
	// Nil or core.NoopTracerProvider means zero overhead, the repo-wide
	// pattern.
	TracerProvider core.TracerProvider

	// MeterProvider opts the Runner into OTel metric emission (issue 1023),
	// alongside the trace spans. The Runner emits a turn counter and duration
	// histogram, a steps counter, a tokens counter by direction, and a
	// tool-call counter and duration histogram by tool and status. It emits
	// them at the same points as the spans. Nil or core.NoopMeterProvider means
	// zero overhead, the same repo-wide pattern as TracerProvider.
	MeterProvider core.MeterProvider

	// Selector, when non-nil, narrows the tools offered to the model each
	// step. It runs on the freshly listed set with the full history, so
	// context-aware routing (keyword, embedding, scored) plugs in here.
	// A selector must be a pure function of (history, tools). If it keeps a
	// cache, the cache should key on tool-list content, never on time or
	// notifications. Then list-changed invalidation has exactly one source,
	// the ToolSource layer. A selector error aborts the turn, because it means
	// a host configuration bug and the model can't recover from that.
	Selector ToolSelector

	// ToolMiddleware wraps every tool call this Runner dispatches. It is the
	// single interception seam. Permission gates, argument redaction, rate
	// and budget limits, blocklists, result caching, retries, audit trails,
	// and marking untrusted tool output are all middleware, not separate
	// mechanisms.
	//
	// Entries wrap outermost-first. The first wraps the second, and the last
	// wraps the real dispatch. A permission gate belongs last, so it sees the
	// arguments after every earlier entry has rewritten them. TieredApproval
	// is the batteries-included gate, and agent/host appends it last.
	//
	// This is not an observation seam. To watch what a turn does, subscribe
	// to the event stream, which already reports every tool outcome. See
	// ToolMiddleware.
	//
	// Empty means every call dispatches unwrapped.
	ToolMiddleware []ToolMiddleware

	// Generation sets the default generation knobs (temperature, token cap,
	// tool-choice bias) for every model call this Runner makes. That covers
	// the streaming steps and the finalizing Generate of a structured-output
	// turn. TurnRequest.Generation overrides it per turn, field by field.
	//
	// The zero value sends nothing. The provider's own defaults apply, and the
	// request has no temperature, max_tokens, or tool_choice. Requests looked
	// the same before these knobs existed.
	Generation GenerationParams

	// Compactor, when non-nil, may rewrite the turn's working messages before
	// any model call to keep the context under a budget. It summarizes the
	// head and keeps a recent tail verbatim. It runs as the first ScopeTurn
	// stage of the step chain (see StepStages). So it covers a single turn
	// that grows past the budget, and also a long conversation (#7). The
	// record is untouched. TurnResult.Messages keeps every message, and Run is
	// still stateless over the history it is given. A no-op returns the input
	// unchanged and emits nothing. A real compaction emits EventCompaction
	// with the step. A Compactor error aborts the turn, since it means a
	// misconfiguration or a summarizer-provider outage, and the model can't
	// recover from either. Some compactors can report whether messages fit,
	// through Fits, and SummarizingCompactor does. With one of those, the turn
	// also ends with ErrContextBudget when a step after the first still
	// doesn't fit after compacting. Nil means messages are sent verbatim.
	Compactor Compactor

	// StepStages run before every model call, after the built-in Compactor
	// and Selector stages, in this order within each StepScope. They are
	// the "before each model call" hook point in docs/HOOKS.md. A stage can
	// add to the record (ScopeSession), rewrite the working messages for the
	// rest of the turn (ScopeTurn), or shape one request (ScopeRequest). An
	// error from a stage ends the turn. StepBudgetStage is a ready-made
	// request stage.
	StepStages []StepStage

	// Interruptible lets a child break this Runner's fan-out join barrier by
	// raising an upward Signal mid-flight (issue 1167, piece C of the 1036
	// control axis). It defaults to false, and the turn is then a pure
	// fan-out-then-join. Resume, fork, eval and compaction rely on that
	// property, so only a signal-wired turn should set it. shouldBreakOn
	// decides which signals break the barrier. Escalate always breaks, preempt
	// breaks only when PreemptGrant honors it, and custom never breaks. When
	// Interruptible is true, the first breaking signal a child raises during a
	// dispatch cancels the remaining in-flight calls, and they feed back
	// "cancelled by user". The dispatch returns the partial results, and the
	// turn's step loop re-enters the model to re-plan. With no breaking
	// signal, an interruptible turn still waits for every call, as the default
	// does. The re-entry ordering is the one bounded-nondeterminism exception
	// to the pure turn. Every emitted event still projects 1:1 (A2).
	Interruptible bool

	// SignalPolicy, when non-nil, decides how this Runner reacts as a parent
	// to upward Signals from its children (issue 1165). Children raise them
	// during a dispatch, and the policy reads them at the join. It runs after
	// the fan-out has joined, so it can only choose whether to abort the turn
	// (SignalAction.AbortTurn -> ErrSignalAbort). Either way, the Runner
	// injects the signals into the next step as a RoleSystem note, so the
	// parent model sees them. Nil means inject-and-continue. See
	// AbortOnEscalate for the built-in deterministic policy.
	SignalPolicy SignalPolicy

	// PreemptGrant decides whether a child's advisory SignalPreempt breaks the
	// interruptible join barrier and cancels the other in-flight calls. A
	// child under A7 isolation can't know the global goal, so it can't verify
	// its own preempt claim. The parent holds that authority instead. Nil is
	// the default, and then a preempt never breaks. The Runner injects it
	// like any signal and the parent model decides whether to re-plan. So a
	// rogue or prompt-injected child can't cancel its siblings on its own. A
	// non-nil PreemptGrant is consulted for each preempt signal, and it may
	// inspect Source and Note to honor only trusted children. Returning true
	// grants the preemption. It must be pure and goroutine-safe, because it
	// is called from a child's raise. It is consulted only when Interruptible
	// is set. It doesn't gate SignalEscalate, which breaks unconditionally.
	PreemptGrant func(sig Signal) bool

	// ResponseSchema, when set, coerces the turn's final answer into
	// structured output. The tool loop ends when the model answers in text
	// without calling a tool. The Runner then makes one more Generate call
	// with this schema and no tools, and puts the JSON document on
	// TurnResult.Structured. That call runs before the TurnEnd hooks, and the
	// hooks judge the document. So after a refusal, the finalizing call runs
	// again on the next answer. Many endpoints forbid tools and a response
	// schema in the same request, so the Runner never sends both together.
	// The schema goes on a separate finalizing call instead of on the loop's
	// requests. Empty means no structured coercion.
	ResponseSchema core.RawJSON

	// ResponseGuided, when set, holds the turn's answer to a decoding
	// constraint. The constraint is a grammar, a regex or a JSON schema. The
	// server enforces it token by token, so the answer can't come out
	// malformed. A step that offers no tools sends it on its own request. A
	// step that offers tools can't, because a grammar covers the whole
	// completion and tool calling needs its own grammar. So once the model
	// answers without calling a tool, the Runner makes one more call from the
	// same conversation, with no tools and with the constraint. That text
	// replaces the free answer, both as the turn's answer and in its record.
	// TurnEnd hooks judge the constrained answer.
	//
	// The turn fails with ErrUnsupportedParam before any model call when the
	// provider's Capabilities can't send the requested kind. It fails with an
	// error when ResponseSchema is also set. Nil means no constraint.
	//
	// It is each step's starting grammar. A ScopeRequest StepStage may
	// replace or clear it for one call through StepView.Guided (#259).
	ResponseGuided *GuidedDecoding

	// TurnEnd runs, in order, each time the model answers without calling a
	// tool, before the answer is accepted (#150). It judges what the turn
	// would return. With ResponseSchema set, that is the finalized JSON
	// document and whether it validates. With ResponseGuided set, it is the
	// constrained answer. The model's free answer is on TurnEndInfo.Draft. A
	// refused answer has already cost its finalizing call. The first hook that
	// doesn't accept wins. On a Continue, its note joins the turn as a
	// RoleSystem note (RoleUser with AsUser), the Runner emits
	// EventTurnContinued, and the loop takes another step. That step counts
	// against MaxSteps and the tree budget like any other. So a hook that
	// never accepts ends the turn with ErrMaxSteps instead of looping. On a
	// Stop, the turn ends at once with ErrTurnStopped and a partial result
	// that holds the refused answer.
	TurnEnd []TurnEndHook
}

// ToolSelector narrows the model-facing tool set for one step. It runs as the
// first ScopeRequest stage of the step chain, so it sees the messages after
// compaction. A nil selector offers everything, and so does one that returns
// the input slice unchanged. Returning an empty slice offers no tools for
// that step. Names must be preserved verbatim, because Call routing still
// resolves them against the underlying ToolSource.
type ToolSelector func(ctx context.Context, history []Message, tools []core.ToolDef) ([]core.ToolDef, error)

// Runner executes turns: the multi-step loop that streams the model,
// dispatches its tool calls, feeds results back, and repeats until the model
// answers in text. Safe for concurrent use; each Run call is an independent
// turn.
type Runner struct {
	cfg     RunnerConfig
	metrics *runnerMetrics
}

// NewRunner validates cfg and returns a Runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("agent: RunnerConfig requires a Provider")
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = defaultMaxSteps
	}
	if cfg.TracerProvider == nil {
		cfg.TracerProvider = core.NoopTracerProvider{}
	}
	if cfg.MeterProvider == nil {
		cfg.MeterProvider = core.NoopMeterProvider{}
	}
	return &Runner{cfg: cfg, metrics: newRunnerMetrics(cfg.MeterProvider)}, nil
}

// TurnResult is the completed turn. Messages holds exactly the entries the
// turn appended (assistant messages and tool results, in order), so callers
// thread history as append(history, result.Messages...).
type TurnResult struct {
	// Text is the final assistant message of the turn, which is the Text of
	// the last model call. Intermediate steps' text is not concatenated here;
	// read Messages for the full sequence, or the event stream to see text as
	// it arrived.
	Text string `json:"text,omitempty"`

	// Messages is exactly what the turn appended, never the full history.
	// Thread it with append(history, result.Messages...).
	Messages []Message `json:"messages"`

	// Usage sums every model call this Runner made during the turn: each
	// step of the loop plus the finalizing Generate of a structured-output
	// turn. Steps whose provider reported no usage contribute zero, so an
	// under-reporting provider yields an undercount rather than an error.
	//
	// It does not include sub-agents. A child reached through AgentSource,
	// AsyncAgentSource, FanOutSource, Team, or AgentPool runs its own Runner
	// and accounts for its own tokens, so cost accounting over an agent tree
	// has to sum the children separately. TreeBudget is the mechanism that
	// does see the whole tree; this field deliberately does not.
	Usage Usage `json:"usage"`

	// UsageByModel splits Usage by StepView.ProviderName. It applies when a
	// StepStage named or routed the turn's steps (#185). It is nil when no
	// stage named any step, so a single-provider turn reads exactly as
	// before. Unnamed steps are counted under "". The finalizing call of a
	// structured or guided turn runs on RunnerConfig.Provider. A stage may
	// have named a step it left on that provider, such as a route's default
	// label (#225). If so, the finalizing call is counted under the last such
	// name, and otherwise under "". Like Usage, it leaves out sub-agents.
	UsageByModel map[string]Usage `json:"usageByModel,omitempty"`

	// Steps is how many times the loop called the model, counting from one.
	// A turn that answered without tools reports 1. The finalizing Generate
	// of a structured-output turn is not counted here, though its tokens are
	// counted in Usage. Reaching RunnerConfig.MaxSteps returns an error
	// wrapping ErrMaxSteps instead of a result.
	Steps int `json:"steps"`

	// FinishReason is the last model call's finish reason, in the provider's
	// own vocabulary and unmapped. See ProviderResponse.FinishReason.
	FinishReason string `json:"finishReason,omitempty"`

	// Structured is the schema-coerced final answer, present only when
	// RunnerConfig.ResponseSchema was set. It is the JSON document from the
	// finalizing Generate call; Bind it into a typed value. Its Usage is
	// already folded into Usage above. Empty when no schema was configured.
	Structured core.RawJSON `json:"structured,omitempty"`

	// StructuredError says why Structured is not valid against the schema
	// after every finalizing attempt. It says the output was not JSON, or it
	// lists the validation errors of the last attempt. Empty when Structured
	// is valid or no schema was set.
	// The turn itself still succeeds, so a caller that needs a valid
	// document checks this rather than the error.
	StructuredError string `json:"structuredError,omitempty"`
}

// Control is the turn's steering envelope. Surfaces send Controls on
// TurnRequest.Control to steer a turn while it runs. Cancellation is the
// first verb, and currently the only one. It has two modes.
//
//   - Control{} cancels ALL calls currently in flight with one send. This
//     is the naive-Esc path. A surface needs no bookkeeping, and three
//     in-flight calls die from a single Control.
//   - Control{CallID: id} cancels exactly one call. The surface saw that
//     ToolCall.ID on the call's tool-begin event. Richer surfaces use this
//     mode, such as a TUI with a row per running call.
//
// Either way the sender makes the decision. Surfaces already track the
// in-flight calls through tool-begin and tool-end events, and constraint A4
// keeps decision callbacks out of the loop.
//
// Future steering verbs (pause, budget bumps, mid-turn priority hints)
// will extend this struct additively, instead of adding new channels or a
// handler registry. The plan is a Kind discriminator plus verb fields, and
// the zero Kind means cancel for compatibility. Mid-turn *content*, such as
// a "/btw" note for the model, is deliberately not a Control. Anything the
// model should see goes through the injection path, so it enters history
// as a message and not as a side effect.
type Control struct {
	// CallID names the in-flight tool call to cancel. Empty cancels every
	// call currently in flight. A call might have finished already, or never
	// been dispatched. Its ID is then a no-op, so racing a call's natural
	// completion is safe.
	CallID string
}

// TurnRequest consolidates RunTurn's inputs so the turn surface can grow
// without breaking signatures (the same C2 shape RunnerConfig uses).
type TurnRequest struct {
	// History is the conversation so far; RunTurn clones it and returns
	// only appended messages, exactly like Run.
	History []Message

	// Emit receives the turn's event stream. Nil is allowed; emit is
	// never called concurrently.
	Emit func(Event)

	// Control, when non-nil, is drained for the whole turn. A Control
	// cancels the targeted call's own context, and the call fails fast.
	// ClientSource.Call threads ctx to the wire, so MCP servers see a real
	// cancellation. The call's result is fed back to the model as
	// "cancelled by user", and the turn continues. Cancelling RunTurn's ctx
	// is different, because it aborts the whole turn. Send only while a
	// turn is running. Between turns nothing drains the channel, and a
	// buffered cancel-all would hit the next turn's first dispatch.
	Control <-chan Control

	// Generation overrides RunnerConfig.Generation for this turn, field by
	// field. A set field wins, and a zero field inherits the config's. Use it
	// for per-turn decisions that the config can't express. Examples are
	// forcing a tool call on a proactive turn, capping tokens on one cheap
	// turn, and varying temperature across sampled candidates.
	//
	// The zero value changes nothing and the turn runs on the config's
	// defaults.
	Generation GenerationParams

	// ResponseSchema overrides RunnerConfig.ResponseSchema for this turn. The
	// Runner coerces the final answer into a JSON document valid against it
	// and puts that on TurnResult.Structured. Imagine a headless caller that
	// uses a session for chat but wants one structured result from it. It
	// sets the schema here, for that one turn. Empty keeps
	// the config's schema. A schema that doesn't compile fails the turn
	// before any model call.
	ResponseSchema core.RawJSON

	// ResponseGuided overrides RunnerConfig.ResponseGuided for this turn. It
	// suits a caller whose grammar differs per request, for example a grammar
	// built from the vocabulary of one query. Nil keeps the config's. A
	// grammar that changes between calls inside the turn goes through
	// StepView.Guided instead.
	ResponseGuided *GuidedDecoding
}

// Run executes one turn against history. Events stream to emit, which may be
// nil and is never called concurrently. A tool can fail as an unknown tool,
// a transport error or bad args. Every such failure is fed back to the model
// as an error-marked tool result, and the loop continues. Only ctx
// cancellation, provider failure, the step cap, or a turn-end hook's Stop
// abort the turn. The returned error wraps ErrMaxSteps when the cap was hit,
// and ErrTurnStopped when a hook stopped the turn. Both come with a partial
// result. Run is shorthand for RunTurn without mid-turn controls.
func (r *Runner) Run(ctx context.Context, history []Message, emit func(Event)) (*TurnResult, error) {
	return r.RunTurn(ctx, TurnRequest{History: history, Emit: emit})
}

// RunTurn executes one turn with the full request surface: Run's
// contract plus mid-turn Controls (per-call cancellation). See
// TurnRequest for the semantics of each field.
func (r *Runner) RunTurn(ctx context.Context, req TurnRequest) (*TurnResult, error) {
	history, emit := req.History, req.Emit
	if emit == nil {
		emit = func(Event) {}
	}
	gen := r.cfg.Generation.merge(req.Generation)
	schema := r.cfg.ResponseSchema
	if req.ResponseSchema.Len() > 0 {
		schema = req.ResponseSchema
	}
	var compiled *core.CompiledSchema
	if schema.Len() > 0 {
		var doc any
		if err := json.Unmarshal(schema.Raw(), &doc); err != nil {
			return nil, fmt.Errorf("agent: response schema: %w", err)
		}
		c, err := core.CompileSchema(doc)
		if err != nil {
			return nil, fmt.Errorf("agent: response schema: %w", err)
		}
		compiled = c
	}

	guided := r.cfg.ResponseGuided
	if req.ResponseGuided != nil {
		guided = req.ResponseGuided
	}
	if guided != nil {
		if schema.Len() > 0 {
			return nil, fmt.Errorf("agent: ResponseGuided and ResponseSchema both set; a turn's answer takes one constraint")
		}
		if _, err := guided.kind(); err != nil {
			return nil, fmt.Errorf("agent: ResponseGuided: %w", err)
		}
		if !CapabilitiesOf(r.cfg.Provider).CanGuide(guided) {
			return nil, fmt.Errorf("%w: ResponseGuided (the provider can't send this kind of constraint)", ErrUnsupportedParam)
		}
	}

	var reg *callCancels
	if req.Control != nil {
		reg = &callCancels{cancels: map[string]context.CancelFunc{}}
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			for {
				select {
				case c, ok := <-req.Control:
					if !ok {
						return
					}
					reg.cancel(c.CallID)
				case <-stop:
					return
				}
			}
		}()
	}
	// Install the aggregate tree budget only if ctx doesn't already have one.
	// The top-level Runner installs it, and every sub-agent run inherits the
	// same shared counter, so the totals add up across the tree.
	if !r.cfg.TreeBudget.zero() && treeBudgetFrom(ctx) == nil {
		ctx = WithTreeBudget(ctx, r.cfg.TreeBudget)
	}
	treeBudget := treeBudgetFrom(ctx)

	ctx, turnSpan := r.cfg.TracerProvider.StartSpan(ctx, "agent.turn")
	defer turnSpan.End()
	turnStart := time.Now()
	emit(Event{Kind: EventTurnBegin})

	// The system prompt is resolved once per turn (dynamic when InstructionsFunc
	// is set), then reused across this turn's steps.
	turnInstructions := r.cfg.Instructions
	if r.cfg.InstructionsFunc != nil {
		turnInstructions = r.cfg.InstructionsFunc(ctx)
	}

	msgs := slices.Clone(history)
	chain := r.stepChain(emit)
	var added []Message
	var usage Usage
	byModel := usageByModel{}
	// defaultName is the name a stage last gave a step it left on
	// RunnerConfig.Provider. The finalizing calls run there, so they count
	// under it.
	defaultName := ""

	for step := 1; step <= r.cfg.MaxSteps; step++ {
		// Charge one step to the aggregate tree budget before spending another
		// model call. The charge fails if prior steps already blew the token cap.
		if !treeBudget.consumeStep() {
			return nil, r.failSpan(emit, turnSpan, fmt.Errorf("%w (aggregate across the agent tree)", ErrTreeBudget))
		}
		stepCtx, stepSpan := r.cfg.TracerProvider.StartSpan(ctx, "agent.step",
			core.Attribute{Key: "agent.step", Value: fmt.Sprint(step)})

		var tools []core.ToolDef
		if r.cfg.Tools != nil {
			var err error
			if tools, err = r.cfg.Tools.Tools(stepCtx); err != nil {
				stepSpan.RecordError(err)
				stepSpan.End()
				return nil, r.failSpan(emit, turnSpan, fmt.Errorf("agent: listing tools: %w", err))
			}
		}
		info := StepInfo{Step: step, MaxSteps: r.cfg.MaxSteps, Usage: usage, Turn: slices.Clone(added), Emit: emit}
		working, recorded, view, err := runStepChain(stepCtx, chain, info, msgs, tools, guided, gen)
		if err != nil {
			stepSpan.RecordError(err)
			stepSpan.End()
			if errors.Is(err, ErrContextBudget) {
				return r.partialResult(added, usage, byModel, step-1, err, "context_budget", emit, turnSpan)
			}
			if errors.Is(err, ErrTurnStopped) {
				return r.partialResult(added, usage, byModel, step-1, err, "stopped", emit, turnSpan)
			}
			return nil, r.failSpan(emit, turnSpan, err)
		}
		msgs, tools = working, view.Tools
		added = append(added, recorded...)
		stepSpan.SetAttribute("agent.tools.offered", fmt.Sprint(len(tools)))

		stepReq := ProviderRequest{
			Instructions: turnInstructions,
			Messages:     view.Messages,
			Tools:        tools,
		}
		view.Generation.applyTo(&stepReq)
		prov := r.cfg.Provider
		if view.Provider == nil && view.ProviderName != "" {
			defaultName = view.ProviderName
		}
		if view.Provider != nil {
			prov = view.Provider
			stepSpan.SetAttribute("agent.provider", view.ProviderName)
		}
		stepGuided := view.Guided
		sendGuided := stepGuided != nil && len(tools) == 0
		if err := r.checkStepGuided(stepGuided, guided, sendGuided, prov, compiled != nil, step, view.ProviderName); err != nil {
			stepSpan.RecordError(err)
			stepSpan.End()
			return nil, r.failSpan(emit, turnSpan, err)
		}
		if sendGuided {
			stepReq.Guided = stepGuided
		}
		stream, err := prov.Stream(stepCtx, stepReq)
		if err != nil {
			stepSpan.RecordError(err)
			stepSpan.End()
			return nil, r.failSpan(emit, turnSpan, err)
		}
		resp, err := consumeStream(stream, step, emit)
		stream.Close()
		if err != nil {
			stepSpan.RecordError(err)
			stepSpan.End()
			return nil, r.failSpan(emit, turnSpan, err)
		}
		if resp.Usage != nil {
			usage.InputTokens += resp.Usage.InputTokens
			usage.OutputTokens += resp.Usage.OutputTokens
			usage.CachedInputTokens += resp.Usage.CachedInputTokens
			treeBudget.addTokens(resp.Usage.InputTokens + resp.Usage.OutputTokens)
		}
		byModel.add(view.ProviderName, resp.Usage)
		emit(Event{Kind: EventStep, Step: step, Model: view.ProviderName, Usage: resp.Usage})

		assistant := Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls}
		msgs = append(msgs, assistant)
		added = append(added, assistant)

		if len(resp.ToolCalls) == 0 {
			info := TurnEndInfo{Step: step, MaxSteps: r.cfg.MaxSteps, Text: resp.Text, Usage: usage}
			if stepGuided != nil && !sendGuided {
				// msgs ends with the free answer; the guided call answers from
				// what came before it.
				before := usage
				text, err := r.finalizeGuided(ctx, turnInstructions, msgs[:len(msgs)-1], &usage, gen, stepGuided)
				byModel.addDelta(defaultName, before, usage)
				if err != nil {
					stepSpan.RecordError(err)
					stepSpan.End()
					return nil, r.failSpan(emit, turnSpan, fmt.Errorf("agent: guided finalize: %w", err))
				}
				info.Draft, info.Text, info.Usage = resp.Text, text, usage
				resp.Text = text
				msgs[len(msgs)-1].Text = text
				added[len(added)-1].Text = text
			}
			var structured core.RawJSON
			var structuredErr string
			if compiled != nil {
				before := usage
				s, invalid, err := r.finalizeStructured(ctx, turnInstructions, msgs, &usage, gen, schema, compiled)
				byModel.addDelta(defaultName, before, usage)
				if err != nil {
					stepSpan.RecordError(err)
					stepSpan.End()
					return nil, r.failSpan(emit, turnSpan, fmt.Errorf("agent: structured finalize: %w", err))
				}
				structured, structuredErr = s, invalid
				info.Draft, info.Text, info.StructuredError, info.Usage = resp.Text, string(s.Raw()), invalid, usage
			}
			info.Messages = added
			verdict, err := r.turnEnd(stepCtx, info)
			if err != nil {
				stepSpan.RecordError(err)
				stepSpan.End()
				return nil, r.failSpan(emit, turnSpan, fmt.Errorf("agent: turn-end hook: %w", err))
			}
			if verdict.Stop != "" {
				stepSpan.SetAttribute("agent.turn_end", "stopped")
				stepSpan.End()
				return r.partialResult(added, usage, byModel, step, fmt.Errorf("%w: %s", ErrTurnStopped, verdict.Stop), "stopped", emit, turnSpan)
			}
			if verdict.Continue != "" {
				stepSpan.SetAttribute("agent.turn_end", "continued")
				stepSpan.End()
				role := RoleSystem
				if verdict.AsUser {
					role = RoleUser
				}
				cont := Message{Role: role, Text: verdict.Continue}
				msgs = append(msgs, cont)
				added = append(added, cont)
				emit(Event{Kind: EventTurnContinued, Step: step, Reason: verdict.Continue})
				continue
			}
			stepSpan.End()
			result := &TurnResult{
				Text:            resp.Text,
				Messages:        added,
				Usage:           usage,
				UsageByModel:    byModel.result(),
				Steps:           step,
				FinishReason:    resp.FinishReason,
				Structured:      structured,
				StructuredError: structuredErr,
			}
			turnSpan.SetAttribute("agent.steps", fmt.Sprint(step))
			turnSpan.SetAttribute("agent.finish_reason", resp.FinishReason)
			turnSpan.SetAttribute("agent.tokens.input", fmt.Sprint(usage.InputTokens))
			turnSpan.SetAttribute("agent.tokens.cached", fmt.Sprint(usage.CachedInputTokens))
			turnSpan.SetAttribute("agent.tokens.output", fmt.Sprint(usage.OutputTokens))
			r.metrics.turnDone(ctx, step, usage.InputTokens, usage.OutputTokens, resp.FinishReason, time.Since(turnStart))
			emit(Event{Kind: EventTurnEnd, Result: result})
			return result, nil
		}

		toolMsgs, signals := r.dispatch(stepCtx, step, resp.ToolCalls, tools, emit, reg)
		stepSpan.End()
		if err := ctx.Err(); err != nil {
			return nil, r.failSpan(emit, turnSpan, err)
		}
		// Providers pair RoleTool messages with the assistant's tool calls, so
		// tool results must immediately follow the assistant message that
		// requested them. Append them first, and put a signal note after.
		msgs = append(msgs, toolMsgs...)
		added = append(added, toolMsgs...)
		if len(signals) > 0 {
			for i := range signals {
				emit(Event{Kind: EventSignal, Step: step, Signal: &signals[i]})
			}
			if r.cfg.SignalPolicy != nil {
				if act := r.cfg.SignalPolicy(signals); act.AbortTurn {
					reason := act.Reason
					if reason == "" {
						reason = "child signalled abort"
					}
					return nil, r.failSpan(emit, turnSpan, fmt.Errorf("%w: %s", ErrSignalAbort, reason))
				}
			}
			// Inject the signals as a RoleSystem note so the parent model sees
			// them on the next step. The signals are drained once from the sink
			// and appended once, so they don't stack up the way a re-derived
			// transient snapshot would.
			sigMsg := Message{Role: RoleSystem, Text: renderSignals(signals)}
			msgs = append(msgs, sigMsg)
			added = append(added, sigMsg)
		}
	}

	return r.partialResult(added, usage, byModel, r.cfg.MaxSteps, fmt.Errorf("%w (%d steps)", ErrMaxSteps, r.cfg.MaxSteps), "max_steps", emit, turnSpan)
}

// partialResult builds the partial result for a turn that ended early. The
// turn reports it alongside its error, which is ErrMaxSteps, ErrContextBudget
// from compaction, or ErrTurnStopped from a turn-end hook. The result holds
// the messages the turn got through. A caller keeps them in history instead
// of starting the next turn blind (issue 109). A trailing assistant message
// may have tool calls that were never answered. A provider rejects that shape
// on the next request, so partialResult trims it off. Text and FinishReason
// are empty, because there is no final answer. The caller learns from the
// returned error that the turn did not complete.
func (r *Runner) partialResult(added []Message, usage Usage, byModel usageByModel, steps int, err error, finish string, emit func(Event), turnSpan core.Span) (*TurnResult, error) {
	msgs := DropUnansweredToolCalls(added)
	result := &TurnResult{
		Messages:     msgs,
		Usage:        usage,
		UsageByModel: byModel.result(),
		Steps:        steps,
	}
	turnSpan.SetAttribute("agent.steps", fmt.Sprint(steps))
	turnSpan.SetAttribute("agent.finish_reason", finish)
	turnSpan.SetAttribute("agent.tokens.input", fmt.Sprint(usage.InputTokens))
	turnSpan.SetAttribute("agent.tokens.cached", fmt.Sprint(usage.CachedInputTokens))
	turnSpan.SetAttribute("agent.tokens.output", fmt.Sprint(usage.OutputTokens))
	emit(Event{Kind: EventError, Error: err.Error()})
	turnSpan.RecordError(err)
	return result, err
}

// DropUnansweredToolCalls trims history back to its last complete round. An
// assistant message that requested tool calls must be followed by one
// RoleTool message per call. Imagine a turn killed mid-dispatch, or one that
// hit its step cap between the request and the results. Its history ends in
// an assistant message with tool calls and no results, and this function
// drops that message. Everything else passes through unchanged. Call it
// before threading a partial turn into history, so the next request has a
// shape providers accept.
func DropUnansweredToolCalls(msgs []Message) []Message {
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role != RoleAssistant || len(last.ToolCalls) == 0 {
			return msgs
		}
		msgs = msgs[:len(msgs)-1]
	}
	return msgs
}

// usageByModel accumulates a turn's usage per StepView.ProviderName.
type usageByModel map[string]Usage

func (m usageByModel) add(name string, u *Usage) {
	t := m[name] // touch the key even without usage, so a named step counts as named
	if u != nil {
		t.InputTokens += u.InputTokens
		t.OutputTokens += u.OutputTokens
		t.CachedInputTokens += u.CachedInputTokens
	}
	m[name] = t
}

// addDelta takes the usage a finalizing call added to the turn and credits it
// to name. name is the configured provider's label, since that provider makes
// the call.
func (m usageByModel) addDelta(name string, before, after Usage) {
	m.add(name, &Usage{
		InputTokens:       after.InputTokens - before.InputTokens,
		OutputTokens:      after.OutputTokens - before.OutputTokens,
		CachedInputTokens: after.CachedInputTokens - before.CachedInputTokens,
	})
}

// result is the TurnResult.UsageByModel value: nil unless some step was named.
func (m usageByModel) result() map[string]Usage {
	for name := range m {
		if name != "" {
			return maps.Clone(m)
		}
	}
	return nil
}

func (r *Runner) failSpan(emit func(Event), span core.Span, err error) error {
	span.RecordError(err)
	emit(Event{Kind: EventError, Error: err.Error()})
	return err
}

// consumeStream folds one model call, emitting deltas as they arrive.
// Thinking markers wrap contiguous reasoning. Begin comes before the first
// reasoning delta, and end comes when the step moves on to text, tool calls, or
// completes.
func consumeStream(stream Stream, step int, emit func(Event)) (*ProviderResponse, error) {
	var acc DeltaAccumulator
	thinking := false
	endThinking := func() {
		if thinking {
			emit(Event{Kind: EventThinkingEnd, Step: step})
			thinking = false
		}
	}
	for {
		d, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		acc.Add(d)
		switch d.Kind {
		case DeltaReasoning:
			if !thinking {
				emit(Event{Kind: EventThinkingBegin, Step: step})
				thinking = true
			}
			emit(Event{Kind: EventThinkingDelta, Step: step, Text: d.Text})
		case DeltaText:
			endThinking()
			emit(Event{Kind: EventTextDelta, Step: step, Text: d.Text})
		case DeltaToolCallStart:
			endThinking()
		}
	}
	endThinking()
	return acc.Result(), nil
}

// structuredMaxAttempts bounds the finalizing Generate. It counts one initial
// call plus retries when the model returns text that isn't valid against the
// schema. Two in total keeps the extra cost low and still absorbs the
// occasional near-miss.
const structuredMaxAttempts = 2

// finalizeStructured makes the schema-coercing Generate call over the finished
// conversation, with no tools offered. msgs already includes the terminal
// assistant text. It checks the answer against the schema. When an attempt
// fails, the model sees what was wrong before the next attempt. Each call's
// usage is folded into usage. A provider error aborts, because the caller
// asked for structured output and can't get it. Once the retry budget is
// spent, it returns the last output best-effort, and invalid says why that
// output doesn't validate.
func (r *Runner) finalizeStructured(ctx context.Context, instructions string, msgs []Message, usage *Usage, gen GenerationParams, schema core.RawJSON, compiled *core.CompiledSchema) (doc core.RawJSON, invalid string, err error) {
	var last string
	msgs = slices.Clip(msgs)
	for attempt := 0; attempt < structuredMaxAttempts; attempt++ {
		finalReq := ProviderRequest{
			Instructions:   instructions,
			Messages:       msgs,
			ResponseSchema: schema,
		}
		gen.applyTo(&finalReq)
		// The finalizing call offers no tools. A ToolChoice copied from the
		// turn's params would ask the provider to force a tool call, and the
		// provider can't make one. Drop it here. applyTo is shared with the
		// step loop, so it can't drop it there.
		finalReq.ToolChoice = ToolChoice{}
		resp, err := r.cfg.Provider.Generate(ctx, finalReq)
		if err != nil {
			return core.RawJSON{}, "", err
		}
		if resp.Usage != nil {
			usage.InputTokens += resp.Usage.InputTokens
			usage.OutputTokens += resp.Usage.OutputTokens
			usage.CachedInputTokens += resp.Usage.CachedInputTokens
			treeBudgetFrom(ctx).addTokens(resp.Usage.InputTokens + resp.Usage.OutputTokens)
		}
		last = stripJSONFence(resp.Text)
		invalid = checkStructured(last, compiled)
		if invalid == "" {
			return core.NewRawJSON(json.RawMessage(last)), "", nil
		}
		msgs = append(msgs,
			Message{Role: RoleAssistant, Text: resp.Text},
			Message{Role: RoleUser, Text: "That answer doesn't match the required JSON schema: " + invalid + ". Reply with only a JSON document that matches it."})
	}
	return core.NewRawJSON(json.RawMessage(last)), invalid, nil
}

// checkStepGuided refuses a step's grammar before the call. g is the step's
// grammar, and turn is the turn's, which RunTurn already checked against
// RunnerConfig.Provider. A grammar a stage set is checked for one kind and
// for a ResponseSchema beside it. Then g is checked against the provider that
// will send it: prov when send is true, and RunnerConfig.Provider, which runs
// the finalize, when it is not.
func (r *Runner) checkStepGuided(g, turn *GuidedDecoding, send bool, prov Provider, hasSchema bool, step int, provName string) error {
	if g == nil {
		return nil
	}
	if g != turn {
		if hasSchema {
			return fmt.Errorf("agent: step %d: a stage set a grammar beside ResponseSchema; a turn's answer takes one constraint", step)
		}
		if _, err := g.kind(); err != nil {
			return fmt.Errorf("agent: step %d's grammar: %w", step, err)
		}
	}
	target := r.cfg.Provider
	if send {
		target = prov
	}
	if CapabilitiesOf(target).CanGuide(g) {
		return nil
	}
	if target != r.cfg.Provider {
		return fmt.Errorf("%w: step %d's grammar (the step is routed to %q, which can't send this kind of constraint)", ErrUnsupportedParam, step, provName)
	}
	return fmt.Errorf("%w: step %d's grammar (the provider can't send this kind of constraint)", ErrUnsupportedParam, step)
}

// finalizeGuided makes the one constrained call that writes a guided answer
// after a step that offered tools. msgs is the conversation before the
// model's free answer. No tools are offered, and the turn's ToolChoice is
// dropped for the same reason finalizeStructured drops it. Its usage is added
// to usage and to the tree budget's tokens, as the step loop's calls are.
func (r *Runner) finalizeGuided(ctx context.Context, instructions string, msgs []Message, usage *Usage, gen GenerationParams, g *GuidedDecoding) (string, error) {
	req := ProviderRequest{Instructions: instructions, Messages: slices.Clip(msgs), Guided: g}
	gen.applyTo(&req)
	req.ToolChoice = ToolChoice{}
	resp, err := r.cfg.Provider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	if resp.Usage != nil {
		usage.InputTokens += resp.Usage.InputTokens
		usage.OutputTokens += resp.Usage.OutputTokens
		usage.CachedInputTokens += resp.Usage.CachedInputTokens
		treeBudgetFrom(ctx).addTokens(resp.Usage.InputTokens + resp.Usage.OutputTokens)
	}
	return resp.Text, nil
}

// checkStructured returns why text is not a document valid against compiled,
// or "" when it is.
func checkStructured(text string, compiled *core.CompiledSchema) string {
	if !json.Valid([]byte(text)) {
		return "it is not valid JSON"
	}
	errs := compiled.Validate(json.RawMessage(text))
	if errs == nil || len(errs.Errors) == 0 {
		return ""
	}
	parts := make([]string, len(errs.Errors))
	for i, e := range errs.Errors {
		path := e.Path
		if path == "" {
			path = "/"
		}
		parts[i] = path + ": " + e.Message
	}
	return strings.Join(parts, "; ")
}

// stripJSONFence removes a ```json fence some models put around a JSON answer
// even when asked for JSON alone.
func stripJSONFence(text string) string {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "```") {
		return text
	}
	t = strings.TrimPrefix(t, "```")
	if nl := strings.IndexByte(t, '\n'); nl >= 0 {
		t = t[nl+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
}

// callCancels tracks the in-flight tool calls of one turn, so the
// control listener can cancel a specific call's context. An entry exists
// from just before a call's tool-begin until just after the call returns.
type callCancels struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (g *callCancels) add(id string, cancel context.CancelFunc) {
	g.mu.Lock()
	g.cancels[id] = cancel
	g.mu.Unlock()
}

func (g *callCancels) remove(id string) {
	g.mu.Lock()
	delete(g.cancels, id)
	g.mu.Unlock()
}

// cancel fires the named call's cancel func, or every in-flight one when
// id is empty. Unknown ids are a no-op.
func (g *callCancels) cancel(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if id == "" {
		for _, c := range g.cancels {
			c()
		}
		return
	}
	if c, ok := g.cancels[id]; ok {
		c()
	}
}

// shouldBreakOn decides whether a child's mid-flight signal breaks the join
// barrier of this interruptible dispatch. Escalate always breaks, because it
// has a must-handle contract. A custom FYI signal never breaks. A preempt
// breaks only when the parent grants it through PreemptGrant. The child
// can't verify its preempt claim, so the parent decides, and an untrusted
// child can't cancel its siblings on its own. dispatch installs this as the
// sink's breakOn, and a child's raise calls it. So it must be a pure read of
// config.
func (r *Runner) shouldBreakOn(sig Signal) bool {
	if !sig.Kind.interrupts() {
		return false
	}
	if sig.Kind == SignalPreempt {
		return r.cfg.PreemptGrant != nil && r.cfg.PreemptGrant(sig)
	}
	return true
}

// dispatch runs the step's tool calls concurrently and serializes event
// emission. It returns RoleTool messages in call order, whatever the
// completion order. It also returns any upward Signals raised by the calls'
// sub-agents. When reg is non-nil, each call runs under its own child context,
// registered by call ID. So a Control can cancel one call without touching
// its siblings or the turn. Each dispatch installs a fresh signal sink, and
// the tree doesn't share it. A sub-agent spawned by one of these calls raises
// into THIS sink, which belongs to its immediate parent. A nested dispatch
// shadows it for grandchildren.
//
// In an interruptible turn (RunnerConfig.Interruptible, issue 1167), a child
// can raise a barrier-breaking signal, as shouldBreakOn decides. The first
// such signal cancels the remaining in-flight calls, and they feed back
// "cancelled by user". dispatch returns the partial results, and the step
// loop re-enters the model. parent is still the step ctx with the sink
// installed, and the calls run under a cancellable child of it. So a fan
// cancel leaves parent live and reads as a per-call "cancelled by user",
// never as a turn abort.
func (r *Runner) dispatch(ctx context.Context, step int, calls []ToolCall, tools []core.ToolDef, emit func(Event), reg *callCancels) ([]Message, []Signal) {
	sink := &signalSink{}
	parent := withDispatchSink(ctx, sink)
	callBase := parent
	var cancelFan context.CancelFunc
	if r.cfg.Interruptible {
		sink.notify = make(chan struct{})
		sink.breakOn = r.shouldBreakOn
		callBase, cancelFan = context.WithCancel(parent)
		defer cancelFan()
	}
	results := make([]Message, len(calls))
	var emitMu sync.Mutex
	locked := func(ev Event) {
		emitMu.Lock()
		emit(ev)
		emitMu.Unlock()
	}

	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call ToolCall) {
			defer wg.Done()
			callCtx := callBase
			if reg != nil {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithCancel(callBase)
				reg.add(call.ID, cancel)
				defer func() {
					reg.remove(call.ID)
					cancel()
				}()
			}
			callCtx = withToolEmit(callCtx, func(ev Event) {
				ev.Step = step
				locked(ev)
			})
			toolCtx, toolSpan := r.cfg.TracerProvider.StartSpan(callCtx, "agent.tool",
				core.Attribute{Key: "agent.tool.name", Value: call.Name})
			locked(Event{Kind: EventToolBegin, Step: step, ToolCall: &call})
			text := r.callTool(toolCtx, parent, step, call, tools, locked, toolSpan)
			toolSpan.End()
			results[i] = Message{Role: RoleTool, ToolCallID: call.ID, Text: text}
		}(i, call)
	}

	if cancelFan == nil {
		wg.Wait()
		return results, sink.drain()
	}
	// In an interruptible turn, stop waiting the moment a child signals and
	// cancel the rest. Then still wg.Wait for the cancelled calls to unwind.
	// Providers require a result per tool call, so every result slot must be
	// filled. With no signal, the select falls through on allDone, the same as
	// the default barrier.
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	select {
	case <-allDone:
	case <-sink.notify:
		cancelFan()
		<-allDone
	}
	return results, sink.drain()
}

// toolHints reads the named tool's safety annotations from the step's offered
// set. It applies the spec's defaults, so every caller sees one resolved
// answer and none re-derives them from a raw map.
//
// The defaults are not symmetric. An absent readOnlyHint means not read-only
// and an absent idempotentHint means not idempotent, so both match the Go
// zero value. An absent destructiveHint means DESTRUCTIVE, and the zero value
// says the opposite. Imagine a caller that reads the annotation map directly
// and takes the zero value. It would silently treat every unannotated tool
// as safe to run. So this defaulting is kept in one place.
//
// readOnlyHint wins over the other two. The spec says those two mean
// something only when a tool writes. So a read-only tool reports
// non-destructive and idempotent whatever else it declared, and no
// contradiction passes through to policy.
//
// An unknown tool gets the strictest reading. Its name is not in the offered
// set, so it has made no promises at all.
func toolHints(tools []core.ToolDef, name string) (readOnly, destructive, idempotent bool) {
	for _, t := range tools {
		if t.Name != name {
			continue
		}
		if ro, _ := t.Annotations["readOnlyHint"].(bool); ro {
			return true, false, true
		}
		destructive = true
		if d, ok := t.Annotations["destructiveHint"].(bool); ok {
			destructive = d
		}
		idempotent, _ = t.Annotations["idempotentHint"].(bool)
		return false, destructive, idempotent
	}
	return false, true, false
}

// callTool executes one call and renders the text fed back to the model.
// Every failure shape becomes model-visible text rather than a turn abort.
// ctx is the call's own context, and a Control may cancel it. parent is the
// step's context. If ctx is cancelled while parent is live, the cancellation
// was per call. It feeds back as "cancelled by user", so the model knows the
// user stopped the call and the tool didn't fail.
func (r *Runner) callTool(ctx context.Context, parent context.Context, step int, call ToolCall, tools []core.ToolDef, emit func(Event), span core.Span) string {
	// status is the terminal outcome recorded on the tool-call metric. The
	// closures and branches below set it as they resolve. The record is
	// deferred, so every return path counts exactly once, including the
	// early ones.
	start := time.Now()
	status := "ok"
	defer func() { r.metrics.toolDone(ctx, call.Name, status, time.Since(start)) }()

	// cancelled identifies a per-call cancellation. This call's ctx is done
	// while the step's is live. It is checked on every outcome shape. A
	// transport reports a cancelled call as an error, and an in-process
	// source reports it as an IsError result. A tool racing the cancel may
	// even return success. The user's cancel wins over all three.
	cancelled := func() bool { return ctx.Err() != nil && parent.Err() == nil }
	cancelledText := func() string {
		status = "cancelled"
		span.SetAttribute("agent.tool.cancelled", "true")
		emit(Event{Kind: EventToolCancelled, Step: step, ToolCall: &call, Reason: "cancelled by user"})
		return "cancelled by user"
	}
	failed := func(err error) string {
		if cancelled() {
			return cancelledText()
		}
		status = "error"
		span.RecordError(err)
		emit(Event{Kind: EventToolError, Step: step, ToolCall: &call, Error: err.Error()})
		return fmt.Sprintf(toolFailedPrefix+"%v", err)
	}

	if r.cfg.Tools == nil {
		return failed(fmt.Errorf("%w: %q (no tools offered)", ErrUnknownTool, call.Name))
	}

	// The middleware chain wraps the real dispatch. Entry 0 is outermost, and
	// the last entry wraps ToolSource.Call. So a gate placed last inspects the
	// arguments after every earlier entry has rewritten them. If a middleware
	// returns without calling next, it has decided the call.
	dispatch := func(ctx context.Context, info ToolCallInfo) (*core.ToolResult, error) {
		args, err := decodeToolArgs(info.Call.Args)
		if err != nil {
			// Return an error result instead of a dispatch failure, so the
			// model reads what was wrong and can retry.
			return errorToolResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
		return r.cfg.Tools.Call(ctx, info.Call.Name, args)
	}
	readOnly, destructive, idempotent := toolHints(tools, call.Name)
	res, err := chainToolMiddleware(r.cfg.ToolMiddleware, dispatch)(ctx, ToolCallInfo{
		Step:        step,
		Call:        call,
		Scope:       ScopeFrom(ctx),
		ReadOnly:    readOnly,
		Destructive: destructive,
		Idempotent:  idempotent,
	})
	if err != nil {
		// A denial is a decision, not a failure. The model is told the call
		// was not permitted, and the turn continues, exactly as it always did
		// after a refused approval.
		if reason, denied := deniedReason(err); denied && !cancelled() {
			status = "denied"
			span.SetAttribute("agent.tool.denied", "true")
			emit(Event{Kind: EventToolDenied, Step: step, ToolCall: &call, Reason: reason})
			return toolDeniedPrefix + deniedModelReason(err, reason)
		}
		// The tool's server may be unreachable right now. That is a non-fatal
		// miss, not a failure. As with a denial or a cancellation, the model is
		// told and the turn continues. The event sets Reason and leaves Error
		// empty, so error-keyed scorers don't count it.
		if errors.Is(err, ErrNotAvailableNow) && !cancelled() {
			status = "unavailable"
			span.SetAttribute("agent.tool.unavailable", "true")
			emit(Event{Kind: EventToolUnavailable, Step: step, ToolCall: &call, Reason: err.Error()})
			return err.Error()
		}
		return failed(err)
	}
	if cancelled() {
		return cancelledText()
	}

	span.SetAttribute("agent.tool.is_error", fmt.Sprint(res.IsError))
	emit(Event{Kind: EventToolEnd, Step: step, ToolCall: &call, ToolResult: res})
	text := toolResultText(res)
	if res.IsError {
		status = "tool_error"
		return toolErrorPrefix + text
	}
	return text
}

// The text the model gets for a tool call that didn't succeed starts with
// one of these. Message has no error flag, so DefaultStuckKey reads them.
const (
	toolErrorPrefix  = "tool reported an error: "
	toolFailedPrefix = "tool call failed: "
	toolDeniedPrefix = "tool call not permitted: "
)

// toolResultText flattens a tool result for the model. It joins the text
// content items with newlines. Without text it falls back to marshaled
// structured content, and then to a neutral placeholder. So the model always
// receives something parseable.
func toolResultText(res *core.ToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) > 0 {
		out := parts[0]
		for _, p := range parts[1:] {
			out += "\n" + p
		}
		return out
	}
	if res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			return string(raw)
		}
	}
	return "(empty result)"
}
