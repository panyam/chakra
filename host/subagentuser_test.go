package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/panyam/chakra"
)

// lender is an extension that records what the host lends its sub-agents,
// and whose own tools are one read and one write.
type lender struct {
	BaseExtension
	got   *SubAgentHost
	tools *agent.FuncSource
}

func (l *lender) Name() string                     { return "lender" }
func (l *lender) Tools() (agent.ToolSource, error) { return l.tools, nil }
func (l *lender) UseSubAgents(h SubAgentHost)      { *l.got = h }

// An extension that implements SubAgentUser is lent the session's provider,
// the session's read-only tools only (its own and later extensions'
// included), and a sink that reaches surfaces as HostSubAgentEvent.
func TestASubAgentUserIsLentTheProviderReadOnlyToolsAndASink(t *testing.T) {
	ts := startTestServer(t)
	tools := agent.NewFuncSource()
	agent.AddFunc(tools, "peek", "read", func(context.Context, struct{}) (string, error) { return "", nil }, agent.ReadOnly())
	agent.AddFunc(tools, "poke", "write", func(context.Context, struct{}) (string, error) { return "", nil })
	var got SubAgentHost
	stub := agent.NewStubProvider()
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub), WithExtension(&lender{got: &got, tools: tools}))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if got.Provider != stub {
		t.Errorf("lent provider %T", got.Provider)
	}
	defs, err := got.ReadOnlyTools.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
		if ro, _ := d.Annotations["readOnlyHint"].(bool); !ro {
			t.Errorf("lent %s, which isn't read-only", d.Name)
		}
	}
	if !strings.Contains(strings.Join(names, " "), "peek") || strings.Contains(strings.Join(names, " "), "poke") {
		t.Errorf("lent tools %v", names)
	}
	sub := app.Subscribe(context.Background())
	got.OnEvent(agent.SubAgentEvent{Scope: "review", Depth: 1, Event: agent.Event{Kind: agent.EventTurnBegin}})
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Kind != HostSubAgentEvent {
				continue // the subscription replays earlier events first
			}
			if ev.SubAgent.Scope != "review" || ev.SubAgent.Event.Kind != agent.EventTurnBegin {
				t.Errorf("the sink emitted %+v", ev.SubAgent)
			}
			return
		case <-timeout:
			t.Fatal("the sink emitted no HostSubAgentEvent")
		}
	}
}
