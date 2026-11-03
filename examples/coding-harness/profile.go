package main

import (
	"context"
	"fmt"
	"sync"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/chakra/surfaces/coding"
)

// profile is the coding agent. It holds everything that makes the agent
// differ from agentchat --workspace, as a choice of config, extension, tool
// or provider. Each mission ticket swaps a piece in when it merges, and
// nothing in this file should ever need to change how a turn runs.
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
	// unconfined is set on platforms with no sandbox backend, where exec runs
	// under standInSandbox. CI is Linux, and the backend is darwin-only.
	unconfined bool
	// checkpoints is where file snapshots go, outside root.
	checkpoints string
	// instructions names the agentsmd preset that loads AGENTS.md and the
	// like, over workspace.json's choice. Empty keeps workspace.json's.
	instructions string
	// routeTo names a config connection that answers every step from
	// routeFrom on (#185), and routeModel overrides its model. Empty routeTo
	// keeps every step on the active connection.
	routeTo    string
	routeFrom  int
	routeModel string
	// recorder sees every model request, so checks can read what the model
	// was sent. Nil records nothing.
	recorder *recorder
}

// build is the SessionManager factory: one host.App per session, built by
// surfaces/coding with the harness's recorder and step routing on top.
func (p profile) build(ctx context.Context) (*host.App, error) {
	cp := coding.Profile{
		Dir:             p.dir,
		Root:            p.root,
		Provider:        p.provider,
		Active:          p.active,
		Model:           p.model,
		Approval:        p.approval,
		CheckpointStore: p.checkpoints,
		Instructions:    p.instructions,
	}
	if p.unconfined {
		cp.Sandbox = coding.StandInSandbox{}
	}
	if p.recorder != nil {
		cp.WrapProvider = func(prov agent.Provider) agent.Provider { return &recordingProvider{Provider: prov, rec: p.recorder} }
	}
	if p.routeTo != "" {
		if p.provider != nil {
			return nil, fmt.Errorf("step routing needs a live model, not the scripted one")
		}
		cp.Extensions = func(cfg *host.Config) ([]host.Extension, error) {
			ext, err := p.routeExtension(cfg)
			if err != nil {
				return nil, err
			}
			return []host.Extension{ext}, nil
		}
	}
	return cp.Build(ctx)
}

// routeExtension sends steps from routeFrom on to routeTo through
// host.StepRouteStage, so a live run can split its spend across two models
// (#185). The routed provider goes through the recorder like the active one.
// Steps are named by model, so the metrics line reads "gpt-5.5" and
// "gpt-5.4-mini" rather than two connection names that may share a model.
func (p profile) routeExtension(cfg *host.Config) (host.Extension, error) {
	routed, routedModel, err := coding.BuildConnection(cfg, p.routeTo, p.routeModel)
	if err != nil {
		return nil, fmt.Errorf("route: %w", err)
	}
	if p.recorder != nil {
		routed = &recordingProvider{Provider: routed, rec: p.recorder}
	}
	activeModel := cfg.Model.Model
	stage := host.StepRouteStage(
		[]host.StepRoute{{FromStep: p.routeFrom, Connection: routedModel}},
		func(string) (agent.Provider, error) { return routed, nil },
		func() string { return activeModel },
	)
	return stageExtension{stage: stage}, nil
}

// stageExtension contributes one step stage and nothing else.
type stageExtension struct {
	host.BaseExtension
	stage agent.StepStage
}

func (e stageExtension) Name() string                  { return "coding-harness.route" }
func (e stageExtension) StepStages() []agent.StepStage { return []agent.StepStage{e.stage} }

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
	// Tools names the tools the request offered.
	Tools []string
}

func (r *recorder) add(req agent.ProviderRequest) {
	rr := recordedRequest{Instructions: req.Instructions}
	for _, d := range req.Tools {
		rr.Tools = append(rr.Tools, d.Name)
	}
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
