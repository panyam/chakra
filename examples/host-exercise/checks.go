package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// check is one host obligation from #88. run reports whether the host met it
// and, either way, what it saw, so a FAIL line says what went wrong.
//
// setup is the host config the obligation is checked under. The tests swap in
// a config that should flip the result, to show each check can tell the two
// apart.
type check struct {
	id    string
	item  string
	setup func(*host.Config)
	run   func(ctx context.Context, d *demo, cfg *host.Config) (bool, string, error)
}

var checks = []check{
	{"instructions", "the server's instructions reach the system prompt with their origin label", nil, checkInstructions},
	// The host's default skills mode, since SEP-2640's no-prefetch rule is
	// about what a host does unless told otherwise.
	{"no-prefetch", "no skill body is fetched before the model asks for it", nil, checkNoPrefetch},
	{"skill-rotation", "a rotated skill re-prompts for approval", func(cfg *host.Config) {
		cfg.Servers[0].SkillsMode = "catalog"
		cfg.Approval = &host.ApprovalConfig{Mode: "ask", Remember: true}
	}, checkSkillRotation},
	// No hint in config, so only the server's _meta hint can apply.
	{"event-hint", "an event type's server-declared hint applies with no config override", func(cfg *host.Config) {
		cfg.Servers[0].Events = []host.EventConfig{{Name: demoEvent}}
	}, checkEventHint},
	{"agent-delegable", "the server's agent can be delegated to", nil, checkAgentDelegable},
}

// runCheck gives a check its own demo server, so no check sees another's
// rotations or events. tweak, when set, edits the config after setup.
func runCheck(ctx context.Context, c check, tweak func(*host.Config)) (bool, string, error) {
	d, err := newDemo()
	if err != nil {
		return false, "", err
	}
	ts := httptest.NewServer(d.handler)
	defer ts.Close()
	cfg := baseConfig(ts.URL + "/mcp")
	if c.setup != nil {
		c.setup(cfg)
	}
	if tweak != nil {
		tweak(cfg)
	}
	return c.run(ctx, d, cfg)
}

func baseConfig(url string) *host.Config {
	return &host.Config{
		Instructions: "base",
		Model:        host.ModelConfig{BaseURL: "http://unused", Model: "scripted"},
		Servers:      []host.ServerConfig{{ID: "demo", URL: url, Required: true}},
	}
}

func checkInstructions(ctx context.Context, _ *demo, cfg *host.Config) (bool, string, error) {
	p := &scripted{}
	app, err := newApp(cfg, p)
	if err != nil {
		return false, "", err
	}
	defer app.Close()
	if err := app.RunTurn(ctx, "hello"); err != nil {
		return false, "", err
	}
	prompt := p.last().Instructions
	switch {
	case !strings.Contains(prompt, demoInstructions):
		return false, "instructions missing from the system prompt", nil
	case !strings.Contains(prompt, `MCP server "demo"`):
		return false, "instructions present but not labelled with their origin", nil
	}
	return true, "present, labelled with server \"demo\"", nil
}

func checkNoPrefetch(ctx context.Context, d *demo, cfg *host.Config) (bool, string, error) {
	app, err := newApp(cfg, &scripted{})
	if err != nil {
		return false, "", err
	}
	defer app.Close()
	if err := waitFor(ctx, app, host.HostSkillsLoaded); err != nil {
		return false, "", err
	}
	if n := d.skillReads.Load(); n > 0 {
		return false, fmt.Sprintf("%d SKILL.md read(s) at connect, before any load_skill call", n), nil
	}
	return true, "no SKILL.md read at connect", nil
}

// checkSkillRotation approves load_skill once with remember on, rotates the
// skill, and loads it again. Content-bound approval says the second load must
// ask again, because the skill the user approved is no longer the one served.
func checkSkillRotation(ctx context.Context, d *demo, cfg *host.Config) (bool, string, error) {
	var mu sync.Mutex
	var prompts int
	ui := func(_ context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(req.Message, "load_skill") {
			prompts++
		}
		return core.ElicitationResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}
	load := agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "s", Name: "load_skill", Args: core.NewRawJSON([]byte(`{"name":"` + rotatingSkill + `"}`))}}}
	p := &scripted{turns: []agent.StubTurn{load, {Text: "loaded"}, load, {Text: "loaded again"}}}
	app, err := newApp(cfg, p, host.WithElicitationUI(ui))
	if err != nil {
		return false, "", err
	}
	defer app.Close()
	if err := waitFor(ctx, app, host.HostSkillsLoaded); err != nil {
		return false, "", err
	}

	if err := app.RunTurn(ctx, "load the release notes skill"); err != nil {
		return false, "", err
	}
	if err := d.rotate(); err != nil {
		return false, "", err
	}
	if err := app.RunTurn(ctx, "load it again"); err != nil {
		return false, "", err
	}
	second := lastToolResult(p.last())

	mu.Lock()
	defer mu.Unlock()
	if prompts < 2 {
		return false, fmt.Sprintf("asked %d time(s) across both loads; second load returned %q", prompts, clip(second, 80)), nil
	}
	return true, fmt.Sprintf("asked again after the rotation (%d prompts)", prompts), nil
}

func checkEventHint(ctx context.Context, d *demo, cfg *host.Config) (bool, string, error) {
	p := &scripted{}
	app, err := newApp(cfg, p)
	if err != nil {
		return false, "", err
	}
	defer app.Close()

	// The stream opens after the server is ready, and an occurrence yielded
	// before that is never delivered. So yield until one reaches a turn.
	want := strings.NewReplacer("{{status}}", "passed", "{{branch}}", "main").Replace(demoHintTemplate)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := d.yield(ctx, buildStatus{Status: "passed", Branch: "main"}); err != nil {
			return false, "", err
		}
		time.Sleep(150 * time.Millisecond)
		if err := app.RunTurn(ctx, "any news?"); err != nil {
			return false, "", err
		}
		for _, m := range p.last().Messages {
			if m.Role != agent.RoleSystem || !strings.Contains(m.Text, "passed") {
				continue
			}
			if strings.Contains(m.Text, want) {
				return true, fmt.Sprintf("rendered as %q", want), nil
			}
			return false, fmt.Sprintf("injected without the server's template: %q", clip(m.Text, 80)), nil
		}
	}
	return false, "no occurrence reached a turn within 5s", nil
}

func checkAgentDelegable(ctx context.Context, _ *demo, cfg *host.Config) (bool, string, error) {
	// The delegate tool appears once the roster is pulled, which happens after
	// the server is ready. Until then the model just answers.
	delegated := false
	p := &scripted{decide: func(req agent.ProviderRequest) agent.StubTurn {
		switch {
		case isChild(req):
			return agent.StubTurn{Text: demoAgentReply}
		case !delegated && offers(req, demoAgent):
			delegated = true
			return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "d", Name: demoAgent, Args: core.NewRawJSON([]byte(`{"task":"check regressions"}`))}}}
		}
		return agent.StubTurn{Text: "ok"}
	}}
	app, err := newApp(cfg, p)
	if err != nil {
		return false, "", err
	}
	defer app.Close()

	deadline := time.Now().Add(5 * time.Second)
	for !delegated && time.Now().Before(deadline) {
		if err := app.RunTurn(ctx, "get the researcher to check"); err != nil {
			return false, "", err
		}
		if !delegated {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !delegated {
		return false, fmt.Sprintf("no %q tool offered within 5s", demoAgent), nil
	}
	if got := lastToolResult(p.last()); !strings.Contains(got, demoAgentReply) {
		return false, fmt.Sprintf("delegated, but the result was %q", clip(got, 80)), nil
	}
	return true, "delegated and got the child's answer back", nil
}

func newApp(cfg *host.Config, p agent.Provider, opts ...host.AppOption) (*host.App, error) {
	return host.NewApp(cfg, &strings.Builder{}, strings.NewReader(""), append([]host.AppOption{host.WithProvider(p)}, opts...)...)
}

// waitFor blocks until the app has emitted an event of kind, replayed or live.
func waitFor(ctx context.Context, app *host.App, kind host.HostEventKind) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for ev := range app.Subscribe(ctx) {
		if ev.Kind == kind {
			return nil
		}
	}
	return fmt.Errorf("no %v event within 5s", kind)
}

func offers(req agent.ProviderRequest, tool string) bool {
	return slices.ContainsFunc(req.Tools, func(t core.ToolDef) bool { return t.Name == tool })
}

// isChild tells the delegated agent's model calls from the main agent's: the
// child runs on the server agent's instructions, not the host's.
func isChild(req agent.ProviderRequest) bool {
	return strings.Contains(req.Instructions, "you research the release")
}

func lastToolResult(req agent.ProviderRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if m := req.Messages[i]; m.Role == agent.RoleTool {
			return m.Text
		}
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// scripted plays turns in order, or asks decide when it is set, and answers
// "ok" once the script runs out. It keeps every request so a check can look at
// what the host actually sent.
type scripted struct {
	mu     sync.Mutex
	turns  []agent.StubTurn
	decide func(agent.ProviderRequest) agent.StubTurn
	reqs   []agent.ProviderRequest
}

func (s *scripted) next(req agent.ProviderRequest) agent.Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	t := agent.StubTurn{Text: "ok"}
	switch {
	case s.decide != nil:
		t = s.decide(req)
	case len(s.turns) > 0:
		t, s.turns = s.turns[0], s.turns[1:]
	}
	return agent.NewStubProvider(t)
}

func (s *scripted) last() agent.ProviderRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		return agent.ProviderRequest{}
	}
	return s.reqs[len(s.reqs)-1]
}

// Stream implements agent.Provider.
func (s *scripted) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return s.next(req).Stream(ctx, req)
}

// Generate implements agent.Provider.
func (s *scripted) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return s.next(req).Generate(ctx, req)
}
