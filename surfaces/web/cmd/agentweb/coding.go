package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/chakra/surfaces/coding"
	web "github.com/panyam/chakra/surfaces/web"
)

// codingOptions are the --coding flags. With dir set, agentweb serves the
// coding agent at /code/ beside chat (#178).
type codingOptions struct {
	// dir holds the coding agent's config.json and workspace.json.
	dir string
	// workspace is the repository every coding session works in. fixture is
	// copied into a fresh repository for each session instead. Exactly one
	// is set.
	workspace string
	fixture   string
	// active and model pick the config's connection and its model.
	active string
	model  string
	// instructions overrides the workspace file's agentsmd preset.
	instructions string
	// stateDir holds a folder per session. Empty uses the system temp dir.
	stateDir string
	// store is the server's session store, so coding sessions persist like
	// chat ones. Nil keeps them in memory.
	store agent.RunStore
}

// codingProfile checks the --coding flags and returns the profile agentweb
// registers. Each session gets its own folder outside the workspace, named by
// its session id, with its checkpoints and, under --coding-fixture, its copy
// of the fixture. The agent can't read or edit its own snapshots there. With
// a session store the folder outlives a restart, so the session resumes on
// the same files. Without one, closing the session removes the folder.
func codingProfile(o codingOptions) (web.Profile, error) {
	for _, f := range []string{coding.ConfigFile, coding.WorkspaceFile} {
		if _, err := os.Stat(filepath.Join(o.dir, f)); err != nil {
			return web.Profile{}, fmt.Errorf("--coding %s: %w", o.dir, err)
		}
	}
	switch {
	case o.workspace == "" && o.fixture == "":
		return web.Profile{}, errors.New("--coding needs --coding-workspace (a repository to work in) or --coding-fixture (copied fresh for each session)")
	case o.workspace != "" && o.fixture != "":
		return web.Profile{}, errors.New("--coding-workspace and --coding-fixture can't both be set")
	}
	if err := checkInstructions(o.instructions); err != nil {
		return web.Profile{}, err
	}
	base := o.stateDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "agentweb-coding")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return web.Profile{}, err
	}
	// The file tools compare resolved paths, and macOS temp dirs sit behind
	// a /var -> /private/var symlink.
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		return web.Profile{}, err
	}
	build := func(ctx context.Context, id string) (*host.App, error) {
		dir := filepath.Join(base, id)
		_, err := os.Stat(dir)
		resuming := err == nil
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		root := o.workspace
		if o.fixture != "" {
			root = filepath.Join(dir, "repo")
			if resuming {
				if _, err := os.Stat(root); err != nil {
					return nil, fmt.Errorf("session %s: its copy of the fixture is gone: %w", id, err)
				}
			} else if err := coding.CopyFixture(o.fixture, root); err != nil {
				return nil, err
			}
		}
		return coding.Profile{
			Dir:             o.dir,
			Root:            root,
			Active:          o.active,
			Model:           o.model,
			Instructions:    o.instructions,
			CheckpointStore: filepath.Join(dir, "checkpoints"),
			RunStore:        o.store,
		}.Build(ctx)
	}
	remove := func(id string) { os.RemoveAll(filepath.Join(base, id)) }
	return web.Profile{Name: "coding", Path: "/code/", Build: build, Close: remove}, nil
}
