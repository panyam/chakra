package host

import (
	"io"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// HostEventKind tags a HostEvent. Each event reports something that happened
// in the host lifecycle. It is not a render instruction. The same stream
// drives a renderer, a tracer, a metrics sink, or a web push.
type HostEventKind string

const (
	// HostRunnerEvent carries one streaming turn event (RunnerEvent). It folds
	// the agent.Event flow (text/thinking/tool deltas) into this stream.
	HostRunnerEvent HostEventKind = "runner-event"
	// HostCommandResult carries a slash-command result (Command).
	HostCommandResult HostEventKind = "command-result"
	// HostTurnDone reports a finished turn (Result).
	HostTurnDone HostEventKind = "turn-done"
	// HostTurnFailed reports a failed turn or a command error (Err).
	HostTurnFailed HostEventKind = "turn-failed"
	// HostSessionChanged reports the active session id changing (RunID;
	// empty = none).
	HostSessionChanged HostEventKind = "session-changed"
	// HostSessionWarn reports something degraded that did not fail the turn
	// (Err). It covers persistence, a server's tool or event registration,
	// and a model connection that had to resend full history.
	HostSessionWarn HostEventKind = "session-warn"
	// HostTriggerFired reports a proactive-turn trigger (Label).
	HostTriggerFired HostEventKind = "trigger-fired"
	// HostSkillsLoaded reports a server's skills load (ServerID, Loaded,
	// Skipped).
	HostSkillsLoaded HostEventKind = "skills-loaded"
	// HostSkillSkipped reports one skipped skill (ServerID, URI, Err).
	HostSkillSkipped HostEventKind = "skill-skipped"
	// HostEventDropped reports a dropped subscription event (ServerID,
	// EventName).
	HostEventDropped HostEventKind = "event-dropped"
	// HostTaskStatus reports a background task status change (TaskStatus).
	HostTaskStatus HostEventKind = "task-status"
	// HostTaskDetached reports a task detaching to the background (Task).
	HostTaskDetached HostEventKind = "task-detached"
	// HostTaskCompleted reports a background task finishing (Task).
	HostTaskCompleted HostEventKind = "task-completed"
	// HostMessage is a plain informational line (Message).
	HostMessage HostEventKind = "message"
	// HostSubAgentEvent carries a sub-agent's nested turn-lifecycle event
	// (SubAgent). It reports a persona's activity, with scope and depth on the
	// envelope. A surface renders it indented under the parent's turn.
	HostSubAgentEvent HostEventKind = "sub-agent-event"
	// HostServerStateChanged reports an MCP server's connection state changing
	// (ServerID, ServerState, and Err on failed/needs-login). Surfaces use it
	// to degrade gracefully while servers connect asynchronously (see
	// docs/AGENT_SERVER_STATE.md).
	HostServerStateChanged HostEventKind = "server-state-changed"
	// HostHandoff reports a Team transferring control from one agent to another
	// (From, To). Only Config.Team emits it. The active agent changes, so a
	// surface renders "→ handed off to <To>" and can update which agent is
	// answering.
	HostHandoff HostEventKind = "handoff"
	// HostElicitRequest announces a pending elicitation/approval ask to every
	// surface (AskID, Elicit). The other kinds are fire-and-forget, and this
	// one is answerable. Any surface reads it and calls RespondToAsk(AskID,
	// ...), and the first responder wins. The local terminal UI is one such
	// responder.
	HostElicitRequest HostEventKind = "elicit-request"
	// HostElicitResolved reports that a surface (By) answered a pending ask
	// (AskID), so every other surface retracts its prompt.
	HostElicitResolved HostEventKind = "elicit-resolved"
	// HostUserInput carries the user's input for a turn as they typed it
	// (Message), before any InputTransform expands it. It is emitted when
	// RunTurn starts, so a surface that joins later, or reloads and replays
	// the log, can show what was asked and not only what was answered.
	HostUserInput HostEventKind = "user-input"
	// HostProcessOutput carries a process's printed output (ProcessOutput).
	// The host reports it as it arrives, not when the model next collects it,
	// so a surface can show a dev server or a build while it runs. It is for
	// the user only. The model's tool results are unchanged, and the RunStore
	// does not keep it.
	HostProcessOutput HostEventKind = "process-output"
)

// ProcessOutput is one stretch of a process's output, from an extension that
// implements ProcessReporter. Successive events for one process arrive in
// order, and the last one has Exited set.
type ProcessOutput struct {
	// Source is the reporting extension's Name. ID is the process id that
	// the extension gives the model (exec_command's session_id). A tool
	// call's result names that id, so a surface can attach the output to the
	// call.
	Source string
	ID     string
	// Command is the command line the process was started with.
	Command string
	// Text is the output since the previous event. A terminal process's
	// output includes its escape sequences.
	Text string
	// Dropped counts bytes left out before Text. One event's text is capped,
	// so a longer burst keeps only its end.
	Dropped int64
	// Paused is set once, when the process's streaming budget is spent.
	// Output after that event still reaches the model, but no longer streams.
	// The exit is still reported.
	Paused bool
	// Exited and ExitCode report that the process has finished.
	Exited   bool
	ExitCode int
}

// HostEvent is one domain event that the host announces. It reports
// something that happened. The host fans it out to every Observer,
// fire-and-forget. It is a tagged union, and only the fields named for the
// Kind (in each constant's doc) are set. The host never formats for a
// specific consumer. A renderer turns events into ANSI lines, a tracer into
// spans, a web surface into socket frames.
//
// The host has two other interaction shapes, and events are neither. Input
// arrives INTO the host from a surface, which calls Dispatch/RunTurn when
// the user acts. Mid-turn input REQUESTS use the elicitation seam (an
// AskFunc / ElicitationUI, 1→1 and blocking). Events are the 1→N
// fire-and-forget half, and neither of the others uses this stream.
//
// Most payloads are wire types (agent.Event, CmdResult, TurnResult,
// strings), so they serialize. The task/health events still hold live
// pointers (TaskStatus, Task, and the Failover inside a
// HostCommandResult). A web surface needs serializable snapshots of those,
// and issue 994 tracks that conversion. The terminal renderer reads the
// pointers directly.
type HostEvent struct {
	Kind HostEventKind

	RunnerEvent agent.Event
	Command     CmdResult
	Result      *agent.TurnResult
	Err         string
	RunID       string
	Label       string
	Message     string

	ServerID    string
	ServerState string // ConnState on HostServerStateChanged
	URI         string
	EventName   string
	Loaded      int
	Skipped     int

	TaskStatus *core.DetailedTask
	Task       *client.BackgroundTask

	// SubAgent is the nested sub-agent event on HostSubAgentEvent.
	SubAgent agent.SubAgentEvent

	// From and To name the agents on HostHandoff (the transfer source and the
	// new active agent).
	From string
	To   string

	// AskID identifies a pending ask on HostElicitRequest / HostElicitResolved,
	// and a surface passes it back to RespondToAsk. Elicit holds the request
	// on HostElicitRequest. By names the resolving surface on
	// HostElicitResolved.
	AskID  int64
	Elicit *core.ElicitationRequest
	By     string

	// ProcessOutput is the output on HostProcessOutput.
	ProcessOutput *ProcessOutput
}

// Observer receives the host's HostEvent stream and does whatever it
// wants with it, such as render to a terminal, emit spans, push to a
// socket, or record for a test. Every event is also recorded on the
// retained event log, and a web surface replays and subscribes onto that
// log. An Observer gets the same stream synchronously and locally. The host
// fans every event out to all registered observers (WithObserver), so a
// renderer and a tracer coexist. On is called from the turn goroutine and
// from event goroutines. An Observer that is not inherently serialized must
// guard its own state.
//
// This is the fire-and-forget half of the host's I/O. It is deliberately
// NOT the input path. A surface drives user input by calling
// Dispatch/RunTurn. Mid-turn, input comes back through the 1→1 blocking
// elicitation seam. Neither of those is an Observer.
type Observer interface {
	On(HostEvent)
}

// WithObserver registers an Observer for the host's event stream. Any
// WithObserver suppresses the default terminal renderer, so a TUI or web
// surface takes over rendering. If you also want the terminal output (e.g. a
// renderer plus a tracer), add NewTerminalRenderer(out) explicitly alongside.
// Without any WithObserver, the App renders to the io.Writer passed to
// NewApp.
func WithObserver(o Observer) AppOption {
	return func(opts *appOptions) { opts.observers = append(opts.observers, o) }
}

// NewTerminalRenderer returns the built-in terminal Observer, the ANSI
// line renderer, writing to w. A TUI reuses it by pointing w at a buffer
// and reading the formatted transcript back, rather than reimplementing
// every event's formatting. Color follows the environment (NO_COLOR / TERM);
// use NewTerminalRendererColor to force the decision.
func NewTerminalRenderer(w io.Writer) Observer { return newRenderer(w, envColorEnabled()) }

// NewTerminalRendererColor is NewTerminalRenderer with an explicit color
// decision. A surface uses it after resolving --no-color, or its own
// precedence chain, to suppress the ANSI dim styling to match.
func NewTerminalRendererColor(w io.Writer, colorEnabled bool) Observer {
	return newRenderer(w, colorEnabled)
}
