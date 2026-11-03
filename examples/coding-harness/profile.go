package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/agentsmd"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/host"
	agentsurfaces "github.com/panyam/chakra/surfaces"
	"github.com/panyam/mcpkit/core"
)

// profile is the coding agent: everything that makes it differ from
// agentchat --workspace lives here, as a choice of config, extension, tool or
// provider. Each mission ticket swaps a piece in as it lands, and nothing in
// this file should ever need to change how a turn runs.
type profile struct {
	// dir holds config.json and workspace.json.
	dir string
	// root is the workspace the file and exec tools are confined to.
	root string
	// provider replaces the config's connection when set (the scripted model).
	provider agent.Provider
	// active names the config connection to build when provider is nil.
	active string
	// model overrides the active connection's model.
	model string
	// approval overrides the config's approval mode ("allow" when headless).
	approval string
	// unconfined runs exec commands with no sandbox, for platforms with no
	// backend (CI is Linux; the backend is darwin-only).
	unconfined bool
	// checkpoints is where file snapshots go, outside root.
	checkpoints string
	// instructions names the agentsmd preset that loads AGENTS.md and the
	// like, over workspace.json's choice. Empty keeps workspace.json's.
	instructions string
	// recorder sees every model request, so checks can read what the model
	// was sent. Nil records nothing.
	recorder *recorder
}

// build is the SessionManager factory: one host.App per session.
func (p profile) build(context.Context) (*host.App, error) {
	cfg, err := host.LoadConfig(filepath.Join(p.dir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if p.approval != "" {
		if cfg.Approval == nil {
			cfg.Approval = &host.ApprovalConfig{}
		}
		cfg.Approval.Mode = p.approval
	}

	prov := p.provider
	if prov == nil {
		prov, err = p.connectionProvider(cfg)
		if err != nil {
			return nil, err
		}
	}
	// The provider is given directly, so the connection list would only
	// offer switches the recorder cannot see.
	cfg.Connections = nil
	if p.recorder != nil {
		prov = &recordingProvider{Provider: prov, rec: p.recorder}
	}

	ws := agentsurfaces.WorkspaceConfig{Roots: []string{p.root}, CheckpointStore: p.checkpoints}
	if err := ws.LoadFile(filepath.Join(p.dir, "workspace.json")); err != nil {
		return nil, err
	}
	if p.instructions != "" {
		pr, err := agentsmd.Preset(p.instructions)
		if err != nil {
			return nil, err
		}
		ws.Instructions, ws.NoInstructions = &pr, false
	}
	if p.unconfined && ws.Exec != nil {
		ws.Exec.Sandbox = exec.Unconfined()
	}
	exts, err := agentsurfaces.WorkspaceExtensions(ws)
	if err != nil {
		return nil, err
	}
	skillDirs, err := agentsurfaces.WorkspaceSkillDirs(ws)
	if err != nil {
		return nil, err
	}
	return host.NewApp(cfg, io.Discard, strings.NewReader(""),
		host.WithExtension(exts...),
		host.WithSkillDirs(skillDirs...),
		host.WithRoots(ws.Roots...),
		host.WithProvider(prov),
		host.WithElicitationUI(awaitSurface),
	)
}

// connectionProvider builds the active connection from the config, with the
// model override applied, and labels the App with it.
func (p profile) connectionProvider(cfg *host.Config) (agent.Provider, error) {
	if cfg.Connections == nil {
		return nil, fmt.Errorf("config has no connections")
	}
	name := p.active
	if name == "" {
		name = cfg.Connections.Active
	}
	conn, ok := cfg.Connections.Connections[name]
	if !ok {
		return nil, fmt.Errorf("no connection %q in the config", name)
	}
	if p.model != "" {
		conn.Model = p.model
	}
	if conn.APIKeyEnv != "" && os.Getenv(conn.APIKeyEnv) == "" {
		return nil, fmt.Errorf("connection %q needs %s set", name, conn.APIKeyEnv)
	}
	prov, err := host.DefaultProviderBuilder(conn)
	if err != nil {
		return nil, fmt.Errorf("connection %q: %w", name, err)
	}
	cfg.Model = host.ModelConfig{BaseURL: name, Model: conn.Model}
	return prov, nil
}

// awaitSurface leaves every ask to a surface's RespondToAsk, as agentweb does:
// the App has no terminal to read an answer from.
func awaitSurface(ctx context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
	<-ctx.Done()
	return core.ElicitationResult{}, ctx.Err()
}

// recorder keeps the instructions and the latest user text of every model
// request. Safe for concurrent use.
type recorder struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

type recordedRequest struct {
	Instructions string
	// SystemTexts is every system message in the request: notes added before
	// a model call, such as instruction files found during the session.
	SystemTexts []string
	// UserTexts is every user message in the request, in order.
	UserTexts []string
}

func (r *recorder) add(req agent.ProviderRequest) {
	rr := recordedRequest{Instructions: req.Instructions}
	for _, m := range req.Messages {
		switch m.Role {
		case agent.RoleUser:
			rr.UserTexts = append(rr.UserTexts, m.Text)
		case agent.RoleSystem:
			rr.SystemTexts = append(rr.SystemTexts, m.Text)
		}
	}
	r.mu.Lock()
	r.reqs = append(r.reqs, rr)
	r.mu.Unlock()
}

func (r *recorder) requests() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.reqs...)
}

type recordingProvider struct {
	agent.Provider
	rec *recorder
}

func (p *recordingProvider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	p.rec.add(req)
	return p.Provider.Stream(ctx, req)
}

func (p *recordingProvider) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	p.rec.add(req)
	return p.Provider.Generate(ctx, req)
}
