package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// onServerEvents reads a server's event types for their advertised hints,
// runs the server's OnConnect calls, and opens one events/stream per
// configured event, when the server has just become ready. Each stream
// delivers on its own buffered channel. A per-stream adapter goroutine tags
// occurrences with the server id and subscription id, and gocurrent's FanIn
// merges the streams into one consumer feed. Per-stream buffers isolate
// backpressure, so one noisy stream drops its own events (with a warning)
// and never a sibling's. A failed OnConnect call leaves every stream on the
// server closed.
func (a *App) onServerEvents(sc ServerConfig) {
	c := a.clientByID(sc.ID)
	if c == nil {
		return
	}
	if a.metaTools {
		if _, ok := c.ServerExtensionCapability(events.ExtensionID); ok {
			a.discoverEventHints(sc.ID, c)
			a.watchEventTypes(sc, c)
		}
	}
	if _, err := a.rerunSetup(a.streams.ctx, sc, c, 0); err != nil || !a.metaTools {
		return
	}
	a.subscribeConfigured(sc)
}

// subscribeConfigured opens the server's configured event streams. The
// ready-observer calls it, so a server that connects after boot gets its
// streams with no restart. It runs again after events/list_changed, so a
// configured event type that the server adds later gets subscribed too.
// openSubscription is idempotent (dedup by subscription id), and a failure
// degrades to a warning. A server whose OnConnect calls failed already
// warned, so it opens nothing and says nothing more.
func (a *App) subscribeConfigured(sc ServerConfig) {
	if _, err := a.setupDone(sc); err != nil {
		return
	}
	for _, ec := range sc.Events {
		if _, err := a.openSubscription(a.streams.ctx, sc.ID, ec.Name, ec.Arguments); err != nil {
			a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("events/stream %s on %s: %v", ec.Name, sc.ID, err)})
		}
	}
}

// Deliver hands an event from in-process code to the same pipeline that
// server event streams feed. A webhook handler, a queue consumer or a timer
// in a program that embeds App uses it, rather than serving events to
// itself over events/stream. The event passes through the injection
// policy's filters and transforms, and then the trigger policy, exactly
// as a streamed event does. One goroutine consumes both sources, so a
// delivered event and a streamed one cannot start two proactive turns at
// once.
//
// A hint in the event's Meta under agent.MetaKeyContextHint sets how it
// reaches the model, and a host EventConfig hint for its Name still wins.
// Delivered events have no subscription, so leave Subscription empty. A
// zero Time is set to now.
//
// Deliver blocks while the pipeline's buffer is full. It returns ctx's
// error if ctx ends first, and an error once the App is closed.
func (a *App) Deliver(ctx context.Context, ev agent.IncomingEvent) error {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	select {
	case a.streams.local <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-a.streams.ctx.Done():
		return errors.New("host: Deliver on a closed App")
	}
}

// consumeEvents is the single goroutine that owns the policy pipeline:
// ingest into injection, evaluate triggers, and run approved proactive
// turns (serialized against user turns by the turn mutex inside
// runProactiveTurn).
func (a *App) consumeEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-a.streams.fanIn.OutputChan():
			if !ok {
				return
			}
			a.injection.Ingest(ev)
			if firing := a.triggers.OnEvent(ev); firing != nil {
				a.runProactiveTurn(ctx, firing)
			}
			a.streams.consumed.Add(1)
		}
	}
}

// runProactiveTurn executes a trigger firing as a full turn: the binding's
// instructions enter history as a system message (alongside any drained
// event context), the model responds, and the transcript marks the turn
// with the binding's label.
func (a *App) runProactiveTurn(ctx context.Context, firing *agent.TriggerFiring) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.emit(HostEvent{Kind: HostTriggerFired, Label: firing.Binding.Label})
	if err := a.startExtensionTurns(ctx); err != nil {
		a.emit(HostEvent{Kind: HostTurnFailed, Err: err.Error()})
		return
	}
	a.history = a.context.runDurable(ctx, a.history,
		agent.Message{Role: agent.RoleSystem, Text: firing.Binding.Instructions})
	result, err := a.runner.Run(ctx, a.history, func(e agent.Event) { a.emit(HostEvent{Kind: HostRunnerEvent, RunnerEvent: e}) })
	if err != nil {
		a.endExtensionTurns(ctx, nil, err)
		a.emit(HostEvent{Kind: HostTurnFailed, Err: err.Error()})
		return
	}
	a.history = append(a.history, result.Messages...)
	a.endExtensionTurns(ctx, result, nil)
	a.emit(HostEvent{Kind: HostTurnDone, Result: result})
	a.promptREPL()
}

// injectionStage is the durable context producer for incoming events: it
// drains pending injected context into history as
// system messages. Caller holds turnMu.
func (a *App) injectionStage() ContextStage {
	return ContextStage{Name: "events", Run: func(_ context.Context, msgs []agent.Message) []agent.Message {
		for _, inj := range a.injection.Drain() {
			msgs = append(msgs, agent.Message{Role: agent.RoleSystem, Text: inj.Text})
		}
		return msgs
	}}
}

// buildTriggerBindings converts config bindings into policy bindings,
// rendering the equality filter against top-level payload fields.
func buildTriggerBindings(cfgs []TriggerConfig) []agent.TriggerBinding {
	out := make([]agent.TriggerBinding, 0, len(cfgs))
	for _, tc := range cfgs {
		b := agent.TriggerBinding{
			Server:       tc.Server,
			Event:        tc.Event,
			Instructions: tc.Instructions,
			Label:        tc.Label,
			Cooldown:     time.Duration(tc.CooldownSec) * time.Second,
		}
		if len(tc.Filter) > 0 {
			want := tc.Filter
			b.Filter = func(ev agent.IncomingEvent) bool {
				for field, expect := range want {
					v, ok := ev.Data.Field(field)
					if !ok || strings.Trim(string(v.Raw()), `"`) != expect {
						return false
					}
				}
				return true
			}
		}
		out = append(out, b)
	}
	return out
}

// hintOverrides collects the hint overrides from config. An event without
// arguments keys by its name, as it always has. One with arguments keys by
// its subscription id, so its hint applies to that subscription alone.
func hintOverrides(cfg *Config) map[string]agent.ContextHint {
	out := map[string]agent.ContextHint{}
	for _, sc := range cfg.Servers {
		for _, ec := range sc.Events {
			if ec.Hint == nil {
				continue
			}
			key := ec.Name
			if len(ec.Arguments) > 0 {
				key = subscriptionID(sc.ID, ec.Name, ec.Arguments)
			}
			out[key] = *ec.Hint
		}
	}
	return out
}

func adaptEvent(serverID string, ev events.Event) agent.IncomingEvent {
	return agent.IncomingEvent{
		Server: serverID,
		Name:   ev.Name,
		ID:     ev.EventID,
		Cursor: ev.CursorStr(),
		Time:   time.Now(),
		Data:   core.NewRawJSON(json.RawMessage(ev.Data)),
		Meta:   ev.Meta,
	}
}

// discoverEventHints lists the server's event types and installs the context
// hint each one advertises in its _meta (#81), so a server-declared hint
// applies with no host configuration. A hint in host config still wins. If a
// server's events/list fails, the server keeps whatever hints it had. Hints
// are advisory, and a host that can't read them still delivers the events.
func (a *App) discoverEventHints(serverID string, c *client.Client) {
	defs, err := eventsclient.List(a.streams.ctx, c)
	if err != nil {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("events/list on %s: %v", serverID, err)})
		return
	}
	for _, d := range defs {
		if h, ok := agent.HintFromMeta(d.Meta); ok {
			a.injection.SetHint(d.Name, h)
		}
	}
}

// watchEventTypes re-reads a server's event types when it announces
// notifications/events/list_changed, so a new or revised event type takes
// effect without a restart. See watchListChanged for how it arrives on each
// wire.
func (a *App) watchEventTypes(sc ServerConfig, c *client.Client) {
	a.watchListChanged(sc.ID, c, events.ListChangedFilterKey, eventsListChanged, "events", func() {
		a.discoverEventHints(sc.ID, c)
		a.subscribeConfigured(sc)
	})
}

// eventsListChanged is a notification method. A server sends it when its
// event types change, and mcpkit's events package broadcasts it by this
// literal.
const eventsListChanged = "notifications/events/list_changed"
