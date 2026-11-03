package host

import (
	"context"
	"fmt"

	"github.com/panyam/chakra"
)

// StepRoute sends a range of a turn's model calls to one connection (#185),
// for instance planning on a large model and continuing on a small one.
// Steps count from one in every turn.
type StepRoute struct {
	// FromStep is the first step the route covers. Zero means the first.
	FromStep int `json:"fromStep,omitempty"`

	// ToStep is the last step the route covers. Zero means no upper bound.
	ToStep int `json:"toStep,omitempty"`

	// Connection names a key in ConnectionsConfig.Connections.
	Connection string `json:"connection"`
}

func (r StepRoute) covers(step int) bool {
	return step >= r.FromStep && (r.ToStep == 0 || step <= r.ToStep)
}

func validateStepRoutes(routes []StepRoute, conns map[string]ConnectionConfig) error {
	for i, r := range routes {
		if _, ok := conns[r.Connection]; !ok {
			return fmt.Errorf("host: stepRoutes[%d] names undefined connection %q", i, r.Connection)
		}
		if r.FromStep < 0 || r.ToStep < 0 || (r.ToStep != 0 && r.ToStep < max(r.FromStep, 1)) {
			return fmt.Errorf("host: stepRoutes[%d] has an empty step range %d..%d", i, r.FromStep, r.ToStep)
		}
	}
	return nil
}

// StepRouteStage is a request-scoped step stage that sends each step the
// first matching route covers to that route's connection, which resolve
// turns into a provider. A step no route covers stays on the Runner's
// configured provider and, when def is non-nil, is labelled def() so
// TurnResult.UsageByModel and EventStep name both sides of the split.
//
// Routes are matched by step number alone, so the same steps go to the same
// connection on every turn. That keeps prompt-cache misses to the boundaries
// between routes; each boundary still makes the next call cold on the
// connection it moves to.
//
// A resolve error ends the turn.
func StepRouteStage(routes []StepRoute, resolve func(connection string) (agent.Provider, error), def func() string) agent.StepStage {
	return agent.StepStage{
		Name:  "host.step_route",
		Scope: agent.ScopeRequest,
		Run: func(_ context.Context, info agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			for _, r := range routes {
				if !r.covers(info.Step) {
					continue
				}
				p, err := resolve(r.Connection)
				if err != nil {
					return v, fmt.Errorf("routing step %d: %w", info.Step, err)
				}
				v.Provider, v.ProviderName = p, r.Connection
				return v, nil
			}
			if def != nil {
				v.ProviderName = def()
			}
			return v, nil
		},
	}
}
