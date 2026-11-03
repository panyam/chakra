package host

import (
	"context"

	"github.com/panyam/chakra"
)

// subscribeBuffer smooths bursts on a Subscribe channel. It is not a
// correctness bound. The drain goroutine reads the retained event log at its
// own pace, so even a full buffer only slows the drain, never emit.
const subscribeBuffer = 64

// Subscribe returns a channel that replays the session's history and then
// follows it live, so a surface attaching mid-session (a web streamer, issue
// 1196) sees the whole conversation and every subsequent event. The channel
// closes when ctx is cancelled; the caller must drain it or cancel ctx to
// release the backing goroutine and its Queue subscription. A dropped consumer
// that wants the stream again simply calls Subscribe once more.
//
// Local observers are synchronous (issue 1194, emit's in-line fan-out).
// Subscribe is a separate async seam, so a slow remote consumer never stalls
// the terminal rendering contract. A slow consumer cannot block emit. emit only
// Appends (non-blocking) and fans out to local observers. This drain goroutine
// reads the log independently and blocks only itself.
//
// Replay stitches two sources together, so a persisted session loses nothing
// even when the event log is bounded and evicts old events.
//
//   - Deep history from the RunStore. It holds the turns already written to
//     the store, and the in-memory Queue may have evicted them since. When a
//     RunStore is configured and the run has persisted events, each stored
//     agent.Event is replayed wrapped as HostEvent{Kind: HostRunnerEvent}.
//     Non-runner HostEvents (server-state, skills) from those turns are
//     ephemeral and intentionally not replayed, because the RunStore only
//     holds the runner stream.
//   - The unpersisted tail from the Queue, drained from persistedOffset. It
//     holds the in-progress turn plus any recent events not yet written to the
//     store. With a generous retention window, the Queue still retains this
//     whole range.
//
// The two halves meet exactly at persistedOffset with no overlap. The RunStore
// covers eventLog[0, persistedOffset), and the Queue drain starts at
// persistedOffset. After the tail, the goroutine follows the Queue's Notify for
// live events.
//
// Consistency works as snapshot-then-stream. The snapshot is three steps:
// subscribe to the Queue, read persistedOffset, and read the RunStore.
// Subscribe takes all three under turnMu. The turn-end persist site also holds
// turnMu while it advances persistedOffset and writes AppendEvents together.
// So a persist cannot interleave the snapshot, and the RunStore contents and
// persistedOffset are always read as one consistent pair. Imagine a two-step
// read without the lock instead. A persist could run between the two reads. If
// the RunStore got ahead of persistedOffset, replay would duplicate events. If
// persistedOffset got ahead of the RunStore, replay would leave a gap.
// Subscribe also subscribes to the Queue before it releases turnMu, so it
// misses no live Append between the tail read and going live. Once the
// snapshot is captured the lock is released and all delivery happens off-lock,
// so a slow consumer never blocks a turn.
//
// When no RunStore is configured, persistedOffset is always 0, so the replay
// is simply the whole retained Queue window (ReadFrom(0)) followed by live.
//
// HostElicitRequest is appended without an ask id. So Subscribe stamps its
// event-log offset onto the delivered copy here (stampAskID). That offset
// serves as the ask id, and E2's RespondToAsk resolves it through the log
// barrier. Deep-history events are runner events, never asks, so only the
// Queue tail and live events are stamped.
func (a *App) Subscribe(ctx context.Context) <-chan HostEvent {
	// Snapshot under turnMu so the persist site cannot advance persistedOffset
	// or the RunStore between the reads below. Subscribe first, so any Append
	// after this point wakes the subscription. The tail read may also pick up
	// that Append, and the extra signal is harmless. Any Append before this
	// point is already in the tail.
	a.turnMu.Lock()
	sub := a.events.log.Subscribe()
	p := int(a.events.persisted.Load())
	var deep []agent.Event
	if a.session.store != nil && a.session.runID != "" && p > 0 {
		if resp, err := a.session.store.LoadRun(ctx, agent.LoadRunRequest{RunID: a.session.runID}); err == nil && resp.Found {
			deep = resp.Run.Events
		}
	}
	tail, next := a.events.log.ReadFrom(p)
	a.turnMu.Unlock()

	out := make(chan HostEvent, subscribeBuffer)
	go func() {
		defer close(out)
		defer sub.Close()

		// (a) deep history from the RunStore: runner events, never asks.
		for _, e := range deep {
			select {
			case out <- HostEvent{Kind: HostRunnerEvent, RunnerEvent: e}:
			case <-ctx.Done():
				return
			}
		}
		// (b) the unpersisted Queue tail captured in the snapshot. tail[i] is at
		// log offset p+i.
		for i, e := range tail {
			stampAskID(&e, p+i)
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
		// (c) live: follow the Queue from where the tail ended.
		off := next
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.Notify():
				evs, n := a.events.log.ReadFrom(off)
				for i, e := range evs {
					stampAskID(&e, off+i)
					select {
					case out <- e:
					case <-ctx.Done():
						return
					}
				}
				off = n
			}
		}
	}()
	return out
}

// stampAskID stamps the event-log offset as the ask id on a HostElicitRequest,
// so a remote surface reads the id off the frame and answers via
// RespondToAsk(offset). A no-op for other kinds. ev points at a delivered copy
// (HostEvent is a value), never the stored log entry.
func stampAskID(ev *HostEvent, offset int) {
	if ev.Kind == HostElicitRequest {
		ev.AskID = int64(offset)
	}
}
