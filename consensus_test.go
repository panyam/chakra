package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// bySeed answers each call with answers[*req.Seed], so a draw's answer
// doesn't depend on the order in which draws run. It reports a seed, and
// records the seeds it was sent.
type bySeed struct {
	answers []string
	mu      sync.Mutex
	seeds   []int
}

func (p *bySeed) ProviderCapabilities() Capabilities { return Capabilities{Seed: true} }

func (p *bySeed) answer(req ProviderRequest) StubTurn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.Seed == nil {
		return StubTurn{Err: errors.New("no seed sent")}
	}
	p.seeds = append(p.seeds, *req.Seed)
	return StubTurn{Text: p.answers[*req.Seed], FinishReason: "stop"}
}

func (p *bySeed) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	t := p.answer(req)
	if t.Err != nil {
		return nil, t.Err
	}
	return &stubStream{deltas: t.deltas()}, nil
}

func (p *bySeed) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	t := p.answer(req)
	return &ProviderResponse{Text: t.Text, FinishReason: "stop"}, t.Err
}

func runnerOver(t *testing.T, p Provider, cfg RunnerConfig) *Runner {
	t.Helper()
	cfg.Provider = p
	r, err := NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// keyText accepts any answer except "refuse", keyed by its text.
func keyText(res *TurnResult) (string, bool) {
	return res.Text, res.Text != "refuse"
}

func vote(t *testing.T, c *Consensus) *ConsensusResult {
	t.Helper()
	res, err := c.Run(context.Background(), TurnRequest{History: []Message{{Role: RoleUser, Text: "q"}}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestConsensusMajorityWins(t *testing.T) {
	p := &bySeed{answers: []string{"b", "a", "refuse", "a", "a"}}
	res := vote(t, &Consensus{Runner: runnerOver(t, p, RunnerConfig{}), Draws: 5, Key: keyText})
	if res.WinnerKey != "a" || res.Agree != 3 || res.Counted != 5 || res.Split {
		t.Fatalf("vote = key %q %d/%d split %v", res.WinnerKey, res.Agree, res.Counted, res.Split)
	}
	if res.Winner != res.Draws[1].Result || res.Winner.Text != "a" {
		t.Fatalf("winner should be the earliest draw with the winning key, draw 1: %+v", res.Winner)
	}
	if d := res.Draws[2]; d.OK || d.Key != "" || d.Result == nil {
		t.Fatalf("a refused draw is counted but agrees with nothing: %+v", d)
	}
	if !res.Seeded || !slices.Equal(p.seeds, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("seeded=%v seeds=%v, want 0..4", res.Seeded, p.seeds)
	}
	for i, d := range res.Draws {
		if d.Seed == nil || *d.Seed != i {
			t.Fatalf("draw %d recorded seed %v", i, d.Seed)
		}
	}
}

// Refused draws don't form a bloc. Three refusals against two agreeing
// answers still elect the answer. With no strict majority, the vote is split.
func TestConsensusRefusalsAgreeWithNothing(t *testing.T) {
	p := &bySeed{answers: []string{"refuse", "x", "refuse", "x", "refuse"}}
	res := vote(t, &Consensus{Runner: runnerOver(t, p, RunnerConfig{}), Draws: 5, Key: keyText})
	if res.WinnerKey != "x" || res.Agree != 2 || res.Counted != 5 || !res.Split {
		t.Fatalf("vote = key %q %d/%d split %v", res.WinnerKey, res.Agree, res.Counted, res.Split)
	}
	p = &bySeed{answers: []string{"refuse", "refuse"}}
	res = vote(t, &Consensus{Runner: runnerOver(t, p, RunnerConfig{}), Draws: 2, Key: keyText})
	if res.Winner != nil || res.Agree != 0 || !res.Split {
		t.Fatalf("all refused should elect nothing: %+v", res)
	}
}

func TestConsensusTieGoesToTheEarlierDraw(t *testing.T) {
	p := &bySeed{answers: []string{"late", "early", "early", "late"}}
	res := vote(t, &Consensus{Runner: runnerOver(t, p, RunnerConfig{}), Draws: 4, Key: keyText})
	if res.WinnerKey != "late" || res.Agree != 2 || !res.Split || res.Winner != res.Draws[0].Result {
		t.Fatalf("tie should go to draw 0's key: key %q agree %d split %v", res.WinnerKey, res.Agree, res.Split)
	}
	// Map order must not decide it, so rerunning gives the same winner.
	for range 20 {
		p := &bySeed{answers: []string{"late", "early", "early", "late"}}
		if again := vote(t, &Consensus{Runner: runnerOver(t, p, RunnerConfig{}), Draws: 4, Key: keyText}); again.WinnerKey != "late" {
			t.Fatalf("rerun picked %q", again.WinnerKey)
		}
	}
}

func TestConsensusUnseededProviderSendsNoSeed(t *testing.T) {
	p := NewStubProvider(StubTurn{Text: "same"}, StubTurn{Text: "same"}, StubTurn{Text: "same"})
	res := vote(t, &Consensus{Runner: runnerOver(t, p, RunnerConfig{}), Draws: 3, Key: keyText})
	if res.Seeded {
		t.Fatal("a provider that reports no seed must give Seeded=false")
	}
	for i, req := range p.Requests() {
		if req.Seed != nil {
			t.Fatalf("request %d carried seed %d", i, *req.Seed)
		}
	}
	for i, d := range res.Draws {
		if d.Seed != nil {
			t.Fatalf("draw %d recorded a seed", i)
		}
	}
	if res.Agree != 3 {
		t.Fatalf("agree = %d", res.Agree)
	}
}

// The Runner's tree budget caps the vote, not each draw. The budget has room
// for two model calls. So the third draw stops the vote, and the first two
// are kept.
func TestConsensusTreeBudgetStopsTheVote(t *testing.T) {
	p := &bySeed{answers: []string{"a", "a", "a", "a"}}
	r := runnerOver(t, p, RunnerConfig{TreeBudget: TreeBudget{MaxSteps: 2}})
	res := vote(t, &Consensus{Runner: r, Draws: 4, Key: keyText})
	if !errors.Is(res.Stopped, ErrTreeBudget) {
		t.Fatalf("stopped = %v, want ErrTreeBudget", res.Stopped)
	}
	if res.Counted != 2 || res.Agree != 2 || res.WinnerKey != "a" {
		t.Fatalf("partial vote = %d/%d %q", res.Agree, res.Counted, res.WinnerKey)
	}
	if !strings.Contains(res.Draws[2].Err, "tree budget") || !strings.Contains(res.Draws[3].Err, "not run") {
		t.Fatalf("draw errs = %q / %q", res.Draws[2].Err, res.Draws[3].Err)
	}
	if len(p.seeds) != 2 {
		t.Fatalf("model calls = %d, want 2", len(p.seeds))
	}
}

func TestConsensusParallelGivesTheSameVote(t *testing.T) {
	answers := []string{"b", "a", "refuse", "a", "a", "c", "a", "b"}
	seq := vote(t, &Consensus{Runner: runnerOver(t, &bySeed{answers: answers}, RunnerConfig{}), Draws: 8, Key: keyText})
	for range 5 {
		p := &bySeed{answers: answers}
		par := vote(t, &Consensus{Runner: runnerOver(t, p, RunnerConfig{}), Draws: 8, Parallel: 3, Key: keyText})
		if par.WinnerKey != seq.WinnerKey || par.Agree != seq.Agree || par.Counted != seq.Counted || par.Winner.Text != seq.Winner.Text {
			t.Fatalf("parallel %q %d/%d, sequential %q %d/%d", par.WinnerKey, par.Agree, par.Counted, seq.WinnerKey, seq.Agree, seq.Counted)
		}
		for i := range answers {
			if par.Draws[i].Key != seq.Draws[i].Key || par.Draws[i].OK != seq.Draws[i].OK {
				t.Fatalf("draw %d differs: %+v vs %+v", i, par.Draws[i], seq.Draws[i])
			}
		}
		got := slices.Sorted(slices.Values(p.seeds))
		if !slices.Equal(got, []int{0, 1, 2, 3, 4, 5, 6, 7}) {
			t.Fatalf("seeds = %v", got)
		}
	}
}

func TestConsensusEventsCarryTheDraw(t *testing.T) {
	p := &bySeed{answers: []string{"x", "y", "z"}}
	var mu sync.Mutex
	texts := map[int]string{}
	ends := map[int]int{}
	c := &Consensus{Runner: runnerOver(t, p, RunnerConfig{}), Draws: 3, Parallel: 3, Key: keyText, Emit: func(e DrawEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch e.Event.Kind {
		case EventTextDelta:
			texts[e.Draw] += e.Event.Text
		case EventTurnEnd:
			ends[e.Draw]++
		}
	}}
	vote(t, c)
	for i, want := range []string{"x", "y", "z"} {
		if texts[i] != want || ends[i] != 1 {
			t.Fatalf("draw %d: text %q, turn-ends %d", i, texts[i], ends[i])
		}
	}
}

func TestConsensusRecordsAFailedDrawAndGoesOn(t *testing.T) {
	p := &bySeed{answers: []string{"a", "a", "a"}}
	r := runnerOver(t, failingSeed{p, 1}, RunnerConfig{})
	res := vote(t, &Consensus{Runner: r, Draws: 3, Key: keyText})
	if res.Stopped != nil || res.Counted != 2 || res.Agree != 2 || res.Draws[1].OK || !strings.Contains(res.Draws[1].Err, "boom") {
		t.Fatalf("res = %+v", res)
	}
}

func TestConsensusNeedsRunnerAndKey(t *testing.T) {
	if _, err := (&Consensus{Key: keyText}).Run(context.Background(), TurnRequest{}); err == nil {
		t.Fatal("no Runner should be refused")
	}
	if _, err := (&Consensus{Runner: runnerOver(t, NewStubProvider(), RunnerConfig{})}).Run(context.Background(), TurnRequest{}); err == nil {
		t.Fatal("no Key should be refused")
	}
}

// failingSeed fails the call with seed bad and defers the rest.
type failingSeed struct {
	*bySeed
	bad int
}

func (f failingSeed) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	if req.Seed != nil && *req.Seed == f.bad {
		return nil, errors.New("boom")
	}
	return f.bySeed.Stream(ctx, req)
}

// A prior answer is counted like a draw that came first. It votes and wins
// ties, and the fresh draws keep their seeds 0..Draws-1.
func TestConsensusCountsPriors(t *testing.T) {
	p := &bySeed{answers: []string{"b", "a"}}
	prior := &TurnResult{Text: "a"}
	res := vote(t, &Consensus{Runner: runnerOver(t, p, RunnerConfig{}), Draws: 2, Prior: []*TurnResult{prior}, Key: keyText})
	if res.WinnerKey != "a" || res.Agree != 2 || res.Counted != 3 || res.Split || res.Winner != prior {
		t.Fatalf("vote = key %q %d/%d split %v winner %+v", res.WinnerKey, res.Agree, res.Counted, res.Split, res.Winner)
	}
	if len(res.Draws) != 3 || !res.Draws[0].Prior || res.Draws[0].Seed != nil || res.Draws[1].Prior || res.Draws[2].Prior {
		t.Fatalf("draws %+v", res.Draws)
	}
	if !slices.Equal(p.seeds, []int{0, 1}) || *res.Draws[1].Seed != 0 || *res.Draws[2].Seed != 1 {
		t.Fatalf("seeds sent %v", p.seeds)
	}
}

func TestConsensusRefusesANilPrior(t *testing.T) {
	c := &Consensus{Runner: runnerOver(t, NewStubProvider(), RunnerConfig{}), Key: keyText, Prior: []*TurnResult{nil}}
	if _, err := c.Run(context.Background(), TurnRequest{}); err == nil {
		t.Fatal("a nil prior should be refused")
	}
}

// A turn-end hook can stop a draw. That draw has failed. It is recorded but
// not counted, and the vote goes on.
func TestConsensusRecordsAStoppedDrawAsFailed(t *testing.T) {
	p := &bySeed{answers: []string{"a", "stop", "a"}}
	stop := func(_ context.Context, info TurnEndInfo) (TurnEndVerdict, error) {
		if info.Text == "stop" {
			return TurnEndVerdict{Stop: "refused"}, nil
		}
		return TurnEndVerdict{}, nil
	}
	res := vote(t, &Consensus{Runner: runnerOver(t, p, RunnerConfig{TurnEnd: []TurnEndHook{stop}}), Draws: 3, Key: keyText})
	if res.Stopped != nil || res.Counted != 2 || res.Agree != 2 || res.Draws[1].OK || !strings.Contains(res.Draws[1].Err, ErrTurnStopped.Error()) {
		t.Fatalf("res = %+v", res)
	}
}
