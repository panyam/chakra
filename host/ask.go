package host

import (
	"context"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// elicitResolution is a pending ask's answer. Whichever surface responds
// first delivers it, and it is the barrier resolution value on the event log.
// err reports that a surface failed to present the ask. A decline is not an
// err, since ElicitationResult.Action carries it. by names the resolving
// surface.
type elicitResolution struct {
	res core.ElicitationResult
	err error
	by  string
}

// RespondToAsk resolves a pending elicitation. The ask was announced at an
// event-log offset, and askOffset identifies it. A surface reads that offset
// from its Watch frame. Resolution uses the log's barrier, so the first
// responder wins. A stale or already-answered offset returns the log's
// ErrAlreadyResolved / ErrOffsetOutOfRange. A web surface's respond RPC calls
// this data-only capability (issue 1196). The local terminal UI resolves the
// same ask through barrierElicit below.
func (a *App) RespondToAsk(askOffset int, res core.ElicitationResult, by string) error {
	return a.events.log.Resolve(askOffset, elicitResolution{res: res, by: by})
}

// barrierElicit wraps a local ElicitationUI. The elicitation is broadcast to
// every surface as a HostElicitRequest at a known log offset, and the first
// responder resolves it. The ask is a log entry, and the log's barrier on
// that entry's offset resolves it. The local UI runs as one responder. It
// races any surface that calls RespondToAsk on that offset. The log's
// Resolve lets the first win, and the loser is cancelled. A
// HostElicitResolved event then tells every surface to retract. With no
// other surface attached the local UI always wins, so single-surface
// behavior is unchanged.
func (a *App) barrierElicit(local agent.ElicitationUI) agent.ElicitationUI {
	return func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		reqCopy := req
		off := a.emit(HostEvent{Kind: HostElicitRequest, Elicit: &reqCopy})

		// The local UI is one responder; cancel it when another surface wins.
		localCtx, cancelLocal := context.WithCancel(ctx)
		defer cancelLocal()
		go func() {
			res, err := local(localCtx, req)
			if localCtx.Err() == nil {
				a.events.log.Resolve(off, elicitResolution{res: res, err: err, by: "local"})
			}
		}()

		// AwaitResolution has no ctx, so run it on a goroutine. If the turn is
		// cancelled, resolve the ask ourselves. The goroutine then unblocks
		// and exits, and the buffered channel means it never leaks.
		resolved := make(chan elicitResolution, 1)
		go func() { resolved <- a.events.log.AwaitResolution(off).(elicitResolution) }()

		select {
		case r := <-resolved:
			cancelLocal()
			if r.by == askCancelled {
				return core.ElicitationResult{}, r.err
			}
			a.emit(HostEvent{Kind: HostElicitResolved, AskID: int64(off), By: r.by})
			return r.res, r.err
		case <-ctx.Done():
			a.events.log.Resolve(off, elicitResolution{err: ctx.Err(), by: askCancelled})
			return core.ElicitationResult{}, ctx.Err()
		}
	}
}

// askCancelled is the resolver tag for a turn cancelled before any surface
// answered; it unblocks the AwaitResolution goroutine without emitting a
// HostElicitResolved (nothing was answered).
const askCancelled = "cancelled"
