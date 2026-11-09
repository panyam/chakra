package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// MetaKeyContextHint is the vendor _meta key that holds a ContextHint on an
// events/list EventDef. docs/AGENT_DESIGN.md covers it in the vendor prefix
// section. The context-hints SEP draft makes it advisory. Hosts MAY ignore
// it, and host configuration overrides it.
const MetaKeyContextHint = "io.github.panyam.mcpkit/context-hint"

// ContextHint declares how an event's occurrences should reach the model.
// The shape mirrors the context-hints SEP draft one-for-one.
type ContextHint struct {
	// Priority orders injection under the budget: critical, high,
	// medium (default), low.
	Priority string `json:"priority,omitempty"`

	// Aggregate coalesces bursts before injection.
	Aggregate *AggregateHint `json:"aggregate,omitempty"`

	// Template renders the payload as context. {{field}} substitutes a
	// top-level payload field. There is deliberately no logic, and a host
	// that wants more renders the payload itself. Empty uses the default
	// rendering.
	Template string `json:"template,omitempty"`

	// Retention is "turn" or "session". "turn" is the default and injects
	// once. With "session", the latest occurrence re-injects every turn
	// until a newer one supersedes it.
	Retention string `json:"retention,omitempty"`

	// Sensitivity is "public" (the default), "personal" or "restricted".
	// Restricted events are dropped unless the consent gate approves.
	Sensitivity string `json:"sensitivity,omitempty"`
}

// AggregateHint is ContextHint's coalescing sub-shape. It says how to fold a
// burst of one event name into fewer entries before the model sees them. A
// server may publish it as part of the wire shape under the context-hint
// _meta key. So treat both fields as an external contract rather than
// internal tuning.
//
// Coalescing is keyed by server plus event name, so two servers emitting the
// same event name aggregate independently.
type AggregateHint struct {
	// WindowMs is the coalescing window in milliseconds. Zero or negative
	// disables aggregation entirely, and every event buffers individually.
	// A missing AggregateHint means the same thing.
	//
	// The window's start depends on Strategy. Last-wins and merge open it at
	// the first event of a burst, so latency is bounded. Debounce restarts it
	// on every event. So under a sustained stream, debounce can defer release
	// indefinitely.
	WindowMs int `json:"windowMs"`

	// Strategy selects the fold. WindowLastWins keeps only the newest event
	// per key. WindowMerge combines payloads through the policy's Merge func,
	// which defaults to ShallowMergeJSON. WindowDebounce releases only after
	// the key has been quiet for the window.
	//
	// The value is not validated. It comes verbatim from the server's JSON.
	// Any value other than the three constants above behaves as last-wins
	// rather than erroring, so a typo degrades quietly.
	Strategy WindowStrategy `json:"strategy,omitempty"`
}

// EventInjectionConfig assembles an EventInjectionPolicy.
type EventInjectionConfig struct {
	// Hints maps a subscription id or an event name to its ContextHint.
	// Lookup tries the event's Subscription first, then its Name. Entries
	// here override server-advertised hints, so host config wins over vendor
	// _meta.
	Hints map[string]ContextHint

	// Filters and Transforms run in order before buffering. A developer
	// uses them to drop or rewrite events regardless of hints.
	Filters    []func(IncomingEvent) bool
	Transforms []func(IncomingEvent) (IncomingEvent, bool)

	// Merge is the combiner for merge-strategy windows. Nil uses
	// ShallowMergeJSON.
	Merge func(acc, next IncomingEvent) IncomingEvent

	// MaxPerDrain caps how many rendered entries one Drain returns
	// (highest priority first). Zero means 16.
	MaxPerDrain int

	// Consent gates sensitive events. Nil allows public and personal
	// events and drops restricted ones. A non-nil gate decides everything
	// except public events, which always pass.
	Consent func(hint ContextHint, ev IncomingEvent) bool

	// now is injectable for tests; nil means time.Now.
	now func() time.Time
}

// defaultMaxPerDrain bounds injected entries per turn when unconfigured.
const defaultMaxPerDrain = 16

// InjectedContext is one rendered entry that a Drain returns, ready to join
// the model's context. Entries arrive in priority order. They are already
// filtered, transformed, coalesced, and budgeted. A caller renders them into
// the turn and does not re-apply policy.
type InjectedContext struct {
	// Event is the event this entry came from, after any Transforms ran.
	// For a coalesced burst it is the single surviving event, so the
	// originals are not recoverable from here.
	Event IncomingEvent

	// Text is the rendered form the model reads.
	Text string

	// Priority is the originating ContextHint's priority, copied through so
	// a caller can group or style entries. It uses the hint's vocabulary
	// verbatim and is not normalized. So an unrecognized value reaches the
	// caller unchanged rather than being defaulted.
	Priority string
}

// EventInjectionPolicy is the host half of the context-hints SEP draft. It
// buffers incoming events in per-event aggregation windows and renders them.
// When the host drains before a turn, it releases them in priority order
// under a budget. The wiring's event goroutine calls Ingest while the turn
// path calls Drain, and that concurrent use is safe.
type EventInjectionPolicy struct {
	cfg EventInjectionConfig

	mu sync.Mutex
	// discovered holds the hints that servers advertised through SetHint.
	// cfg.Hints holds the host-config ones separately. So a server can
	// revise its own hint while config still wins.
	discovered map[string]ContextHint
	windows    map[string]stage[IncomingEvent]
	pending    []IncomingEvent
	session    map[string]IncomingEvent
	sessOrd    []string
	dropped    int
}

// NewEventInjectionPolicy builds the policy.
func NewEventInjectionPolicy(cfg EventInjectionConfig) *EventInjectionPolicy {
	if cfg.MaxPerDrain <= 0 {
		cfg.MaxPerDrain = defaultMaxPerDrain
	}
	if cfg.Merge == nil {
		cfg.Merge = ShallowMergeJSON
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &EventInjectionPolicy{cfg: cfg, windows: map[string]stage[IncomingEvent]{}, session: map[string]IncomingEvent{}}
}

// HintFromMeta extracts a server-advertised ContextHint from an events/list
// EventDef _meta map. The second return reports presence. Malformed hints are
// ignored, because advisory data never breaks the host.
func HintFromMeta(meta map[string]any) (ContextHint, bool) {
	raw, ok := meta[MetaKeyContextHint]
	if !ok {
		return ContextHint{}, false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return ContextHint{}, false
	}
	var h ContextHint
	if v, ok := m["priority"].(string); ok {
		h.Priority = v
	}
	if v, ok := m["template"].(string); ok {
		h.Template = v
	}
	if v, ok := m["retention"].(string); ok {
		h.Retention = v
	}
	if v, ok := m["sensitivity"].(string); ok {
		h.Sensitivity = v
	}
	if agg, ok := m["aggregate"].(map[string]any); ok {
		ah := &AggregateHint{}
		if w, ok := agg["windowMs"].(float64); ok {
			ah.WindowMs = int(w)
		}
		if s, ok := agg["strategy"].(string); ok {
			ah.Strategy = WindowStrategy(s)
		}
		h.Aggregate = ah
	}
	return h, true
}

// SetHint installs a server-advertised hint for an event name, for example
// from events/list discovery. A later call for the same name replaces it, so
// a server's revised hint takes effect after list_changed. Host-config
// entries passed at construction still win over any of these.
func (p *EventInjectionPolicy) SetHint(name string, h ContextHint) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discovered == nil {
		p.discovered = map[string]ContextHint{}
	}
	p.discovered[name] = h
}

// hint resolves the hint for one event. The order is config for its
// subscription, config for its name, a hint on the occurrence's own _meta,
// then what a server advertised for the name in events/list. Callers hold
// p.mu.
func (p *EventInjectionPolicy) hint(ev IncomingEvent) ContextHint {
	if ev.Subscription != "" {
		if h, ok := p.cfg.Hints[ev.Subscription]; ok {
			return h
		}
	}
	if h, ok := p.cfg.Hints[ev.Name]; ok {
		return h
	}
	if h, ok := HintFromMeta(ev.Meta); ok {
		return h
	}
	return p.discovered[ev.Name]
}

// streamKey groups events for aggregation windows and session retention.
// Two subscriptions to one event name with different arguments are kept
// apart. An event without a subscription groups by server and name.
func streamKey(ev IncomingEvent) string {
	if ev.Subscription != "" {
		return ev.Subscription
	}
	return ev.Server + "/" + ev.Name
}

// Ingest feeds one event through the developer stages and into its
// aggregation window. When the hint has no window, the event goes straight
// to pending.
func (p *EventInjectionPolicy) Ingest(ev IncomingEvent) {
	for _, f := range p.cfg.Filters {
		if !f(ev) {
			return
		}
	}
	for _, tr := range p.cfg.Transforms {
		var keep bool
		if ev, keep = tr(ev); !keep {
			return
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.cfg.now()
	h := p.hint(ev)
	if h.Aggregate == nil || h.Aggregate.WindowMs <= 0 {
		p.buffer(ev, h)
		return
	}
	key := streamKey(ev)
	w, ok := p.windows[key]
	if !ok {
		w = newWindowStage(time.Duration(h.Aggregate.WindowMs)*time.Millisecond, h.Aggregate.Strategy,
			streamKey, p.cfg.Merge)
		p.windows[key] = w
	}
	for _, released := range w.Push(now, ev) {
		p.buffer(released, h)
	}
}

func (p *EventInjectionPolicy) buffer(ev IncomingEvent, h ContextHint) {
	if h.Retention == "session" {
		key := streamKey(ev)
		if _, seen := p.session[key]; !seen {
			p.sessOrd = append(p.sessOrd, key)
		}
		p.session[key] = ev
		return
	}
	p.pending = append(p.pending, ev)
}

// Drain flushes expired windows and returns rendered entries in priority
// order under the budget. Turn-retention entries leave the buffer.
// Session-retention entries re-drain every call until a newer one supersedes
// them. Entries beyond the budget remain buffered for the next drain. When
// the consent gate denies a restricted event, Drain counts it and drops it.
func (p *EventInjectionPolicy) Drain() []InjectedContext {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.cfg.now()

	for _, w := range p.windows {
		for _, released := range w.Flush(now) {
			p.buffer(released, p.hint(released))
		}
	}

	type cand struct {
		ev      IncomingEvent
		session bool
	}
	cands := make([]cand, 0, len(p.pending)+len(p.session))
	for _, key := range p.sessOrd {
		cands = append(cands, cand{ev: p.session[key], session: true})
	}
	for _, ev := range p.pending {
		cands = append(cands, cand{ev: ev})
	}

	// Consent-gate first, then order by priority, THEN apply the budget.
	// Low-priority arrivals must never use up the budget while
	// higher-priority entries wait.
	type ranked struct {
		cand
		priority string
	}
	rankedCands := make([]ranked, 0, len(cands))
	for _, c := range cands {
		h := p.hint(c.ev)
		if !p.consentLocked(h, c.ev) {
			p.dropped++
			continue
		}
		rankedCands = append(rankedCands, ranked{cand: c, priority: priorityOrDefault(h.Priority)})
	}
	sort.SliceStable(rankedCands, func(i, j int) bool {
		return priorityRank(rankedCands[i].priority) < priorityRank(rankedCands[j].priority)
	})

	var out []InjectedContext
	p.pending = p.pending[:0]
	for _, rc := range rankedCands {
		if len(out) >= p.cfg.MaxPerDrain {
			if !rc.session {
				p.pending = append(p.pending, rc.ev)
			}
			continue
		}
		out = append(out, InjectedContext{Event: rc.ev, Text: renderEvent(rc.ev, p.hint(rc.ev)), Priority: rc.priority})
	}
	return out
}

// Dropped reports how many events the consent gate has refused so far.
func (p *EventInjectionPolicy) Dropped() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

func (p *EventInjectionPolicy) consentLocked(h ContextHint, ev IncomingEvent) bool {
	sensitivity := h.Sensitivity
	if sensitivity == "" || sensitivity == "public" {
		return true
	}
	if p.cfg.Consent != nil {
		return p.cfg.Consent(h, ev)
	}
	return sensitivity == "personal"
}

func priorityOrDefault(p string) string {
	if p == "" {
		return "medium"
	}
	return p
}

func priorityRank(p string) int {
	switch p {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	default:
		return 3
	}
}

var templateField = regexp.MustCompile(`\{\{([a-zA-Z0-9_.-]+)\}\}`)

// renderEvent renders one event as context text. With a hint template, it
// substitutes {{field}} from top-level payload fields. Without one, it writes
// the default "event <name> from <server>: <payload>" line.
func renderEvent(ev IncomingEvent, h ContextHint) string {
	if h.Template == "" {
		return fmt.Sprintf("event %s from %s: %s", ev.Name, ev.Server, strings.TrimSpace(string(ev.Data.Raw())))
	}
	return templateField.ReplaceAllStringFunc(h.Template, func(m string) string {
		field := templateField.FindStringSubmatch(m)[1]
		v, ok := ev.Data.Field(field)
		if !ok {
			return m
		}
		return strings.Trim(string(v.Raw()), `"`)
	})
}
