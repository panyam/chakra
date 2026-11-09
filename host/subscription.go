package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// subscription tracks one open event stream so the model (via meta-tools) or
// the user can list and cancel it.
type subscription struct {
	id     string
	server string
	name   string
	args   map[string]any

	mu      sync.Mutex
	call    *eventsclient.StreamCall
	cursor  *string
	setup   int // the server's setup generation when this stream opened
	stopped bool
}

// stop ends the stream for good, so a drop that races it is not reopened.
func (s *subscription) stop() {
	s.mu.Lock()
	s.stopped = true
	call := s.call
	s.mu.Unlock()
	if call != nil {
		call.Stop()
	}
}

// subscriptionID names a subscription by server, event name and arguments.
// Without arguments it is "server:name", the form the meta-tools have
// always returned. With arguments it adds a short hash of their canonical
// JSON. encoding/json sorts map keys, so equal arguments hash equally.
func subscriptionID(serverID, name string, args map[string]any) string {
	id := serverID + ":" + name
	if len(args) == 0 {
		return id
	}
	raw, err := json.Marshal(args)
	if err != nil {
		raw = []byte(fmt.Sprint(args))
	}
	sum := sha256.Sum256(raw)
	return id + "#" + hex.EncodeToString(sum[:4])
}

// openSubscription opens an events/stream on the named server, tags its
// occurrences with the server id and subscription id, and feeds them into
// the fan-in that drives the injection and trigger policies. It returns the
// subscription id so the model can reference it for unsubscribe. Static
// config and the subscribe_events meta-tool share this one path. Opening an
// id that is already open does nothing.
//
// On a server with OnConnect calls, the host owns reconnection. The events
// client's own reopen would race ahead of the setup calls, so it is turned
// off, and a stream that ends with an error goes through reopen.
func (a *App) openSubscription(ctx context.Context, serverID, name string, args map[string]any) (string, error) {
	c := a.clientByID(serverID)
	if c == nil {
		return "", fmt.Errorf("unknown server %q", serverID)
	}
	id := subscriptionID(serverID, name, args)
	if a.streams.has(id) {
		return id, nil
	}
	sc, _ := a.serverConfig(serverID)
	gen, err := a.setupDone(sc)
	if err != nil {
		return "", err
	}

	sub := &subscription{id: id, server: serverID, name: name, args: args, setup: gen}
	ch, call, err := a.openStream(ctx, c, sub, len(sc.OnConnect) > 0)
	if err != nil {
		return "", err
	}
	sub.call = call
	if !a.streams.add(sub) {
		call.Stop()
		return id, nil
	}

	adapted := make(chan agent.IncomingEvent, 16)
	go func() {
		defer close(adapted)
		for ch != nil {
			for ev := range ch {
				if ev.Cursor != nil {
					sub.mu.Lock()
					sub.cursor = ev.Cursor
					sub.mu.Unlock()
				}
				out := adaptEvent(serverID, ev)
				out.Subscription = id
				adapted <- out
			}
			ch = a.reopen(ctx, sc, c, sub)
		}
	}()
	a.streams.fanIn.Add(adapted)
	return id, nil
}

// openStream starts one events/stream call for sub at its last cursor and
// delivers occurrences on a channel that closes when the call ends. It
// mirrors eventsclient.StreamChan, which has no way to turn off the
// client's own reconnect.
func (a *App) openStream(ctx context.Context, c *client.Client, sub *subscription, hostReconnects bool) (<-chan events.Event, *eventsclient.StreamCall, error) {
	sub.mu.Lock()
	cursor := sub.cursor
	sub.mu.Unlock()
	ch := make(chan events.Event, 64)
	call, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName:        sub.name,
		Cursor:           cursor,
		Arguments:        sub.args,
		DisableReconnect: hostReconnects,
		OnEvent: func(ev events.Event) {
			select {
			case ch <- ev:
			default:
				a.emit(HostEvent{Kind: HostEventDropped, ServerID: sub.server, EventName: sub.name})
			}
		},
	})
	if err != nil {
		return nil, nil, err
	}
	go func() {
		<-call.Done()
		close(ch)
	}()
	return ch, call, nil
}

// reopenBackoff bounds the wait between attempts to reopen a dropped
// stream. Tests shorten it.
var reopenBackoff = struct{ min, max time.Duration }{200 * time.Millisecond, 30 * time.Second}

// reopen decides what happens after a stream's call ends. It returns the
// next call's channel, or nil when the subscription is over. Only a server
// with OnConnect calls gets here with a stream to reopen, since every other
// stream reconnects inside the events client. A stream the host stopped, a
// clean end from the server, and a closed App all end the subscription.
// Otherwise the host reruns the server's setup calls and reopens at the
// last cursor, backing off until both succeed.
func (a *App) reopen(ctx context.Context, sc ServerConfig, c *client.Client, sub *subscription) <-chan events.Event {
	sub.mu.Lock()
	ended := sub.stopped || sub.call == nil || sub.call.Err() == nil
	seen := sub.setup
	sub.mu.Unlock()
	if ended || len(sc.OnConnect) == 0 {
		a.streams.drop(sub)
		return nil
	}
	wait := reopenBackoff.min
	for {
		if ctx.Err() != nil {
			a.streams.drop(sub)
			return nil
		}
		gen, err := a.rerunSetup(ctx, sc, c, seen)
		if err == nil {
			var ch <-chan events.Event
			var call *eventsclient.StreamCall
			ch, call, err = a.openStream(ctx, c, sub, true)
			if err == nil {
				sub.mu.Lock()
				sub.call, sub.setup = call, gen
				stopped := sub.stopped
				sub.mu.Unlock()
				if stopped {
					call.Stop()
				}
				return ch
			}
			a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("events/stream %s on %s: %v", sub.name, sc.ID, err)})
		}
		seen = gen
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		if wait *= 2; wait > reopenBackoff.max {
			wait = reopenBackoff.max
		}
	}
}

// closeSubscription stops one stream. Unknown ids report false.
func (a *App) closeSubscription(id string) bool {
	sub := a.streams.take(id)
	if sub == nil {
		return false
	}
	sub.stop()
	return true
}

// listSubscriptions snapshots the open subscriptions.
func (a *App) listSubscriptions() []*subscription {
	return a.streams.all()
}

// clientByID resolves a configured server id to its connected client, or nil.
func (a *App) clientByID(id string) *client.Client {
	for i, sc := range a.cfg.Servers {
		if sc.ID == id {
			return a.servers.clients[i]
		}
	}
	return nil
}

// serverConfig resolves a configured server id to its config.
func (a *App) serverConfig(id string) (ServerConfig, bool) {
	for _, sc := range a.cfg.Servers {
		if sc.ID == id {
			return sc, true
		}
	}
	return ServerConfig{}, false
}
