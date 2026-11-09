package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

func TestStepRoutesValidated(t *testing.T) {
	cases := map[string][]StepRoute{
		"undefined connection": {{FromStep: 2, Connection: "nope"}},
		"empty range":          {{FromStep: 3, ToStep: 2, Connection: "cloud"}},
		"negative":             {{FromStep: -1, Connection: "cloud"}},
	}
	for name, routes := range cases {
		cfg := twoConn()
		cfg.StepRoutes = routes
		if _, err := newConnectionRegistry(cfg, stubBuilder(t)); err == nil || !strings.Contains(err.Error(), "stepRoutes[0]") {
			t.Errorf("%s: err = %v, want a stepRoutes error", name, err)
		}
	}
	cfg := twoConn()
	cfg.StepRoutes = []StepRoute{{FromStep: 2, Connection: "cloud"}, {ToStep: 1, Connection: "local"}}
	if _, err := newConnectionRegistry(cfg, stubBuilder(t)); err != nil {
		t.Errorf("valid routes refused: %v", err)
	}
}

func TestStepRouteStageMatchesFirstCoveringRoute(t *testing.T) {
	provs := map[string]agent.Provider{"a": agent.NewStubProvider(), "b": agent.NewStubProvider()}
	resolve := func(n string) (agent.Provider, error) { return provs[n], nil }
	st := StepRouteStage([]StepRoute{{FromStep: 2, ToStep: 3, Connection: "a"}, {FromStep: 3, Connection: "b"}}, resolve, func() string { return "active" })
	for step, want := range map[int]string{1: "active", 2: "a", 3: "a", 4: "b"} {
		v, err := st.Run(context.Background(), agent.StepInfo{Step: step}, agent.StepView{})
		if err != nil {
			t.Fatal(err)
		}
		if v.ProviderName != want {
			t.Errorf("step %d named %q, want %q", step, v.ProviderName, want)
		}
		if (want == "active") != (v.Provider == nil) {
			t.Errorf("step %d: provider set = %v; only a routed step replaces the provider", step, v.Provider != nil)
		}
	}
}

// routingBuilder builds one script per connection and keeps each provider so
// a test can count the calls it answered. Separate stubs per connection keep
// the Runner from racing on one shared script.
func routingBuilder(scripts map[string][]agent.StubTurn) (ProviderBuilder, map[string]*agent.StubProvider) {
	byModel := map[string]string{"qwen": "local", "gpt-5": "cloud"}
	built := map[string]*agent.StubProvider{}
	return func(c ConnectionConfig) (agent.Provider, error) {
		name := byModel[c.Model]
		p := agent.NewStubProvider(scripts[name]...)
		built[name] = p
		return p, nil
	}, built
}

func echoCall() agent.StubTurn {
	return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"hi"}`))}}}
}

func TestApp_StepRoutesSendLaterStepsElsewhere(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Connections = twoConn()
	cfg.Connections.StepRoutes = []StepRoute{{FromStep: 2, Connection: "cloud"}}
	build, built := routingBuilder(map[string][]agent.StubTurn{
		"local": {echoCall()},
		"cloud": {{Text: "reply from cloud"}},
	})
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProviderBuilder(build))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if n, m := len(built["local"].Requests()), len(built["cloud"].Requests()); n != 1 || m != 1 {
		t.Errorf("local answered %d calls and cloud %d, want step 1 on local and step 2 on cloud", n, m)
	}
	if !strings.Contains(out.String(), "reply from cloud") {
		t.Errorf("turn did not end on the routed connection:\n%s", out.String())
	}
}

func TestApp_ProviderSwitchStillSetsUnroutedSteps(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Connections = twoConn()
	cfg.Connections.StepRoutes = []StepRoute{{FromStep: 2, Connection: "local"}}
	build, built := routingBuilder(map[string][]agent.StubTurn{
		"local": {{Text: "reply from local"}},
		"cloud": {echoCall()},
	})
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProviderBuilder(build))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.SwitchProvider("cloud"); err != nil {
		t.Fatal(err)
	}
	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if n, m := len(built["cloud"].Requests()), len(built["local"].Requests()); n != 1 || m != 1 {
		t.Errorf("cloud answered %d calls and local %d, want step 1 on the switched-to cloud and step 2 routed to local", n, m)
	}
}
