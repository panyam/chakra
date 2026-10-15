package surfaces

import (
	"context"
	"fmt"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/checkpoint"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/ext/files"
	"github.com/panyam/chakra/ext/lsp"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// WorkspaceConfig describes the workspace tool set a surface exposes.
//
// It lives here rather than in host because agent/ext/files imports host for
// its approval renderer, so host cannot import the extensions back. Both
// agentchat and agentweb need the identical wiring, and getting it wrong is
// silent in one specific way (see WorkspaceExtensions), so it is written once.
type WorkspaceConfig struct {
	// Roots confine every path the file tools will touch. Empty disables the
	// whole set: no file tools, no checkpoint. The first is primary, and a
	// relative path resolves against it.
	//
	// A set rather than one directory because a session that stays inside a
	// single repository is the exception (issue 1314). An agent given one root
	// edits an API and reports success while the caller it broke sits in
	// another repository it cannot see.
	//
	// There is deliberately no "unset means the current directory" default.
	// files.Config.Roots documents why the tools themselves refuse to run
	// unconfined, and the same reasoning applies to choosing the confinement
	// for a user: a model's instructions can come from content it read, so an
	// editor rooted somewhere nobody named is an injected instruction away
	// from writing anywhere under it. Enabling these is a choice with
	// directories attached.
	Roots []string

	// Exclude overrides the directories list_files and search_files skip.
	// Nil means files.DefaultExclude; an explicitly empty non-nil slice means
	// exclude nothing.
	Exclude []string

	// CheckpointStore is where snapshots live. Empty puts them under the
	// primary root.
	//
	// It is separate from Roots because a snapshot store inside a workspace is
	// a directory the agent can read, search, and edit, and one that grows
	// with every write. With several roots there is also no obvious one to
	// pick. Naming a location outside them all is the cleaner arrangement and
	// is what a caller should usually do.
	CheckpointStore string

	// NoCheckpoint drops the snapshot-and-restore safety net, leaving the file
	// tools with no /undo. Opt-out rather than opt-in because a write tool
	// whose effects cannot be reversed is the case checkpoint was built for.
	NoCheckpoint bool

	// RepoMap adds a ranked repository map to the system prompt, which needs
	// LanguageServers to be set: the map's symbols come from them.
	//
	// Opt-in because indexing costs a round trip per file, and because a map
	// is prompt weight every turn. It is worth it for orientation on an
	// unfamiliar tree and wasted on a task the operator already knows the
	// shape of.
	RepoMap bool

	// LanguageServers adds LSP-in-loop: navigation tools, and diagnostics
	// after every write and in each turn's context. Empty adds nothing.
	//
	// Opt-in, unlike checkpoint, because this one spawns processes. A
	// checkpoint costs a directory and is wanted wherever writes are, while a
	// language server the caller did not ask for is a subprocess, an index of
	// the whole tree, and a few hundred megabytes. Naming the servers is also
	// unavoidable: nothing here can guess which languages the workspace holds
	// or which server the caller wants driving them.
	LanguageServers []lsp.ServerSpec

	// Exec adds the allowlisted command tools. Nil adds nothing. Its Roots
	// are overwritten with this config's Roots, so commands run in, and write
	// to, the same directories the file tools are confined to.
	//
	// Checkpoint does not cover exec. It snapshots the paths a tool names in
	// its arguments, and a command's writes are whatever the process does,
	// so an /undo after a command restores only the file-tool edits around
	// it. Mark commands ReadOnly only when that is true of them.
	Exec *exec.Config
}

// WorkspaceExtensions builds the file and checkpoint extensions for cfg, plus
// exec and LSP when configured.
//
// Returns nil when cfg.Roots is empty, so a caller can pass the result to
// host.WithExtension unconditionally and get today's behaviour when no
// workspace was requested. Exec or language servers without Roots is an
// error rather than nil: they were asked for, and dropping them silently
// would leave a session missing tools nobody is told about.
//
// # Order is load-bearing
//
// Checkpoint comes first in the returned slice. Extensions are applied in
// registration order and their middleware keeps it, so the snapshot must be
// installed ahead of the tool that writes. Reversed, the middleware captures
// the file *after* the write and /undo restores the damage.
//
// Nothing catches that. Both orders construct, both run, and the difference
// only shows up as an /undo that appears to work and does not, which is why
// the two extensions are built together here instead of being left to each
// surface to register in whatever order reads well.
//
// # Why the two are coupled
//
// Checkpoint's WriteSpec list is per-tool, so with no file tools it guards
// nothing; and file tools without it drop the reversal path that agent/ext/
// checkpoint exists to provide. Wiring them separately would let a surface
// enable half of a safety property.
//
// fx.WritePaths joins them: checkpoint and lsp need the file a call will
// write, and files exports the resolver its own tools use, so all three agree
// on which file that is whatever the process's working directory (issue 69).
// Neither module imports the other.
func WorkspaceExtensions(cfg WorkspaceConfig) ([]host.Extension, error) {
	if len(cfg.Roots) == 0 {
		if cfg.Exec != nil || len(cfg.LanguageServers) > 0 {
			return nil, fmt.Errorf("workspace: exec and language servers need at least one workspace root")
		}
		return nil, nil
	}

	var exts []host.Extension

	store := cfg.CheckpointStore
	if store == "" {
		store = cfg.Roots[0]
	}

	// files.New rather than files.NewSource: the extension carries the prompt
	// section describing the read-then-edit contract along with the tools.
	// Registering the tools alone leaves that contract discoverable only by
	// failing an edit. Built first because checkpoint and lsp resolve paths
	// through it; registration order below is unchanged.
	fx, err := files.New(files.Config{Roots: cfg.Roots, Exclude: cfg.Exclude})
	if err != nil {
		return nil, fmt.Errorf("workspace files: %w", err)
	}

	if !cfg.NoCheckpoint {
		var writes []checkpoint.WriteSpec
		for _, tool := range fileWrites {
			writes = append(writes, checkpoint.WriteSpec{Tool: tool, Paths: fx.WritePaths})
		}
		cx, err := checkpoint.New(checkpoint.Config{Root: store, Writes: writes})
		if err != nil {
			return nil, fmt.Errorf("workspace checkpoint: %w", err)
		}
		exts = append(exts, cx)
	}

	if cfg.NoCheckpoint {
		exts = append(exts, fx)
	} else {
		exts = append(exts, restorable{Extension: fx})
	}

	if cfg.Exec != nil {
		ec := *cfg.Exec
		ec.Roots = cfg.Roots
		ex, err := exec.New(ec)
		if err != nil {
			return nil, fmt.Errorf("workspace exec: %w", err)
		}
		exts = append(exts, ex)
	}

	if len(cfg.LanguageServers) > 0 {
		// Last, and for the mirror image of checkpoint's reason. Checkpoint
		// must see a file before it is written; this must see it after. Its
		// middleware therefore has to sit innermost, which registration order
		// gives it.
		var mapCfg *lsp.RepoMapConfig
		if cfg.RepoMap {
			mapCfg = &lsp.RepoMapConfig{}
		}
		lx, lspErr := lsp.New(lsp.Config{
			Roots:   cfg.Roots,
			RepoMap: mapCfg,
			Servers: cfg.LanguageServers,
			Writes: []lsp.WriteSpec{
				{Tool: "write_file", Paths: fx.WritePaths},
				{Tool: "edit_file", Paths: fx.WritePaths},
			},
		})
		if lspErr != nil {
			// Everything built so far owns nothing that outlives this
			// function except the language servers we failed to finish
			// starting, which lsp.New has already cleaned up.
			return nil, fmt.Errorf("workspace lsp: %w", lspErr)
		}
		exts = append(exts, lx)
	}

	return exts, nil
}

// fileWrites are the files tools that write, which the checkpoint snapshots
// and which restorable therefore marks reversible. One list, so a tool cannot
// be marked reversible without also being snapshotted.
var fileWrites = []string{"write_file", "edit_file"}

// restorable wraps the files extension when a checkpoint covers its writes,
// and declares those writes reversible (destructiveHint false) so
// reversible-auto runs them unasked. files cannot say this itself: whether
// /undo can take a write back depends on the checkpoint this wiring adds, and
// --no-checkpoint leaves the writes unwrapped, so they stay destructive.
// A checkpoint restore is a true inverse, which is what A11 asks of anything
// that runs unattended.
type restorable struct{ host.Extension }

func (r restorable) Tools() (agent.ToolSource, error) {
	src, err := r.Extension.Tools()
	if err != nil || src == nil {
		return src, err
	}
	return restorableSource{src}, nil
}

type restorableSource struct{ agent.ToolSource }

func (s restorableSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	defs, err := s.ToolSource.Tools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]core.ToolDef, len(defs))
	for i, d := range defs {
		for _, w := range fileWrites {
			if d.Name != w {
				continue
			}
			ann := make(map[string]any, len(d.Annotations)+1)
			for k, v := range d.Annotations {
				ann[k] = v
			}
			ann["destructiveHint"] = false
			d.Annotations = ann
		}
		out[i] = d
	}
	return out, nil
}
