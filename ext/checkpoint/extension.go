package checkpoint

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// WriteSpec declares that a tool writes files and says which of its arguments
// name them.
//
// Capture happens BEFORE the call, while the pre-state still exists, so the
// tool has to declare its paths. Nothing detects them. There is no filesystem
// watcher and no after-the-fact diff. So a checkpoint grows with the number of
// files a turn touched, not with the size of the tree. The same choice means a
// shell command cannot be covered, because nobody can know which files
// `make install` will touch until it has touched them.
type WriteSpec struct {
	// Tool is the tool name to intercept, as the model calls it.
	Tool string

	// Paths is given the call's arguments. It returns an absolute path for
	// each file that the call is about to write, resolved the way the tool
	// resolves it. The checkpoint refuses a relative path. Resolving it
	// against the process's working directory would snapshot a different
	// file (issue 69). A path that does not exist yet is fine, and that is
	// the normal case for a create. The checkpoint records the path as
	// absent, so undoing the call deletes the file.
	Paths func(args map[string]any) []string
}

// Config configures the extension.
type Config struct {
	// Root is the directory that holds the snapshot store. Required.
	Root string

	// Writes declares the file-writing tools. The extension never captures a
	// tool absent from this list. If such a call ran, was not read-only and
	// was not denied, /undo reports it as something it could not undo,
	// instead of passing over it in silence.
	Writes []WriteSpec

	// Proposer suggests inverse calls for the gaps that /undo could not
	// close. Nil is the default, and /undo then reports the gaps and stops.
	// That is never wrong, only less helpful.
	Proposer Proposer

	// Approve gates every proposal. It is called once per proposal and the
	// call is made only if it returns true.
	//
	// Nil means no proposal ever runs. A proposal is a guess at an operation
	// that nobody could reverse automatically, and it calls a tool that
	// changes things. So with no way to ask, the extension does nothing
	// instead of proceeding. Wire it to the host's
	// ElicitationCoordinator.Confirm to reuse the existing prompt.
	Approve func(ctx context.Context, p Proposal) (bool, error)

	// Tools dispatches an approved proposal. Nil disables running them even
	// when Approve says yes, since there would be nowhere to send the call.
	Tools agent.ToolSource
}

// Extension wires the reversal seam into a turn: capture before a write,
// restore on /undo, and report what could not be undone.
//
// Register it through host.WithExtension. The middleware and the commands then
// arrive as one unit and share one store. host.Extension exists so they can
// share it, instead of needing five separate registrations.
type Extension struct {
	host.BaseExtension

	store    *Store
	writes   map[string]WriteSpec
	proposer Proposer
	approve  func(ctx context.Context, p Proposal) (bool, error)
	tools    agent.ToolSource

	// session tags this instance's checkpoint ids. The turn counter restarts
	// with the process but the store persists, so "turn-1" alone would reopen
	// an earlier session's checkpoint and merge into it (issue 69).
	session string

	mu         sync.Mutex
	turn       int
	current    *Checkpoint
	unreversed []Gap

	// undoPos counts how many checkpoints bare /undo calls have already
	// restored, newest first. So repeating /undo steps back a turn at a time
	// (issue 75). It is a position and leaves no mark on the store. If a
	// restore fails halfway, the user can still retry it by naming its id.
	// A new checkpoint resets undoPos, because the next /undo is then about
	// the turn that just ran.
	undoPos int
}

// New builds the extension, creating the store directory if needed.
func New(cfg Config) (*Extension, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("checkpoint: Config.Root is required")
	}
	store, err := NewStore(cfg.Root)
	if err != nil {
		return nil, err
	}
	writes := make(map[string]WriteSpec, len(cfg.Writes))
	for _, w := range cfg.Writes {
		if w.Tool == "" || w.Paths == nil {
			return nil, fmt.Errorf("checkpoint: WriteSpec needs both Tool and Paths")
		}
		writes[w.Tool] = w
	}
	var tag [3]byte
	if _, err := rand.Read(tag[:]); err != nil {
		return nil, fmt.Errorf("checkpoint: session tag: %w", err)
	}
	return &Extension{
		store:    store,
		writes:   writes,
		proposer: cfg.Proposer,
		approve:  cfg.Approve,
		tools:    cfg.Tools,
		session:  hex.EncodeToString(tag[:]),
	}, nil
}

// Name identifies the extension.
func (e *Extension) Name() string { return "checkpoint" }

// TurnStart begins a new restore point. It does not create the checkpoint.
// The first captured write creates it, so a turn that touches no files costs
// nothing on disk.
func (e *Extension) TurnStart(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.turn++
	e.current = nil
	e.unreversed = nil
	return nil
}

// Middleware captures before a write and records what it could not capture.
func (e *Extension) Middleware() []agent.ToolMiddleware {
	return []agent.ToolMiddleware{e.wrap}
}

func (e *Extension) wrap(ctx context.Context, info agent.ToolCallInfo, next agent.ToolCallFunc) (*core.ToolResult, error) {
	// A sub-agent's writes belong inside the parent's checkpoint. The useful
	// restore point is the turn, not each frame of the tree, and capturing
	// per frame would also mean a child's checkpoint outliving the parent
	// turn that owns it.
	if info.Scope.Depth() > 0 {
		return next(ctx, info)
	}

	spec, writes := e.writes[info.Call.Name]
	if writes {
		// Capture runs before dispatch, the only moment the pre-state
		// exists. This middleware runs outside the host's permission gate, so
		// it captures a call even if the gate then denies it. Restoring that
		// call is a no-op because nothing changed, and a no-op is harmless.
		args := map[string]any{}
		if info.Call.Args.Len() > 0 {
			if err := info.Call.Args.Bind(&args); err != nil {
				return nil, fmt.Errorf("checkpoint: %q arguments are not a JSON object: %w", info.Call.Name, err)
			}
		}
		if paths := spec.Paths(args); len(paths) > 0 {
			cp, err := e.checkpoint()
			if err != nil {
				return nil, err
			}
			if err := cp.Add(paths...); err != nil {
				return nil, err
			}
		}
	}

	res, err := next(ctx, info)

	// Record the gap only for a call that ran and could have changed
	// something. A read-only tool has nothing to undo, and a denied call
	// never happened, so reporting either would train the user to ignore the
	// list.
	if !writes && !info.ReadOnly && !denied(err) {
		e.record(info)
	}
	return res, err
}

func denied(err error) bool {
	var d *agent.ToolDeniedError
	return errors.As(err, &d)
}

func (e *Extension) record(info agent.ToolCallInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unreversed = append(e.unreversed, Gap{
		Tool: info.Call.Name,
		Args: truncate(string(info.Call.Args.Raw()), 120),
	})
}

// checkpoint returns this turn's restore point, creating it on first use.
func (e *Extension) checkpoint() (*Checkpoint, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != nil {
		return e.current, nil
	}
	cp, err := e.store.Open("turn-" + strconv.Itoa(e.turn) + "-" + e.session)
	if err != nil {
		return nil, err
	}
	e.current = cp
	e.undoPos = 0
	return cp, nil
}

// Commands returns /undo and /checkpoints.
func (e *Extension) Commands() []*host.Command {
	return []*host.Command{
		{
			Name: "undo",
			Help: "restore files to the start of a turn (optionally: /undo <checkpoint-id>)",
			Run:  e.runUndo,
		},
		{
			Name: "checkpoints",
			Help: "list restore points, newest first",
			Run:  e.runList,
		},
	}
}

func (e *Extension) runUndo(ctx context.Context, args string) (host.CmdResult, error) {
	id := strings.TrimSpace(args)
	if id == "" {
		chosen, res, ok, err := e.nextUndoTarget()
		if err != nil || !ok {
			return res, err
		}
		id = chosen
	}
	cp, err := e.store.Load(id)
	if err != nil {
		return host.CmdResult{}, fmt.Errorf("checkpoint: no such checkpoint %q: %w", id, err)
	}
	res, err := cp.Restore()
	if err != nil {
		return host.CmdResult{}, err
	}
	// Only a bare /undo advances the position. A named one is a jump, and
	// moving the position for it would make the next bare /undo step from
	// somewhere the user did not walk to.
	if strings.TrimSpace(args) == "" {
		e.mu.Lock()
		e.undoPos++
		e.mu.Unlock()
	}
	report := e.undoReport(cp, res)
	if extra := e.offerProposals(ctx, e.gaps()); extra != "" {
		report += "\n\n" + extra
	}
	return host.CmdResult{Kind: host.CmdMessage, Message: report}, nil
}

// nextUndoTarget picks which checkpoint a bare /undo restores. It goes one turn
// further back than the last bare /undo. ok is false when nothing is left to
// undo, and res then holds the message saying so.
func (e *Extension) nextUndoTarget() (string, host.CmdResult, bool, error) {
	list, err := e.store.List()
	if err != nil {
		return "", host.CmdResult{}, false, err
	}
	e.mu.Lock()
	pos := e.undoPos
	e.mu.Unlock()
	if len(list) == 0 {
		return "", host.CmdResult{Kind: host.CmdMessage, Message: "nothing to undo: no checkpoints yet"}, false, nil
	}
	if pos >= len(list) {
		// Restoring the oldest again would report success for a turn that
		// is already undone.
		return "", host.CmdResult{Kind: host.CmdMessage, Message: "nothing more to undo: every checkpoint has been restored"}, false, nil
	}
	return list[pos].ID, host.CmdResult{}, true, nil
}

// offerProposals asks the proposer for inverse calls, and runs only those that
// a human approves. It returns a transcript to append to the undo report, or
// "" when there was nothing to offer.
//
// Every exit that skips a proposal SAYS so. Imagine a skip that said nothing.
// The user would believe an offset ran. The gap report exists to prevent that
// false confidence, and a silent skip here would bring it back one level down.
func (e *Extension) offerProposals(ctx context.Context, gaps []Gap) string {
	if e.proposer == nil || len(gaps) == 0 {
		return ""
	}
	proposals, err := e.proposer.Propose(ctx, gaps)
	if err != nil {
		return "could not propose offsets: " + err.Error()
	}
	if len(proposals) == 0 {
		return "no offsets proposed."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d offset(s) proposed. None runs without your approval:", len(proposals))
	for _, p := range proposals {
		fmt.Fprintf(&b, "\n  %s %v\n    why: %s", p.Tool, p.Args, p.Rationale)
		switch {
		case e.approve == nil:
			b.WriteString("\n    NOT RUN: no approval prompt is wired")
		case e.tools == nil:
			b.WriteString("\n    NOT RUN: no tool source is wired")
		default:
			ok, err := e.approve(ctx, p)
			if err != nil {
				fmt.Fprintf(&b, "\n    NOT RUN: approval failed: %v", err)
				continue
			}
			if !ok {
				b.WriteString("\n    declined")
				continue
			}
			if _, err := e.tools.Call(ctx, p.Tool, p.Args); err != nil {
				fmt.Fprintf(&b, "\n    FAILED: %v", err)
				continue
			}
			b.WriteString("\n    ran")
		}
	}
	return b.String()
}

func (e *Extension) gaps() []Gap {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Gap(nil), e.unreversed...)
}

// undoReport says what was restored AND what was not.
//
// Imagine a turn edits three files and creates a GitHub issue. A design that
// only knows what it captured reports "3 files restored", and the issue goes
// unmentioned. A safety net with an unreported hole is worse than none,
// because people stop checking it.
//
// There are two kinds of hole. The report lists them separately because the
// reader acts on each one differently. A call with no reverser was never
// undoable, and the answer is a proposed offset. A refused path WAS captured
// and would have been restored. But something changed it after capture, and
// the answer is to go and look at it.
func (e *Extension) undoReport(cp *Checkpoint, res RestoreResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d file(s) restored", cp.ID(), len(res.Restored))

	if len(res.Refused) > 0 {
		fmt.Fprintf(&b, "\n\n%d path(s) REFUSED (not restored):", len(res.Refused))
		for _, r := range res.Refused {
			fmt.Fprintf(&b, "\n  %s — %s", r.Path, r.Reason)
		}
	}

	e.mu.Lock()
	gaps := append([]Gap(nil), e.unreversed...)
	e.mu.Unlock()

	if len(gaps) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "\n\n%d call(s) had no reverser and were NOT undone:", len(gaps))
	for _, g := range gaps {
		fmt.Fprintf(&b, "\n  %s %s", g.Tool, g.Args)
	}
	return b.String()
}

func (e *Extension) runList(_ context.Context, _ string) (host.CmdResult, error) {
	list, err := e.store.List()
	if err != nil {
		return host.CmdResult{}, err
	}
	if len(list) == 0 {
		return host.CmdResult{Kind: host.CmdMessage, Message: "no checkpoints yet"}, nil
	}
	var b strings.Builder
	for i, info := range list {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s  %s  %d file(s)", info.ID, info.Created.Format("15:04:05"), info.Files)
	}
	return host.CmdResult{Kind: host.CmdMessage, Message: b.String()}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
