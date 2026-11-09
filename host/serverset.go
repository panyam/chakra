package host

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/panyam/chakra"
	gocurrent "github.com/panyam/gocurrent"
	"github.com/panyam/mcpkit/client"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// serverSet holds the connected MCP servers. It has the Group that owns their
// connection lifecycle and the clients the Group drives. It also has the
// persona-visible mirror of their tools, and the interactive token sources
// that make /login possible.
//
// It is held by value, and every field is zero-value usable. A nil group
// means construction failed before the Group existed, and Close checks for
// exactly that.
type serverSet struct {
	// group owns the connection lifecycle, which covers async connect,
	// per-server state, and backoff reconnect. Its observer registers a
	// server's tools when it becomes ready. See docs/AGENT_SERVER_STATE.md.
	group   *client.Group
	clients []*client.Client

	// tools mirrors the server sources only, with no meta-tools and no
	// sub-agents. A persona gets a filtered view of the servers and does not
	// see the other personas, since seeing them would let it recurse. Nil
	// unless personas are configured.
	tools *agent.MultiSource

	// oauth holds the interactive token source per server id, so LoginServer
	// can force a fresh browser login. Only oauth-typed servers have an entry,
	// and CanLogin reports whether one exists.
	oauth map[string]loginSource
}

// canLogin reports whether the server has an interactive token source.
func (s *serverSet) canLogin(id string) bool { return s.oauth[id] != nil }

// streamSet is the inbound event plumbing. It holds the live subscriptions
// and a fan-in that merges every server's stream into one channel. A context
// bounds them all.
//
// It is per-server, like serverSet, but kept separate because the lifecycles
// differ. Connections come up and go down on the Group's schedule and outlive
// any single subscription. The streams exist only when the async control
// plane is on (metaTools), and Close stops them before the Group. Merging the
// two would put a conditional half inside an unconditional whole.
type streamSet struct {
	mu   sync.Mutex
	subs map[string]*subscription

	calls []*eventsclient.StreamCall

	// setups holds each server's OnConnect state, by server id.
	setups map[string]*serverSetup

	// local carries events that in-process code hands to App.Deliver. It is
	// one more input to fanIn, so delivered and streamed events share one
	// consumer.
	local chan agent.IncomingEvent

	// consumed counts events the consumer has finished with, so tests can
	// wait for one to pass through.
	consumed atomic.Int64

	// fanIn merges every subscription into one channel, and the consumer
	// drains it. Nil when the async control plane is off.
	fanIn *gocurrent.FanIn[agent.IncomingEvent]

	// ctx bounds every subscription. It is built before the servers connect,
	// so the ready-observer can subscribe a late server the moment it
	// arrives. stop cancels them all.
	ctx  context.Context
	stop context.CancelFunc
}

// add registers a live subscription. Reports false when the id is already
// taken, which is the caller's cue that the subscription already exists.
func (s *streamSet) add(sub *subscription) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.subs[sub.id]; exists {
		return false
	}
	s.subs[sub.id] = sub
	return true
}

// take removes and returns a subscription, or nil when it is unknown.
func (s *streamSet) take(id string) *subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[id]
	if !ok {
		return nil
	}
	delete(s.subs, id)
	return sub
}

// drop removes sub when it is still the subscription registered under its
// id. A stream that ended on its own leaves the set this way, and a newer
// subscription under the same id stays.
func (s *streamSet) drop(sub *subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs[sub.id] == sub {
		delete(s.subs, sub.id)
	}
}

// setupFor returns the server's OnConnect state, creating it on first use.
func (s *streamSet) setupFor(serverID string) *serverSetup {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setups == nil {
		s.setups = map[string]*serverSetup{}
	}
	st, ok := s.setups[serverID]
	if !ok {
		st = &serverSetup{}
		s.setups[serverID] = st
	}
	return st
}

// has reports whether the id is already subscribed.
func (s *streamSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.subs[id]
	return ok
}

// all snapshots the live subscriptions.
func (s *streamSet) all() []*subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out
}
