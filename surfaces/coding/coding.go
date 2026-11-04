// Package coding is chakra's coding agent: a host.App built from a config
// file, a workspace file and a workspace root, with no harness code of its
// own. examples/coding-harness drives it over agentweb's Connect API, and
// agentweb serves it at /code/ beside chat (#178).
//
// Everything that makes the agent differ from agentchat --workspace is a
// choice of config, extension, tool or provider. Each mission ticket swaps a
// piece in when it merges, and nothing here should need to change how a
// turn runs.
package coding

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/agentsmd"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/host"
	agentsurfaces "github.com/panyam/chakra/surfaces"
	"github.com/panyam/mcpkit/core"
)

// ConfigFile and WorkspaceFile are the files Profile.Dir holds.
const (
	ConfigFile    = "config.json"
	WorkspaceFile = "workspace.json"
)

// Profile describes one coding-agent session. Build turns it into an App.
type Profile struct {
	// Dir holds ConfigFile and WorkspaceFile. Required.
	Dir string

	// Root is the workspace the file and exec tools are confined to.
	// Required.
	Root string

	// Provider replaces the config's connection when set, as the harness's
	// scripted model does.
	Provider agent.Provider

	// Active names the config connection to build when Provider is nil.
	// Empty uses the config's active connection. Model overrides that
	// connection's model.
	Active string
	Model  string

	// Approval overrides the config's approval mode, such as "allow" for a
	// headless run. Empty keeps the config's.
	Approval string

	// CheckpointStore is where file snapshots go. It should sit outside Root,
	// so the agent can't read or edit them. Empty puts them under Root.
	CheckpointStore string

	// Instructions names the agentsmd preset that loads AGENTS.md and the
	// like, over the workspace file's choice. Empty keeps the file's.
	Instructions string

	// Sandbox replaces the workspace file's exec sandbox when set. The
	// harness passes StandInSandbox where the platform has none.
	Sandbox exec.Sandbox

	// WrapProvider, when set, wraps the provider the session uses, such as a
	// recorder that keeps every request.
	WrapProvider func(agent.Provider) agent.Provider

	// Extensions adds extensions once the config is loaded and the provider
	// is built. The harness uses it for step routing, which reads the
	// config's other connections.
	Extensions func(cfg *host.Config) ([]host.Extension, error)

	// ElicitationUI answers asks. Nil leaves every ask to a surface's
	// RespondToAsk (AwaitSurface), as agentweb needs.
	ElicitationUI agent.ElicitationUI

	// RunStore persists the session's turns, as host.WithRunStore does, so
	// a surface can resume it after a restart. Nil keeps it in memory.
	RunStore agent.RunStore
}

// Build makes the session's App. It is shaped as a SessionManager factory.
func (p Profile) Build(context.Context) (*host.App, error) {
	if p.Dir == "" || p.Root == "" {
		return nil, fmt.Errorf("coding: Dir and Root are required")
	}
	cfg, err := host.LoadConfig(filepath.Join(p.Dir, ConfigFile))
	if err != nil {
		return nil, fmt.Errorf("coding: load config: %w", err)
	}
	if p.Approval != "" {
		if cfg.Approval == nil {
			cfg.Approval = &host.ApprovalConfig{}
		}
		cfg.Approval.Mode = p.Approval
	}

	prov := p.Provider
	if prov == nil {
		name := p.Active
		if name == "" && cfg.Connections != nil {
			name = cfg.Connections.Active
		}
		var model string
		prov, model, err = BuildConnection(cfg, name, p.Model)
		if err != nil {
			return nil, err
		}
		cfg.Model = host.ModelConfig{BaseURL: name, Model: model}
		if p.WrapProvider == nil {
			// The host builds the provider from the connection list, so a
			// surface's model picker can switch between them mid-session
			// (#244). BuildConnection above only checked the active one.
			cfg.Connections.Active = name
			conn := cfg.Connections.Connections[name]
			conn.Model = model
			cfg.Connections.Connections[name] = conn
			prov = nil
		}
	}
	var extra []host.Extension
	if p.Extensions != nil {
		if extra, err = p.Extensions(cfg); err != nil {
			return nil, err
		}
	}
	if prov != nil {
		// The provider is given directly, so the connection list would only
		// offer switches that bypass Provider or WrapProvider.
		cfg.Connections = nil
	}
	if prov != nil && p.WrapProvider != nil {
		prov = p.WrapProvider(prov)
	}

	ws := agentsurfaces.WorkspaceConfig{Roots: []string{p.Root}, CheckpointStore: p.CheckpointStore}
	if err := ws.LoadFile(filepath.Join(p.Dir, WorkspaceFile)); err != nil {
		return nil, err
	}
	if p.Instructions != "" {
		pr, err := agentsmd.Preset(p.Instructions)
		if err != nil {
			return nil, err
		}
		ws.Instructions, ws.NoInstructions = &pr, false
	}
	if p.Sandbox != nil && ws.Exec != nil {
		ws.Exec.Sandbox = p.Sandbox
	}
	exts, err := agentsurfaces.WorkspaceExtensions(ws)
	if err != nil {
		return nil, err
	}
	skillDirs, err := agentsurfaces.WorkspaceSkillDirs(ws)
	if err != nil {
		return nil, err
	}
	ui := p.ElicitationUI
	if ui == nil {
		ui = AwaitSurface
	}
	opts := []host.AppOption{
		host.WithExtension(append(exts, extra...)...),
		host.WithSkillDirs(skillDirs...),
		host.WithRoots(ws.Roots...),
		host.WithElicitationUI(ui),
	}
	if prov != nil {
		opts = append(opts, host.WithProvider(prov))
	}
	if p.RunStore != nil {
		opts = append(opts, host.WithRunStore(p.RunStore))
	}
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), opts...)
}

// BuildConnection builds the config connection called name, with model
// replacing its model when set. It returns the model it ended up with. A
// connection whose APIKeyEnv is unset fails here and names the variable,
// rather than failing on the first model call.
func BuildConnection(cfg *host.Config, name, model string) (agent.Provider, string, error) {
	if cfg.Connections == nil {
		return nil, "", fmt.Errorf("coding: config has no connections")
	}
	conn, ok := cfg.Connections.Connections[name]
	if !ok {
		return nil, "", fmt.Errorf("coding: no connection %q in the config", name)
	}
	if model != "" {
		conn.Model = model
	}
	if conn.APIKeyEnv != "" && os.Getenv(conn.APIKeyEnv) == "" {
		return nil, "", fmt.Errorf("coding: connection %q needs %s set", name, conn.APIKeyEnv)
	}
	prov, err := host.DefaultProviderBuilder(conn)
	if err != nil {
		return nil, "", fmt.Errorf("coding: connection %q: %w", name, err)
	}
	return prov, conn.Model, nil
}

// AwaitSurface leaves every ask to a surface's RespondToAsk. A web App has no
// terminal to read an answer from, so it waits until a surface answers or
// the turn is cancelled.
func AwaitSurface(ctx context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
	<-ctx.Done()
	return core.ElicitationResult{}, ctx.Err()
}

// StandInSandbox counts as a sandbox but confines nothing. It is for the
// harness on a platform with no backend (CI is Linux, and exec's backend is
// darwin-only). exec.Unconfined() turns escalation off (#203). This one
// doesn't, so the shell tool still offers escalation and an escalated call
// still goes to the approval ask, and the scripted run covers the same
// wiring on every platform. Don't use it where a person expects confinement.
type StandInSandbox struct{}

// Name implements exec.Sandbox.
func (StandInSandbox) Name() string { return "stand-in" }

// Available implements exec.Sandbox. It is always available.
func (StandInSandbox) Available() error { return nil }

// Confine implements exec.Sandbox. It leaves the command as it is.
func (StandInSandbox) Confine(*osexec.Cmd, exec.Policy) error { return nil }

// CopyFixture copies src into a new directory dst and commits it as a git
// repository, as a real checkout would be. ext/git needs a repository, and
// the agent's changes then show as uncommitted. It needs git on PATH.
func CopyFixture(src, dst string) error {
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"},
	} {
		if out, err := osexec.Command("git", append([]string{"-C", dst}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("coding: git %s: %v\n%s", args[0], err, out)
		}
	}
	return nil
}
