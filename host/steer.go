package host

import (
	"context"
	"strings"
	"sync"

	"github.com/panyam/chakra"
)

// steerQueue holds input a user sent while a turn was running. The turn's
// steer stage drains it before each model call, so the model reads the
// input in the same turn; whatever arrives after the turn's last call is
// handed back when the turn ends and runs as the next turn.
type steerQueue struct {
	mu     sync.Mutex
	active bool
	queue  []string
}

func (q *steerQueue) begin() {
	q.mu.Lock()
	q.active = true
	q.mu.Unlock()
}

// offer queues input if a turn is running, and reports whether it did.
func (q *steerQueue) offer(input string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.active {
		return false
	}
	q.queue = append(q.queue, input)
	return true
}

func (q *steerQueue) take() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.queue
	q.queue = nil
	return out
}

// end marks the turn over and returns the input it never got to.
func (q *steerQueue) end() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active = false
	out := q.queue
	q.queue = nil
	return out
}

// Submit is how a surface hands the App a user message without knowing
// whether a turn is running. With no turn running it runs one, as
// RunTurnWith does with opts, and returns when it ends. While a turn runs it queues the message as
// a steer and returns at once with steered true: the model reads it before
// its next call in that turn, and it joins the turn's record. A steer that
// arrives after the turn's last model call runs as the next turn when this
// one ends, so nothing is dropped.
//
// A steer is announced as a HostUserInput event with Label "steer" when it
// is queued, so every surface can show it in place.
//
// A steer joins a turn that is already running, so opts don't apply to it.
//
// Team mode runs its own loop with no steer stage, so there a steer waits
// for the turn to end and runs as the next one.
func (a *App) Submit(ctx context.Context, input string, opts TurnOptions) (steered bool, err error) {
	if a.steer.offer(input) {
		a.emit(HostEvent{Kind: HostUserInput, Message: input, Label: "steer"})
		return true, nil
	}
	return false, a.RunTurnWith(ctx, input, opts)
}

// steerStage drains queued steers into the turn's record before each model
// call. It is a session-scope stage because a steer is something the user
// said: it belongs in history and the session store like the message that
// started the turn, not only in the view of one call.
func (a *App) steerStage() agent.StepStage {
	return agent.StepStage{
		Name:  "host.steer",
		Scope: agent.ScopeSession,
		Run: func(_ context.Context, _ agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			for _, s := range a.steer.take() {
				v.Messages = append(v.Messages, agent.Message{Role: agent.RoleUser, Text: s})
			}
			return v, nil
		},
	}
}

// joinSteers makes one message of steers that missed their turn.
func joinSteers(steers []string) string { return strings.Join(steers, "\n\n") }
