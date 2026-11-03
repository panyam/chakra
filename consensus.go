package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// Consensus runs one turn several times and votes on the answers
// (self-consistency). Each draw is an independent RunTurn over the same
// request, seeded by its index so a rerun repeats it; Key groups the answers,
// and the most common key wins. The caller threads Winner.Messages into its
// history; Consensus appends nothing.
//
// It is a bounded loop over one turn rather than orchestration (A8): every
// draw is the same turn, and nothing is decided between draws. What to do
// with a split vote (escalate to a bigger model, ask the user) is left to the
// caller through ConsensusResult.Split.
type Consensus struct {
	// Runner runs every draw. Required. Its provider decides whether draws
	// are seeded (see ConsensusResult.Seeded), and its TreeBudget, when set,
	// caps the whole vote rather than each draw.
	Runner *Runner

	// Draws is how many times the turn runs. Values below 1 mean 1. Prior
	// answers don't count toward it.
	Draws int

	// Prior holds answers the caller already has, such as the one a turn
	// already accepted, so the vote counts them without paying to redraw
	// them. Each is keyed and counted like a draw that ran before draw 0:
	// priors come first in ConsensusResult.Draws, unseeded, and win ties
	// against fresh draws. Fresh draws keep seeds 0..Draws-1, so adding a
	// prior doesn't change what they sample. A nil entry is a misconfigured
	// Consensus.
	Prior []*TurnResult

	// Parallel bounds how many draws run at once. Values below 1 mean 1, so
	// draws run in order by default. Running draws at once is faster but can
	// cost reproducibility: on llama-server a seed only repeats its draw when
	// the server has one slot and draws run one at a time, since a draw that
	// lands on another slot's cache samples differently.
	Parallel int

	// Key groups a draw's answer with the others. ok=false refuses the
	// answer (it didn't parse, it answered nothing), and a refused draw
	// agrees with nothing. Required. Called once per finished draw, possibly
	// from several goroutines when Parallel > 1.
	Key func(res *TurnResult) (key string, ok bool)

	// Emit, when set, receives every draw's events wrapped with the draw's
	// index. It is called from the draw's goroutine; with Parallel > 1 calls
	// for different draws interleave but are never concurrent.
	Emit func(DrawEvent)
}

// DrawEvent is one Runner event from one draw. The draw index travels on the
// envelope rather than inside Event, which stays wire-serializable as is (A2).
type DrawEvent struct {
	// Draw is the draw's index, 0-based, which is also its seed when the
	// draws are seeded.
	Draw int `json:"draw"`
	// Event is the Runner event, unchanged.
	Event Event `json:"event"`
}

// DrawResult is what one draw produced.
type DrawResult struct {
	// Seed is the seed the draw ran with, or nil when draws are unseeded.
	Seed *int `json:"seed,omitempty"`
	// Result is the draw's turn, nil when it failed or never ran.
	Result *TurnResult `json:"result,omitempty"`
	// Key is the draw's key when OK.
	Key string `json:"key,omitempty"`
	// OK says the draw finished and Key accepted its answer. A draw that is
	// not OK agrees with nothing.
	OK bool `json:"ok"`
	// Err is why the draw failed, empty when it finished. A draw that never
	// started because the vote stopped carries the stopping error.
	Err string `json:"err,omitempty"`
	// Prior says the answer came from Consensus.Prior rather than a draw
	// this vote ran.
	Prior bool `json:"prior,omitempty"`
}

// ConsensusResult is the vote.
type ConsensusResult struct {
	// Winner is the earliest draw with the winning key, nil when no draw was
	// accepted.
	Winner *TurnResult `json:"winner,omitempty"`
	// WinnerKey is the key most accepted draws share. A tie goes to the key
	// whose first draw came earliest, so a rerun with the same seeds picks
	// the same winner.
	WinnerKey string `json:"winnerKey,omitempty"`
	// Agree is how many draws share WinnerKey.
	Agree int `json:"agree"`
	// Counted is how many draws ran to completion, accepted or refused, and
	// is the denominator for Agree. Draws that failed or never started are
	// not counted.
	Counted int `json:"counted"`
	// Draws holds Consensus.Prior's answers first, then every fresh draw
	// in index order, including failed and unstarted ones. A fresh draw i
	// sits at Draws[len(Prior)+i].
	Draws []DrawResult `json:"draws"`
	// Split says no key won a strict majority of Counted, including when no
	// draw was accepted.
	Split bool `json:"split"`
	// Seeded says each draw ran with its own seed. It is false when the
	// provider takes no seed (CapabilitiesOf(provider).Seed), in which case
	// agreement may only mean the draws were identical: at temperature 0
	// unseeded draws agree by construction, so Agree is not a confidence.
	Seeded bool `json:"seeded"`
	// Stopped is why the vote ended before every draw ran, nil when all of
	// them ran. Today that is the tree budget (errors.Is ErrTreeBudget) or
	// the context ending.
	Stopped error `json:"-"`
}

// Run draws the turn c.Draws times and votes. req is used as is for every
// draw, except that Generation.Seed is set to the draw's index when the
// provider takes a seed, and Emit is replaced by c.Emit's envelope.
//
// A draw that fails with an ordinary error is recorded on its DrawResult and
// the vote goes on. That includes a draw a turn-end hook stopped
// (ErrTurnStopped): its refused answer is not a vote. A draw that hits the tree budget, or a cancelled ctx,
// stops further draws, and Run returns the draws so far with Stopped set and
// a nil error. Run returns an error only for a misconfigured Consensus.
func (c *Consensus) Run(ctx context.Context, req TurnRequest) (*ConsensusResult, error) {
	if c.Runner == nil || c.Key == nil {
		return nil, fmt.Errorf("agent: Consensus requires Runner and Key")
	}
	if slices.Contains(c.Prior, nil) {
		return nil, fmt.Errorf("agent: Consensus.Prior holds a nil result")
	}
	n := max(c.Draws, 1)
	par := min(max(c.Parallel, 1), n)
	seeded := CapabilitiesOf(c.Runner.cfg.Provider).Seed

	// One shared budget for the whole vote. RunTurn installs the Runner's
	// budget only when none is on ctx, so installing it here makes every
	// draw charge the same counter.
	if !c.Runner.cfg.TreeBudget.zero() && treeBudgetFrom(ctx) == nil {
		ctx = WithTreeBudget(ctx, c.Runner.cfg.TreeBudget)
	}

	k := len(c.Prior)
	out := &ConsensusResult{Draws: make([]DrawResult, k+n), Seeded: seeded}
	for i, res := range c.Prior {
		d := &out.Draws[i]
		d.Prior, d.Result = true, res
		if d.Key, d.OK = c.Key(res); !d.OK {
			d.Key = ""
		}
	}
	var (
		mu      sync.Mutex
		emitMu  sync.Mutex
		stopped error
		wg      sync.WaitGroup
	)
	next := 0
	claim := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		if stopped != nil || next >= n {
			return 0, false
		}
		i := next
		next++
		return i, true
	}
	for range par {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i, ok := claim()
				if !ok {
					return
				}
				if err := ctx.Err(); err != nil {
					mu.Lock()
					if stopped == nil {
						stopped = err
					}
					mu.Unlock()
					out.Draws[k+i].Err = err.Error()
					continue
				}
				dr := c.draw(ctx, req, i, seeded, &emitMu)
				out.Draws[k+i] = dr.DrawResult
				if dr.stop != nil {
					mu.Lock()
					if stopped == nil {
						stopped = dr.stop
					}
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if stopped != nil {
		out.Stopped = stopped
		for i := range out.Draws {
			d := &out.Draws[i]
			if d.Result == nil && d.Err == "" {
				d.Err = "not run: " + stopped.Error()
			}
		}
	}
	out.tally()
	return out, nil
}

type drawOutcome struct {
	DrawResult
	stop error
}

func (c *Consensus) draw(ctx context.Context, req TurnRequest, i int, seeded bool, emitMu *sync.Mutex) drawOutcome {
	var d drawOutcome
	if seeded {
		seed := i
		d.Seed = &seed
		req.Generation.Seed = &seed
	}
	req.Emit = nil
	if c.Emit != nil {
		req.Emit = func(ev Event) {
			emitMu.Lock()
			defer emitMu.Unlock()
			c.Emit(DrawEvent{Draw: i, Event: ev})
		}
	}
	res, err := c.Runner.RunTurn(ctx, req)
	if err != nil {
		d.Err = err.Error()
		if errors.Is(err, ErrTreeBudget) || ctx.Err() != nil {
			d.stop = err
		}
		return d
	}
	d.Result = res
	d.Key, d.OK = c.Key(res)
	if !d.OK {
		d.Key = ""
	}
	return d
}

// tally fills the vote fields from Draws.
func (r *ConsensusResult) tally() {
	counts := map[string]int{}
	first := map[string]int{}
	for i, d := range r.Draws {
		if d.Result != nil {
			r.Counted++
		}
		if !d.OK {
			continue
		}
		if _, seen := first[d.Key]; !seen {
			first[d.Key] = i
		}
		counts[d.Key]++
	}
	best := -1
	for k, n := range counts {
		if n > r.Agree || (n == r.Agree && first[k] < best) {
			r.WinnerKey, r.Agree, best = k, n, first[k]
		}
	}
	if best >= 0 {
		r.Winner = r.Draws[best].Result
	}
	r.Split = r.Agree*2 <= r.Counted
}
