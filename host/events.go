package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// onServerEvents reads a server's event types for their advertised hints and
// opens one events/stream per configured event, when the server has just
// become ready. Each stream delivers on its own buffered channel
// (eventsclient.StreamChan), gets a per-stream adapter goroutine tagging
// occurrences with the server id, and merges into one consumer feed via
// gocurrent's FanIn. Per-stream buffers isolate backpressure: one noisy stream
// drops its own events (warned), never a sibling's.
func (a *App) onServerEvents(sc ServerConfig) {
	if a.streams.fanIn == nil {
		return
	}
	if c := a.clientByID(sc.ID); c != nil {
		if _, ok := c.ServerExtensionCapability(events.ExtensionID); ok {
			a.discoverEventHints(sc.ID, c)
			a.watchEventTypes(sc, c)
		}
	}
	a.subscribeConfigured(sc)
}

// subscribeConfigured opens the server's configured event streams. Called
// from the ready-observer, so a server that connects after boot has its
// streams subscribed with no restart, and again after events/list_changed,
// so a configured event type the server adds later gets subscribed too.
// openSubscription is idempotent (dedup by server:name), and a failure
// degrades to a warning.
func (a *App) subscribeConfigured(sc ServerConfig) {
	for _, ec := range sc.Events {
		if _, err := a.openSubscription(a.streams.ctx, sc.ID, ec.Name); err != nil {
			a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("events/stream %s on %s: %v", ec.Name, sc.ID, err)})
		}
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
		a.emit(HostEvent{Kind: HostTurnFailed, Err: err.Error()})
		return
	}
	a.history = append(a.history, result.Messages...)
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

// hintOverrides collects the per-event hint overrides from config.
func hintOverrides(cfg *Config) map[string]agent.ContextHint {
	out := map[string]agent.ContextHint{}
	for _, sc := range cfg.Servers {
		for _, ec := range sc.Events {
			if ec.Hint != nil {
				out[ec.Name] = *ec.Hint
			}
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
// applies with no host configuration. A hint in host config still wins. A
// server whose events/list fails just keeps whatever hints it had: hints are
// advisory, and a host that can't read them still delivers the events.
func (a *App) discoverEventHints(serverID string, c *client.Client) {
	defs, err := listEventDefs(a.streams.ctx, c)
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

// eventsListChanged is the notification a server sends when its event types
// change. mcpkit's events package broadcasts it by this literal.
const eventsListChanged = "notifications/events/list_changed"

// listEventDefs reads every page of a server's events/list. mcpkit has no
// typed client call for it yet, so this decodes the wire shape into its
// EventDef, as mcpkit's own events bridge does.
func listEventDefs(ctx context.Context, c *client.Client) ([]events.EventDef, error) {
	var all []events.EventDef
	cursor := ""
	for range 100 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.Call(ctx, "events/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Events     []events.EventDef `json:"events"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(res.Raw, &page); err != nil {
			return nil, fmt.Errorf("decode events/list: %w", err)
		}
		all = append(all, page.Events...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("events/list did not finish after 100 pages")
}
