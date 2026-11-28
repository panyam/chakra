package research

import (
	"context"
	"sync"

	"github.com/panyam/chakra"
)

// meter counts what the calls through it spend. The sub-agents and the
// critic call the model through one, since a turn's Usage leaves their
// calls out, and each case's Setup reports it as Spent.
type meter struct {
	agent.Provider
	mu    sync.Mutex
	usage agent.Usage
}

func (m *meter) add(u *agent.Usage) {
	if u == nil {
		return
	}
	m.mu.Lock()
	m.usage = m.usage.Add(*u)
	m.mu.Unlock()
}

// take returns what the meter counted since the last take, and starts
// again from zero.
func (m *meter) take() agent.Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.usage
	m.usage = agent.Usage{}
	return u
}

func (m *meter) ProviderCapabilities() agent.Capabilities {
	return agent.CapabilitiesOf(m.Provider)
}

func (m *meter) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	resp, err := m.Provider.Generate(ctx, req)
	if resp != nil {
		m.add(resp.Usage)
	}
	return resp, err
}

func (m *meter) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	s, err := m.Provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &meteredStream{Stream: s, m: m}, nil
}

type meteredStream struct {
	agent.Stream
	m *meter
}

func (s *meteredStream) Recv() (agent.Delta, error) {
	d, err := s.Stream.Recv()
	if err == nil && d.Kind == agent.DeltaUsage {
		s.m.add(d.Usage)
	}
	return d, err
}
