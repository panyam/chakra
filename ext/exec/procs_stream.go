package exec

import (
	"sync"
	"time"

	"github.com/panyam/chakra/host"
)

const (
	// defaultStreamBudget is how much of one process's output streams to the
	// surfaces before it pauses. The host's event log keeps every event, so a
	// dev server left running would otherwise grow it without end.
	defaultStreamBudget = 4 << 20
	// A process's output is sent every streamEvery, or as soon as streamAt
	// bytes are waiting, so a chatty process costs a few events a second
	// rather than one per write.
	streamEvery = 250 * time.Millisecond
	streamAt    = 8 << 10
	// streamMax caps one event's text; a longer burst keeps its end.
	streamMax = 16 << 10
)

// setProcessOutput sets the function that reports process output as it arrives.
// Processes started before it is set don't stream.
func (s *Source) setProcessOutput(fn func(host.ProcessOutput)) {
	if s.procs == nil {
		return
	}
	s.procs.mu.Lock()
	s.procs.report = fn
	s.procs.mu.Unlock()
}

// outputStream batches one process's output for the surfaces. It reads a copy
// of the process's writes, so it doesn't affect the model's reads through
// collect.
type outputStream struct {
	mu      sync.Mutex
	pending []byte
	dropped int64
	paused  bool
	sent    int64
	budget  int64 // negative: no limit
	kick    chan struct{}
}

func newOutputStream(budget int) *outputStream {
	return &outputStream{budget: int64(budget), kick: make(chan struct{}, 1)}
}

// add takes a copy of b. It runs on the process's output path, so it never
// blocks on the surfaces.
func (st *outputStream) add(b []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.paused {
		return
	}
	st.pending = append(st.pending, b...)
	if over := len(st.pending) - streamMax; over > 0 {
		st.pending = append([]byte(nil), st.pending[over:]...)
		st.dropped += int64(over)
	}
	if len(st.pending) >= streamAt {
		select {
		case st.kick <- struct{}{}:
		default:
		}
	}
}

// take returns the event for what is waiting, and whether there is anything
// to send, spending the budget.
func (st *outputStream) take() (host.ProcessOutput, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.pending) == 0 && st.dropped == 0 {
		return host.ProcessOutput{}, false
	}
	o := host.ProcessOutput{Text: string(st.pending), Dropped: st.dropped}
	st.pending, st.dropped = nil, 0
	if st.budget >= 0 {
		if left := st.budget - st.sent; int64(len(o.Text)) >= left {
			o.Text = o.Text[:max(left, 0)]
			o.Paused, st.paused = true, true
		}
	}
	st.sent += int64(len(o.Text))
	return o, true
}

// run reports p's output until it exits, then reports the exit.
func (st *outputStream) run(p *proc, report func(host.ProcessOutput)) {
	send := func(o host.ProcessOutput) {
		o.Source, o.ID, o.Command = "exec", p.id, p.command
		report(o)
	}
	t := time.NewTicker(streamEvery)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			// Let the output copier drain, as collect does.
			time.Sleep(20 * time.Millisecond)
			o, _ := st.take()
			o.Exited, o.ExitCode = true, p.exit
			send(o)
			return
		case <-t.C:
		case <-st.kick:
		}
		if o, ok := st.take(); ok {
			send(o)
		}
	}
}
