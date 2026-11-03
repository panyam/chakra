package git

import (
	"context"
	"strings"
	"sync"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

// Extension contributes the git tools, and reports what each turn changed in
// the workspace (#151): it snapshots the work tree when a turn starts and
// again when it ends, announces a one-line summary to every surface, and keeps
// the diff for /diff and for LastTurn.
//
// It reports on version control and restores nothing. ext/checkpoint is
// what puts files back, and the two don't share snapshots.
type Extension struct {
	host.BaseExtension
	src *Source

	mu       sync.Mutex
	before   string // the snapshot TurnStart took, "" when it couldn't
	last     TurnDiff
	announce func(string)
}

// New builds the extension over Config.Root, which must be inside a git work
// tree.
func New(cfg Config) (*Extension, error) {
	src, err := NewSource(cfg)
	if err != nil {
		return nil, err
	}
	return &Extension{src: src}, nil
}

// Name is "git", the source id the tools register under.
func (e *Extension) Name() string { return "git" }

// Tools returns the git tools Config enabled.
func (e *Extension) Tools() (agent.ToolSource, error) { return e.src, nil }

// Source is the tool set, for a caller that wants Diff without going through
// a tool call, such as a review (#152).
func (e *Extension) Source() *Source { return e.src }

// SetAnnounce implements host.Announcer.
func (e *Extension) SetAnnounce(fn func(string)) { e.announce = fn }

// TurnStart snapshots the work tree. A snapshot that fails leaves this turn
// unreported rather than failing it, since the turn's work doesn't depend on
// it.
func (e *Extension) TurnStart(ctx context.Context) error {
	tree, err := e.src.snapshot(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.before = ""
	if err == nil {
		e.before = tree
	}
	return nil
}

// TurnEnd implements host.TurnEnder: it snapshots again, keeps the diff from
// TurnStart's snapshot, and announces its summary when anything changed. A
// failed or partial turn is reported too, since its writes happened all the
// same.
func (e *Extension) TurnEnd(ctx context.Context, _ *agent.TurnResult, _ error) {
	e.mu.Lock()
	before := e.before
	e.before = ""
	e.mu.Unlock()
	if before == "" {
		return
	}
	after, err := e.src.snapshot(ctx)
	if err != nil {
		return
	}
	d, err := e.src.diffTrees(ctx, before, after)
	if err != nil {
		return
	}
	e.mu.Lock()
	e.last = d
	e.mu.Unlock()
	if !d.Empty() && e.announce != nil {
		e.announce("git: " + d.Summary() + "; /diff shows it")
	}
}

// LastTurn is what the most recent turn changed, the zero TurnDiff before
// any turn has ended.
func (e *Extension) LastTurn() TurnDiff {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

// Commands adds /diff, which shows the last turn's changes ("/diff stat" for
// the summary alone).
func (e *Extension) Commands() []*host.Command {
	return []*host.Command{{
		Name: "diff",
		Help: "show what the last turn changed in the workspace (/diff stat for files and line counts)",
		Run: func(_ context.Context, args string) (host.CmdResult, error) {
			return host.CmdResult{Kind: host.CmdMessage, Message: e.diffText(args)}, nil
		},
	}}
}

func (e *Extension) diffText(args string) string {
	d := e.LastTurn()
	switch {
	case d.Before == "":
		return "no turn has ended yet"
	case d.Empty():
		return d.Summary()
	case strings.TrimSpace(args) == "stat":
		return d.Summary() + "\n" + d.Stat
	}
	return d.Summary() + "\n" + d.Patch
}
