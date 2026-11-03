package host

import (
	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// registerRunnerControl builds an agent.AgentPool from the configured sub-agent
// personas and adds the runner-control meta-tools (spawn_agent / await_agent /
// cancel_agent / list_agents, issue 1166) to the main aggregate, so the main
// model can run personas in the background with handles.
//
// registerSubAgents builds each persona as a blocking tool. Each pooled agent
// is a persona child Runner built exactly the same way (shared provider,
// serverTools-only, memory-free per A7). So the same persona is reachable as
// a direct blocking tool and as a spawnable background agent. The pool
// forwards each background child's event stream as a HostSubAgentEvent, so
// spawned activity renders the same way a blocking sub-agent's does.
func (a *App) registerRunnerControl(multi, serverTools *agent.MultiSource, provider agent.Provider, tp core.TracerProvider, mp core.MeterProvider) error {
	pool := agent.NewAgentPool(func(e agent.SubAgentEvent) {
		a.emit(HostEvent{Kind: HostSubAgentEvent, SubAgent: e})
	})
	for _, sub := range a.cfg.SubAgents {
		child, err := agent.NewRunner(a.personaRunnerConfig(sub, serverTools, provider, tp, mp))
		if err != nil {
			return err
		}
		if err := pool.Register(sub.Name, sub.Description, child, sub.MaxDepth); err != nil {
			return err
		}
	}
	a.agentPool = pool
	return multi.Add("runner-control", agent.NewSpawnSource(pool))
}
