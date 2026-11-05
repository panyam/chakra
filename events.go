package agent

import (
	"github.com/panyam/mcpkit/core"
)

// EventKind discriminates Event payloads. The vocabulary is the surfaces
// contract from docs/AGENT_DESIGN.md. Every kind is emitted in-process and
// must project 1:1 onto a wire.
type EventKind string

// Event kinds, in the order a typical turn emits them. Thinking markers wrap
// contiguous reasoning deltas within one step. Tool events may interleave
// across parallel calls of the same step. For each call, tool-begin always
// comes before its tool-end, tool-error, tool-denied, or tool-cancelled.
const (
	EventTurnBegin     EventKind = "turn-begin"
	EventThinkingBegin EventKind = "thinking-begin"
	EventThinkingDelta EventKind = "thinking-delta"
	EventThinkingEnd   EventKind = "thinking-end"
	EventTextDelta     EventKind = "text-delta"
	EventToolBegin     EventKind = "tool-begin"
	EventToolEnd       EventKind = "tool-end"
	EventToolError     EventKind = "tool-error"
	EventToolDenied    EventKind = "tool-denied"
	// EventToolCancelled marks a call that the user cancelled mid-flight
	// through a TurnRequest Control. The tool did not fail, because the user
	// stopped it. So this kind is separate from tool-error, and surfaces can
	// render an interrupt differently from a failure. Reason holds the
	// model-visible feedback text.
	EventToolCancelled EventKind = "tool-cancelled"
	// EventToolUnavailable marks a call to a tool whose backing server was
	// not reachable (ErrNotAvailableNow). The tool exists, but its server is
	// down right now. Nothing failed on the server, because the server just
	// isn't there yet. So this kind is separate from tool-error. A surface can
	// render it as a transient miss, and evals keyed on Error don't count it.
	// Reason holds the model-visible feedback text. The turn continues, so
	// the model can retry, route around it, or tell the user. See
	// docs/AGENT_SERVER_STATE.md.
	EventToolUnavailable EventKind = "tool-unavailable"
	// EventCompaction marks that a Compactor rewrote the turn's working
	// messages before a model call. It summarized the head and kept a recent
	// tail verbatim. Step says which call, and a Step above 1 is a mid-turn
	// compaction. It is emitted only when compaction actually fired.
	// Compaction holds the before/after message counts. Surfaces can render
	// a "compacted context" note, and evals can assert that it happened.
	EventCompaction EventKind = "compaction"
	// EventSignal marks that a child agent raised an upward Signal. The
	// parent Runner observes it at the dispatch join (issue 1165). Signal
	// holds the raised signal. This event projects the control-axis "up"
	// channel for observability, and a surface renders "child escalated".
	// The SignalPolicy decides the signal's effect on the turn, inject or
	// abort. This event does not.
	EventSignal EventKind = "signal"
	// EventTurnContinued marks that a TurnEndHook refused the model's final
	// answer (#150). Step is the step that answered. Reason is the message
	// that the turn continues with. That message also joins the record as a
	// RoleSystem note, or as RoleUser when the verdict set AsUser. A Stop
	// emits no turn-continued, and the turn ends with EventError. Another
	// step follows unless the turn is at MaxSteps.
	EventTurnContinued EventKind = "turn-continued"
	// EventPlan sends the plan that a model published with update_plan
	// (#148). Plan holds the whole plan each time, so a surface replaces the
	// plan it showed. Step is the step whose tool call published it.
	EventPlan EventKind = "plan"
	// EventStep closes one model call. Step is the call. Model is the name a
	// StepStage gave it through StepView.ProviderName (#185), and it is empty
	// when no stage named the step. Usage holds what the provider reported for
	// the call, or nil when it reported none. It fires after the call's deltas
	// and before its tool calls run. Every step emits one, so a watcher can
	// sum the usage of a turn that is killed before EventTurnEnd (#262). The
	// sum leaves out a structured or guided finalize call, which TurnResult
	// counts. Surfaces show a line only for a named step.
	EventStep    EventKind = "step"
	EventTurnEnd EventKind = "turn-end"
	EventError   EventKind = "error"
)

// Event is one increment of a running turn, and surfaces consume it.
// Constraint A2 makes it JSON-tagged, with a stable kind and no Go-only
// fields. Events have no turn or session identity on purpose. In-process,
// the emit closure is scoped to one Run call. Wire layers wrap events in
// their own envelope with a session id, turn id and sequence. So the module
// does not pre-commit to an ID scheme.
type Event struct {
	Kind EventKind `json:"kind"`

	// Step is the 1-based loop step for step-scoped kinds (deltas, tool
	// events). Zero on turn-begin, turn-end, and error.
	Step int `json:"step,omitempty"`

	// Text holds the fragment for text-delta and thinking-delta.
	Text string `json:"text,omitempty"`

	// ToolCall identifies the call for tool-begin, tool-end, and
	// tool-error.
	ToolCall *ToolCall `json:"toolCall,omitempty"`

	// ToolResult is the outcome on tool-end, including IsError results.
	// tool-error is reserved for dispatch failures.
	ToolResult *core.ToolResult `json:"toolResult,omitempty"`

	// Error is the failure description on tool-error and error. A string,
	// not an error value, so the event crosses wires unchanged.
	Error string `json:"error,omitempty"`

	// Reason is the human-readable justification on three kinds. On
	// tool-denied it says why the approval policy refused the call. On
	// tool-cancelled it says the user stopped the call. On turn-continued it
	// is the message that a TurnEndHook refused the answer with. All three
	// are outcomes of someone's decision, not failures, so Reason is
	// separate from Error. None of them has a ToolResult.
	Reason string `json:"reason,omitempty"`

	// Result is the completed turn on turn-end.
	Result *TurnResult `json:"result,omitempty"`

	// Compaction holds the before/after message counts on compaction.
	Compaction *CompactionInfo `json:"compaction,omitempty"`

	// Signal is the upward signal a child raised, on EventSignal.
	Signal *Signal `json:"signal,omitempty"`

	// Plan is the model's whole current plan, on EventPlan.
	Plan *Plan `json:"plan,omitempty"`

	// Model names the provider that answered, on EventStep. See
	// StepView.ProviderName.
	Model string `json:"model,omitempty"`

	// Usage is one model call's token counts, on EventStep.
	Usage *Usage `json:"usage,omitempty"`
}

// CompactionInfo reports the message count before and after a compaction
// pass summarized the head. After < Before whenever the event fires.
type CompactionInfo struct {
	Before int `json:"before"`
	After  int `json:"after"`
}
