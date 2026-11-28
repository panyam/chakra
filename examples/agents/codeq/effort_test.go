package codeq

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// effortRecorder answers as the scripted model and notes each request's
// effort under the ladder step running at the time.
type effortRecorder struct {
	agent.Provider
	mu   sync.Mutex
	step string
	reqs map[string][]agent.ProviderRequest
}

func (r *effortRecorder) ProviderCapabilities() agent.Capabilities {
	return agent.CapabilitiesOf(r.Provider)
}

func (r *effortRecorder) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	r.mu.Lock()
	r.reqs[r.step] = append(r.reqs[r.step], req)
	r.mu.Unlock()
	return r.Provider.Stream(ctx, req)
}

func asksForRows(req agent.ProviderRequest) bool {
	return slices.ContainsFunc(req.Messages, func(m agent.Message) bool { return m.Text == rowsOnly })
}

// Each step of the effort ladder asks the provider for its policy's effort
// on every call (#375). The first step leaves the connection's setting. The
// last asks for high only once the answer check has refused an answer, and
// the scripted model's prose answers give it both kinds of call.
func TestEachEffortStepAsksForItsPolicy(t *testing.T) {
	l, stop, err := EffortLadder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	rec := &effortRecorder{Provider: Scripted(), reqs: map[string][]agent.ProviderRequest{}}
	for i := range l.Steps {
		name, configure := l.Steps[i].Name, l.Steps[i].Configure
		l.Steps[i].Configure = func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
			rec.mu.Lock()
			rec.step = name
			rec.mu.Unlock()
			return configure(cfg)
		}
	}
	cfg := agentConfig()
	cfg.Provider = rec
	r := l.Run(context.Background(), eval.Suite{Config: cfg})
	for _, s := range r.Steps {
		if s.Err != "" {
			t.Fatalf("%s: %s", s.Step, s.Err)
		}
		if s.Report.Passed != len(questions) {
			t.Errorf("%s passed %d, want %d: the scripted model ignores effort", s.Step, s.Report.Passed, len(questions))
		}
	}
	want := map[string]string{"connection's effort": "", "effort minimal": "minimal", "effort low": agent.EffortLow, "effort high": agent.EffortHigh}
	for step, effort := range want {
		if len(rec.reqs[step]) == 0 {
			t.Errorf("%s made no model calls", step)
		}
		for _, req := range rec.reqs[step] {
			if req.Effort != effort {
				t.Errorf("%s asked for effort %q, want %q", step, req.Effort, effort)
				break
			}
		}
	}
	var low, high int
	for _, req := range rec.reqs["high on repair"] {
		want := agent.EffortLow
		if asksForRows(req) {
			want = agent.EffortHigh
		}
		if req.Effort != want {
			t.Errorf("high on repair asked for %q on a call where the answer check had refused = %v", req.Effort, asksForRows(req))
		}
		if req.Effort == agent.EffortHigh {
			high++
		} else {
			low++
		}
	}
	if low == 0 || high == 0 {
		t.Errorf("high on repair made %d low calls and %d high ones, want some of each", low, high)
	}
}

// A connection whose provider can't send the ladder's levels fails the
// first step with one error that names them, before any case runs.
func TestTheEffortLadderNeedsAProviderThatSendsEffort(t *testing.T) {
	l, stop, err := EffortLadder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stub := agent.NewStubProvider()
	cfg := agentConfig()
	cfg.Provider = stub
	r := l.Run(context.Background(), eval.Suite{Config: cfg})
	if e := r.Steps[0].Err; !strings.Contains(e, "doesn't send minimal, low, high") {
		t.Errorf("first step's error = %q", e)
	}
	if n := len(stub.Requests()); n != 0 {
		t.Errorf("%d model calls, want 0", n)
	}
}
