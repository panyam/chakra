package surfaces

import (
	"context"
	"fmt"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/agentsmd"
	"github.com/panyam/chakra/ext/checkpoint"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/ext/files"
	"github.com/panyam/chakra/ext/git"
	"github.com/panyam/chakra/ext/lsp"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// WorkspaceConfig describes the workspace tool set a surface exposes.
//
// WorkspaceConfig lives here, not in host. ext/files imports host for its
// approval renderer, so host cannot import the extensions back. agentchat and
// agentweb need identical wiring. One mistake in that wiring fails silently
// (see WorkspaceExtensions), so the wiring is written once, here.
type WorkspaceConfig struct {
	// Roots confine the file tools, and every path they touch is inside one
	// of them. Empty disables the whole set, with no file tools and no
	// checkpoint. The first is primary, and a relative path resolves against
	// it.
	//
	// Roots is a set, not one directory, because most sessions span more than
	// one repository (issue 1314). Imagine an agent with one root. It edits
	// an API and reports success. The caller it broke is in another
	// repository, and the agent cannot see that repository.
	//
	// Unset Roots deliberately does not default to the current directory.
	// files.Config.Roots explains why the tools refuse to run unconfined, and
	// the same reasoning rules out picking the confinement for a user. A model
	// can take instructions from content it read. An editor rooted somewhere
	// nobody named is then one injected instruction away from writing
	// anywhere under that directory. So enabling these tools means naming
	// directories.
	Roots []string

	// Exclude overrides the directories list_files and search_files skip.
	// Nil means files.DefaultExclude; an explicitly empty non-nil slice means
	// exclude nothing.
	Exclude []string

	// EditTools picks the editing tools offered: "edit_file", "apply_patch",
	// or both (nil). See files.Config.EditTools.
	EditTools []string

	// CheckpointStore is where snapshots live. Empty puts them under the
	// primary root.
	//
	// It is separate from Roots. Inside a workspace, the agent could read,
	// search and edit the snapshot store, and the store grows with every
	// write. With several roots there is also no obvious one to pick. A
	// location outside all of them is cleaner, and a caller should usually
	// name one.
	CheckpointStore string

	// NoCheckpoint drops the snapshot-and-restore safety net, leaving the file
	// tools with no /undo. It is opt-out, not opt-in, because checkpoint was
	// built for write tools. Without it their effects cannot be reversed.
	NoCheckpoint bool

	// RepoMap adds a ranked repository map to the system prompt. The map's
	// symbols come from LanguageServers, so RepoMap needs them set.
	//
	// It is opt-in. Indexing takes a round trip per file, and the map adds to
	// the prompt on every turn. The map helps a model find its way around an
	// unfamiliar tree. It is wasted when the operator already knows the shape
	// of the task.
	RepoMap bool

	// LanguageServers adds LSP-in-loop. That means navigation tools, plus
	// diagnostics after every write and in each turn's context. Empty adds
	// nothing.
	//
	// It is opt-in, unlike checkpoint, because it spawns processes. A
	// checkpoint costs a directory, and any session that writes wants one. A
	// language server costs a subprocess, an index of the whole tree, and a
	// few hundred megabytes, so a caller who did not ask for one should not
	// get one. The caller also has to name the servers. Nothing here can
	// guess which languages the workspace holds, or which server the caller
	// wants for each.
	LanguageServers []lsp.ServerSpec

	// Exec adds the allowlisted command tools. Nil adds nothing.
	// WorkspaceExtensions overwrites its Roots with this config's Roots, so
	// commands run and write in the same directories as the file tools.
	//
	// Checkpoint does not cover exec. It snapshots the paths named in a
	// tool's arguments, but a command can write anywhere the process can. An
	// /undo after a command restores only the file-tool edits around it. Mark
	// a command ReadOnly only when it really writes nothing.
	Exec *exec.Config

	// Plan adds update_plan, the model's published checklist (#148).
	Plan bool

	// Git adds ext/git. That always means read-only git tools and a per-turn
	// diff, and its fields can add commit and branch tools, worktrees and
	// review. Nil adds nothing. WorkspaceExtensions overwrites its Root with
	// this config's first root.
	Git *git.Config

	// Offloading says host.Config.Offload is set, so the host stores large
	// tool results. Exec results then hold the whole capture instead of a
	// 32 KiB view, unless Exec.InlineOutput is set. A model can then find a
	// failing assertion in the middle of a long run by searching the store.
	// Set Offloading from the same config that turns offloading on, so the
	// two cannot disagree. A whole capture with no store behind it puts up to
	// 2 MiB in context. A PerTool entry that pins a run_ tool inline has the
	// same effect.
	Offloading bool

	// Instructions says how to find and load the repository's instruction
	// files, such as AGENTS.md. Nil uses agentsmd.DefaultPreset. Set it from
	// agentsmd.Preset to copy another tool's rules. The files load whenever
	// there are roots. Imagine a repository whose AGENTS.md says "never edit
	// the tests". Its authors meant that for every agent working there.
	Instructions *agentsmd.Profile

	// NoInstructions skips the instruction files altogether.
	NoInstructions bool

	// SkillDirs lists the folders to load local skills from (see
	// WorkspaceSkillDirs). Nil uses SkillsPreset's list, and an empty non-nil
	// list reads none.
	SkillDirs []string

	// SkillsPreset names the starting list when SkillDirs is nil: "chakra"
	// (the default), "codex" or "claude". See SkillPresets.
	SkillsPreset string
}

// WorkspaceExtensions builds the file and checkpoint extensions for cfg, plus
// exec and LSP when configured.
//
// Returns nil when cfg.Roots is empty, so a caller can pass the result to
// host.WithExtension unconditionally and get today's behaviour when no
// workspace was requested. Exec or language servers without Roots is an
// error, not nil. The caller asked for them. Dropping them silently would
// leave a session without those tools, and nobody would be told.
//
// # Order matters
//
// Checkpoint comes first in the returned slice. Extensions are applied in
// registration order and their middleware keeps it, so the snapshot must be
// installed ahead of the tool that writes. Reversed, the middleware captures
// the file *after* the write and /undo restores the damage.
//
// Nothing catches that. Both orders construct, both run, and the difference
// only shows up as an /undo that appears to work and does not. So the two
// extensions are built together here. Otherwise each surface would register
// them in whatever order reads well.
//
// # Why the two are coupled
//
// Checkpoint's WriteSpec list is per tool, so with no file tools it guards
// nothing. File tools without checkpoint lose /undo, and ext/checkpoint
// exists to provide it. Wiring them separately would let a surface enable
// half of a safety property.
//
// fx.WritePaths connects them. Checkpoint and lsp need to know which file a
// call will write. The files tools resolve paths through fx.WritePaths too,
// so all three agree on that file whatever the process's working directory
// is (issue 69). Neither module imports the other.
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

	// Use files.New, not files.NewSource. The extension adds a prompt section
	// that describes the read-then-edit contract. With the tools alone, a
	// model discovers that contract only by failing an edit. fx is built
	// first because checkpoint and lsp resolve paths through it. The
	// registration order below does not change.
	fx, err := files.New(files.Config{Roots: cfg.Roots, Exclude: cfg.Exclude, EditTools: cfg.EditTools})
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
		if cfg.Offloading && ec.InlineOutput == 0 {
			ec.InlineOutput = -1
		}
		ex, err := exec.New(ec)
		if err != nil {
			return nil, fmt.Errorf("workspace exec: %w", err)
		}
		exts = append(exts, ex)
	}

	if len(cfg.LanguageServers) > 0 {
		// After checkpoint, for the mirror image of checkpoint's reason.
		// Checkpoint must see a file before it is written, and this must see
		// it after. So its middleware has to sit inside checkpoint's, and
		// registration order puts it there. agentsmd registers later and adds
		// middleware inside this one, but it only reads files, so the write
		// has happened by the time this middleware's next call returns.
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
				{Tool: files.PatchTool, Paths: fx.WritePaths},
			},
		})
		if lspErr != nil {
			// Nothing built so far outlives this function except the
			// half-started language servers, and lsp.New has already
			// cleaned those up.
			return nil, fmt.Errorf("workspace lsp: %w", lspErr)
		}
		exts = append(exts, lx)
	}

	if cfg.Plan {
		exts = append(exts, host.NewPlanExtension())
	}

	if cfg.Git != nil {
		gc := *cfg.Git
		gc.Root = cfg.Roots[0]
		gx, err := git.New(gc)
		if err != nil {
			return nil, fmt.Errorf("workspace git: %w", err)
		}
		exts = append(exts, gx)
	}

	if !cfg.NoInstructions {
		profile := cfg.Instructions
		if profile == nil {
			p, err := agentsmd.Preset(agentsmd.DefaultPreset)
			if err != nil {
				return nil, err
			}
			profile = &p
		}
		ax, err := agentsmd.New(agentsmd.Config{Roots: cfg.Roots, Profile: *profile})
		if err != nil {
			return nil, fmt.Errorf("workspace instructions: %w", err)
		}
		exts = append(exts, ax)
	}

	return exts, nil
}

// fileWrites lists the files tools that write. The checkpoint snapshots their
// writes, so restorable marks them reversible. Keeping one list means no tool
// is marked reversible without being snapshotted.
// delete_file is on it because /undo can restore a deleted file only from
// the snapshot (issue 108).
// apply_patch is on it because a patch can add, change, move and delete
// files, and the snapshot covers all of them.
var fileWrites = []string{"write_file", "edit_file", "delete_file", files.PatchTool}

// restorable wraps the files extension when a checkpoint covers its writes.
// It sets destructiveHint false on those writes, so reversible-auto runs them
// unasked. files cannot set this itself. /undo can take a write back only if
// this wiring adds a checkpoint. With --no-checkpoint the writes are not
// wrapped, and they are still destructive. A11 lets only a true inverse run
// unattended, and a checkpoint restore is one.
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
