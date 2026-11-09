package agent

import (
	"sort"
	"testing"
)

func subEvt(sub, payload string) IncomingEvent {
	ev := evt("s", "room.event", payload)
	ev.Subscription = sub
	return ev
}

func drainedTexts(p *EventInjectionPolicy) []string {
	var out []string
	for _, c := range p.Drain() {
		out = append(out, c.Text)
	}
	sort.Strings(out)
	return out
}

// Two subscriptions to one event name keep separate session entries, so
// neither supersedes the other (#310).
func TestInjectionSessionRetentionKeepsSubscriptionsApart(t *testing.T) {
	p := NewEventInjectionPolicy(EventInjectionConfig{
		Hints: map[string]ContextHint{"room.event": {Retention: "session", Template: "{{room}}={{v}}"}},
	})
	p.Ingest(subEvt("s:room.event#a", `{"room":"a","v":1}`))
	p.Ingest(subEvt("s:room.event#b", `{"room":"b","v":1}`))
	p.Ingest(subEvt("s:room.event#a", `{"room":"a","v":2}`))
	got := drainedTexts(p)
	if len(got) != 2 || got[0] != "a=2" || got[1] != "b=1" {
		t.Fatalf("drained %v, want [a=2 b=1]", got)
	}
}

// A last-wins window folds per subscription, not per event name (#310).
func TestInjectionWindowKeepsSubscriptionsApart(t *testing.T) {
	clock := newClock()
	p := NewEventInjectionPolicy(EventInjectionConfig{
		Hints: map[string]ContextHint{"room.event": {
			Aggregate: &AggregateHint{WindowMs: 100, Strategy: WindowLastWins},
			Template:  "{{room}}={{v}}",
		}},
		now: clock.now,
	})
	p.Ingest(subEvt("s:room.event#a", `{"room":"a","v":1}`))
	p.Ingest(subEvt("s:room.event#b", `{"room":"b","v":1}`))
	p.Ingest(subEvt("s:room.event#a", `{"room":"a","v":2}`))
	clock.advance(200 * 1e6)
	got := drainedTexts(p)
	if len(got) != 2 || got[0] != "a=2" || got[1] != "b=1" {
		t.Fatalf("drained %v, want [a=2 b=1]", got)
	}
}

// Hint lookup runs config by subscription, config by name, the occurrence's
// own _meta, then the hint a server advertised for the name (#310, #313).
func TestInjectionHintPrecedence(t *testing.T) {
	meta := func(tmpl string) map[string]any {
		return map[string]any{MetaKeyContextHint: map[string]any{"template": tmpl}}
	}
	cases := []struct {
		name   string
		config map[string]ContextHint
		served string
		ev     IncomingEvent
		want   string
	}{
		{"subscription config wins", map[string]ContextHint{"sub": {Template: "sub"}, "e": {Template: "name"}}, "served",
			IncomingEvent{Name: "e", Subscription: "sub", Meta: meta("occ")}, "sub"},
		{"name config beats occurrence", map[string]ContextHint{"e": {Template: "name"}}, "served",
			IncomingEvent{Name: "e", Subscription: "sub", Meta: meta("occ")}, "name"},
		{"occurrence beats served", nil, "served",
			IncomingEvent{Name: "e", Meta: meta("occ")}, "occ"},
		{"served last", nil, "served",
			IncomingEvent{Name: "e"}, "served"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewEventInjectionPolicy(EventInjectionConfig{Hints: tc.config})
			p.SetHint("e", ContextHint{Template: tc.served})
			tc.ev.Data = evt("", "", `{}`).Data
			p.Ingest(tc.ev)
			if got := drainedTexts(p); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("drained %v, want [%s]", got, tc.want)
			}
		})
	}
}
