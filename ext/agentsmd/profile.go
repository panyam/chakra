package agentsmd

import (
	"fmt"
	"sort"
)

// Walk says how far above a workspace root the startup chain looks.
type Walk string

const (
	// WalkMarker climbs to the nearest directory holding one of
	// Profile.Markers (the project root) and loads from there down to the
	// workspace root. With no marker above the workspace root, only the
	// workspace root itself is read.
	WalkMarker Walk = "marker"

	// WalkFSRoot loads from every directory above the workspace root, up to
	// but not including the filesystem root.
	WalkFSRoot Walk = "fs-root"
)

// Subdirs says whether files below a workspace root are read.
type Subdirs string

const (
	// SubdirsOff reads only the startup chain.
	SubdirsOff Subdirs = "off"

	// SubdirsLazy reads a subdirectory's file the first time a tool call
	// names a path in that subdirectory, and shows it to the model from the
	// next model call on.
	SubdirsLazy Subdirs = "lazy"
)

// Changes says what happens when a loaded file changes during a session.
type Changes string

const (
	// ChangesFreeze reads each file once. Later edits apply from the next
	// session.
	ChangesFreeze Changes = "freeze"

	// ChangesNotice checks every directory read so far before each model
	// call. A file that changed, appeared or was deleted is shown to the
	// model with a note saying which version it replaces.
	ChangesNotice Changes = "notice"
)

// Profile is one tool's rules for finding and loading instruction files.
// Presets holds named ones; a zero field in a profile given to New is taken
// as written, so start from a preset and change what differs.
type Profile struct {
	// Names are tried in order in each directory, and the first one present
	// and non-empty is read. A name may include a directory
	// (".claude/CLAUDE.md").
	Names []string `json:"names,omitempty"`

	// Override, when present in a directory, is read instead of Names.
	Override string `json:"override,omitempty"`

	// Local is a personal, usually uncommitted file read after the
	// directory's main file.
	Local string `json:"local,omitempty"`

	// Fallback names are read only when no directory in the startup chain
	// has a Names, Override or Local file. A lazily read subdirectory uses
	// them when it has none of its own. Claude Code treats AGENTS.md beside
	// CLAUDE.md the same way.
	Fallback []string `json:"fallback,omitempty"`

	// GlobalDir holds the user's own file, read before everything else with
	// the same Names and Override rules. A leading "~" is the home
	// directory. Empty reads none.
	GlobalDir string `json:"globalDir,omitempty"`

	// Walk and Markers decide where the startup chain begins. Markers
	// default to [".git"] under WalkMarker.
	Walk    Walk     `json:"walk,omitempty"`
	Markers []string `json:"markers,omitempty"`

	// PrimaryOnly reads the startup chain for the first workspace root only,
	// as a tool with one working directory does.
	PrimaryOnly bool `json:"primaryOnly,omitempty"`

	Subdirs Subdirs `json:"subdirs,omitempty"`
	Changes Changes `json:"changes,omitempty"`

	// Imports expands "@path" references, Claude Code style: relative to the
	// file holding them, outside code spans and fenced blocks, at most four
	// hops deep. A project file may import only from inside the workspace
	// roots; the global file may import from anywhere.
	Imports bool `json:"imports,omitempty"`

	// StripComments drops block-level HTML comments outside code blocks, so
	// notes for human maintainers cost no context.
	StripComments bool `json:"stripComments,omitempty"`

	// MaxBytes caps the text of every file loaded, together. A file that
	// crosses the cap is cut, and later ones are named as skipped. Zero or
	// less means no cap.
	MaxBytes int `json:"maxBytes,omitempty"`
}

// DefaultPreset is the profile WorkspaceExtensions uses when none is named.
const DefaultPreset = "chakra"

// Presets are profiles that copy how other coding agents load their
// instruction files, plus chakra's own default.
//
//   - codex follows the Codex CLI: ~/.codex first, then one file per
//     directory from the git root down to the working directory,
//     AGENTS.override.md over AGENTS.md, skipping empty files, 32 KiB
//     combined, read once per run.
//   - claude follows Claude Code: ~/.claude/CLAUDE.md, then CLAUDE.md and
//     CLAUDE.local.md in every directory above the working directory,
//     AGENTS.md only when there is no CLAUDE.md, subdirectory files once the
//     agent touches a file there, @path imports and stripped HTML comments,
//     no size cap. Claude Code re-reads its files after a compaction; this
//     preset reads them once, which is the same until a session compacts.
//   - chakra is Codex's file rules with lazy subdirectories and change
//     notices, over every workspace root.
var Presets = map[string]Profile{
	"codex": {
		Names:       []string{"AGENTS.md"},
		Override:    "AGENTS.override.md",
		GlobalDir:   "~/.codex",
		Walk:        WalkMarker,
		Markers:     []string{".git"},
		PrimaryOnly: true,
		Subdirs:     SubdirsOff,
		Changes:     ChangesFreeze,
		MaxBytes:    32 << 10,
	},
	"claude": {
		Names:         []string{"CLAUDE.md", ".claude/CLAUDE.md"},
		Local:         "CLAUDE.local.md",
		Fallback:      []string{"AGENTS.md", ".claude/AGENTS.md"},
		GlobalDir:     "~/.claude",
		Walk:          WalkFSRoot,
		PrimaryOnly:   true,
		Subdirs:       SubdirsLazy,
		Changes:       ChangesFreeze,
		Imports:       true,
		StripComments: true,
	},
	"chakra": {
		Names:    []string{"AGENTS.md", "CLAUDE.md"},
		Override: "AGENTS.override.md",
		Walk:     WalkMarker,
		Markers:  []string{".git"},
		Subdirs:  SubdirsLazy,
		Changes:  ChangesNotice,
		MaxBytes: 32 << 10,
	},
}

// Preset returns a copy of the named preset, so a caller can change its
// fields without touching Presets.
func Preset(name string) (Profile, error) {
	p, ok := Presets[name]
	if !ok {
		names := make([]string, 0, len(Presets))
		for n := range Presets {
			names = append(names, n)
		}
		sort.Strings(names)
		return Profile{}, fmt.Errorf("agentsmd: no preset %q (have %v)", name, names)
	}
	p.Names = append([]string(nil), p.Names...)
	p.Fallback = append([]string(nil), p.Fallback...)
	p.Markers = append([]string(nil), p.Markers...)
	return p, nil
}

func (p Profile) validate() error {
	if len(p.Names) == 0 && p.Override == "" && p.Local == "" && len(p.Fallback) == 0 {
		return fmt.Errorf("agentsmd: profile names no files")
	}
	switch p.Walk {
	case WalkMarker, WalkFSRoot:
	default:
		return fmt.Errorf("agentsmd: walk %q, want %q or %q", p.Walk, WalkMarker, WalkFSRoot)
	}
	switch p.Subdirs {
	case SubdirsOff, SubdirsLazy:
	default:
		return fmt.Errorf("agentsmd: subdirs %q, want %q or %q", p.Subdirs, SubdirsOff, SubdirsLazy)
	}
	switch p.Changes {
	case ChangesFreeze, ChangesNotice:
	default:
		return fmt.Errorf("agentsmd: changes %q, want %q or %q", p.Changes, ChangesFreeze, ChangesNotice)
	}
	return nil
}
