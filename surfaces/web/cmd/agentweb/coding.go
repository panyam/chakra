package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
}

// codingProfile checks the --coding flags and returns the profile agentweb
// registers. Each session gets its own checkpoint folder outside the
// workspace, so the agent can't read or edit its own snapshots.
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
	build := func(ctx context.Context) (*host.App, error) {
		tmp, err := os.MkdirTemp("", "agentweb-coding-")
		if err != nil {
			return nil, err
		}
		// The file tools compare resolved paths, and macOS temp dirs sit
		// behind a /var -> /private/var symlink.
		if tmp, err = filepath.EvalSymlinks(tmp); err != nil {
			return nil, err
		}
		root := o.workspace
		if o.fixture != "" {
			root = filepath.Join(tmp, "repo")
			if err := coding.CopyFixture(o.fixture, root); err != nil {
				return nil, err
			}
		}
		return coding.Profile{
			Dir:             o.dir,
			Root:            root,
			Active:          o.active,
			Model:           o.model,
			Instructions:    o.instructions,
			CheckpointStore: filepath.Join(tmp, "checkpoints"),
		}.Build(ctx)
	}
	return web.Profile{Name: "coding", Path: "/code/", Build: build}, nil
}
