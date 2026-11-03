package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

func transferCall(id, target string) agent.StubTurn {
	return agent.StubTurn{ToolCalls: []agent.ToolCall{{
		ID: id, Name: "transfer_to_" + target, Args: core.NewRawJSON(json.RawMessage(`{}`)),
	}}}
}

// TestAppTeamDrivesRunTurnWithHandoff is the end-to-end check for 1042. A
// Config.Team drives RunTurn instead of the single runner. A transfer fires a
// HostHandoff event. The active agent persists across user turns, so turn 2
// starts from the specialist, not the triage Start.
func TestAppTeamDrivesRunTurnWithHandoff(t *testing.T) {
	ts := startTestServer(t)

	// One StubProvider serves both members. In turn 1, triage transfers, then
	// emits its own follow-up text, because the Runner continues its turn
	// after the tool call. Then the specialist answers. In turn 2, the
	// specialist answers directly.
	stub := agent.NewStubProvider(
		transferCall("t1", "specialist"),
		agent.StubTurn{Text: "connecting you"},
		agent.StubTurn{Text: "specialist: here is turn one"},
		agent.StubTurn{Text: "specialist: here is turn two"},
	)

	cfg := testConfig(ts.URL)
	cfg.Team = &HostTeamConfig{
		Start: "triage",
		Members: []TeamMemberConfig{
			{SubAgentConfig: SubAgentConfig{Name: "triage", Instructions: "route the user"}, HandoffTo: []string{"specialist"}},
			{SubAgentConfig: SubAgentConfig{Name: "specialist", Instructions: "answer in depth"}},
		},
	}
	obs := &collectObserver{}
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if app.team == nil || app.activeTeamAgent != "triage" {
		t.Fatalf("team not wired / active not seeded to Start: team=%v active=%q", app.team != nil, app.activeTeamAgent)
	}

	// turn 1: triage hands off to the specialist, which answers.
	if err := app.RunTurn(context.Background(), "I have a billing question"); err != nil {
		t.Fatal(err)
	}
	if app.activeTeamAgent != "specialist" {
		t.Fatalf("after handoff, active agent should persist as specialist, got %q", app.activeTeamAgent)
	}
	handoffs := obs.kinds(HostHandoff)
	if len(handoffs) != 1 || handoffs[0].From != "triage" || handoffs[0].To != "specialist" {
		t.Fatalf("expected one triage->specialist HostHandoff, got %+v", handoffs)
	}
	// team member events surface attributed by agent name (1033 tagging), not as
	// untagged HostRunnerEvent.
	scopes := map[string]bool{}
	for _, e := range obs.kinds(HostSubAgentEvent) {
		scopes[e.SubAgent.Scope] = true
	}
	if !scopes["triage"] || !scopes["specialist"] {
		t.Fatalf("team events not attributed to both agents, saw %v", scopes)
	}

	// turn 2 starts from the persisted specialist. The triage provider turns
	// are already consumed, so if turn 2 restarted at triage it would misroute.
	triageReqs := len(stub.Requests())
	if err := app.RunTurn(context.Background(), "one more thing"); err != nil {
		t.Fatal(err)
	}
	if app.activeTeamAgent != "specialist" {
		t.Fatalf("turn 2 active should stay specialist, got %q", app.activeTeamAgent)
	}
	// exactly one more model call happened on turn 2 (the specialist), and no new
	// handoff fired.
	if n := len(stub.Requests()) - triageReqs; n != 1 {
		t.Fatalf("turn 2 should be one specialist call, got %d model calls", n)
	}
	if n := len(obs.kinds(HostHandoff)); n != 1 {
		t.Fatalf("turn 2 must not hand off again, total handoffs = %d", n)
	}
}

// TestAppTeamRejectsSingleAgentFeatures guards the mutual-exclusivity
// contract. Team mode replaces the single agent. So combining it with memory,
// sub-agents, or fan-out is a construction error, not a silent drop.
func TestAppTeamRejectsSingleAgentFeatures(t *testing.T) {
	ts := startTestServer(t)
	team := &HostTeamConfig{Start: "a", Members: []TeamMemberConfig{{SubAgentConfig: SubAgentConfig{Name: "a"}}}}

	cases := map[string]func(*Config){
		"memory":    func(c *Config) { c.Memory = &MemoryConfig{} },
		"subAgents": func(c *Config) { c.SubAgents = []SubAgentConfig{{Name: "s"}} },
		"fanOut":    func(c *Config) { c.FanOut = []FanOutGroupConfig{{Name: "f", Members: []SubAgentConfig{{Name: "m"}}}} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(ts.URL)
			cfg.Team = team
			mut(cfg)
			if _, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(agent.NewStubProvider())); err == nil {
				t.Fatalf("team + %s should be rejected at construction", name)
			}
		})
	}
}
