package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// ToolOwner identifies one source's claim on a tool name during collision
// resolution.
type ToolOwner struct {
	// SourceID is the identifier the source was added under.
	SourceID string
	// Def is that source's definition for the contested name.
	Def core.ToolDef
}

// Resolver picks which source handles an ambiguous bare-name call. Returning
// an empty string or an error fails the call. The caller can always reach a
// specific candidate through the qualified "sourceID/name" form instead.
type Resolver func(name string, candidates []ToolOwner, args map[string]any) (sourceID string, err error)

// MultiSource aggregates ToolSources under stable source IDs. It copies the
// collision semantics of ext/ui's ServerRegistry, in ToolSource form.
//
//   - Unique names are exposed and callable as-is.
//   - Colliding names are exposed to the model ONLY in qualified form, as
//     "sourceID/name" for every claimant. So the model-facing list never
//     contains duplicates, and every tool is still reachable.
//   - A bare-name Call can hit a collision. It then consults the Resolver
//     if one is configured. Otherwise it fails with an error that names the
//     qualified forms.
//
// Qualification is deterministic. It depends only on the set of source IDs
// that claim the name, not on registration order.
type MultiSource struct {
	mu          sync.RWMutex
	order       []string
	sources     map[string]ToolSource
	resolver    Resolver
	onCollision func(name string, sourceIDs []string)

	// index memoizes the gathered claims. Add, Remove, and Invalidate clear
	// it. Tools refreshes it lazily, and so does Call on a name miss.
	index      map[string][]ToolOwner
	indexOrder []string
}

// MultiOption configures a MultiSource.
type MultiOption func(*MultiSource)

// WithResolver installs the ambiguous-call resolver used for bare-name calls
// that collide.
func WithResolver(r Resolver) MultiOption {
	return func(m *MultiSource) { m.resolver = r }
}

// WithCollisionNotify installs a hook for name collisions. Tools invokes it
// whenever it finds a name claimed by multiple sources. The call is
// synchronous and runs under the source lock. The hook is meant for logging
// and metrics, not control flow.
func WithCollisionNotify(fn func(name string, sourceIDs []string)) MultiOption {
	return func(m *MultiSource) { m.onCollision = fn }
}

// NewMultiSource returns an empty aggregator.
func NewMultiSource(opts ...MultiOption) *MultiSource {
	m := &MultiSource{sources: make(map[string]ToolSource)}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Add registers a source under id. IDs must be unique. They must not contain
// "/", because "/" is reserved as the qualified-name separator. Rejecting it
// keeps "sourceID/name" parsing unambiguous. Underscores are fine, since id
// namespaces are commonly snake_case (e.g. "subagent:deep_researcher").
func (m *MultiSource) Add(id string, src ToolSource) error {
	if strings.Contains(id, "/") {
		return fmt.Errorf("agent: source id %q must not contain %q", id, "/")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sources[id]; exists {
		return fmt.Errorf("agent: source %q already added", id)
	}
	m.sources[id] = src
	m.order = append(m.order, id)
	m.index = nil
	return nil
}

// Invalidate drops the memoized tool index. The next Tools or Call gathers
// fresh lists from every source. Wire this to tools/list_changed
// notifications when the embedding host tracks them.
func (m *MultiSource) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.index = nil
}

// Remove drops a source. Unknown ids are a no-op.
func (m *MultiSource) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sources[id]; !ok {
		return
	}
	delete(m.sources, id)
	for i, v := range m.order {
		if v == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.index = nil
}

// Tools returns the merged, deduplicated model-facing list. Unique names
// appear as-is, in source order. A colliding name is replaced by a qualified
// name for every claimant.
func (m *MultiSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	claims, orderNames, err := m.indexLocked(ctx)
	if err != nil {
		return nil, err
	}

	var out []core.ToolDef
	for _, name := range orderNames {
		owners := claims[name]
		if len(owners) == 1 {
			out = append(out, owners[0].Def)
			continue
		}
		if m.onCollision != nil {
			ids := make([]string, len(owners))
			for i, o := range owners {
				ids[i] = o.SourceID
			}
			m.onCollision(name, ids)
		}
		for _, o := range owners {
			def := o.Def
			def.Name = qualifiedName(o.SourceID, name)
			out = append(out, def)
		}
	}
	return out, nil
}

// SourceTools returns the tools of the single source registered under id. The
// names are that source's own, unqualified. A caller uses this per-source view
// to show "what does server X expose". The merged Tools list differs, because
// it qualifies colliding names. found is false for an unknown id, which is app
// state and not an error. err is only a real listing failure from that source.
func (m *MultiSource) SourceTools(ctx context.Context, id string) (defs []core.ToolDef, found bool, err error) {
	m.mu.RLock()
	src, ok := m.sources[id]
	m.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	defs, err = src.Tools(ctx)
	return defs, true, err
}

// Call dispatches by bare or qualified name. It tries an exact unique bare
// name first. An ambiguous bare name goes through the Resolver. Then it tries
// the qualified "sourceID/name" form. A name miss against the memoized index
// triggers exactly one fresh gather before the call fails. So tools
// registered after the last listing are still reachable.
func (m *MultiSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	m.mu.Lock()
	claims, _, err := m.indexLocked(ctx)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	id, bare, resolveErr := m.resolveLocked(claims, name, args)
	if errors.Is(resolveErr, ErrUnknownTool) {
		m.index = nil
		if claims, _, err = m.indexLocked(ctx); err == nil {
			id, bare, resolveErr = m.resolveLocked(claims, name, args)
		}
	}
	src := m.sources[id]
	m.mu.Unlock()
	if resolveErr != nil {
		return nil, resolveErr
	}
	return src.Call(ctx, bare, args)
}

// OwnerOf reports which source would handle a call to name, without making it.
//
// It lets a caller key policy on where a tool came from. The host derives
// spotlight provenance this way. It takes args because resolution takes them.
// An ambiguous bare name goes through the Resolver, and the Resolver may
// inspect the args. Pass OwnerOf the same name and args that Call would get.
// It then names the exact source that Call would act on, not a plausible
// guess.
//
// found is false for an unknown tool, for an ambiguity that no Resolver
// settled, and for a listing error. Callers get a single "no answer" signal,
// because each of those means the same thing to a policy. The policy must not
// claim to know where the tool came from.
func (m *MultiSource) OwnerOf(ctx context.Context, name string, args map[string]any) (sourceID string, found bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	claims, _, err := m.indexLocked(ctx)
	if err != nil {
		return "", false
	}
	id, _, resolveErr := m.resolveLocked(claims, name, args)
	if errors.Is(resolveErr, ErrUnknownTool) {
		m.index = nil
		if claims, _, err = m.indexLocked(ctx); err != nil {
			return "", false
		}
		id, _, resolveErr = m.resolveLocked(claims, name, args)
	}
	if resolveErr != nil {
		return "", false
	}
	return id, true
}

// indexLocked returns the memoized claims index, gathering it if absent.
// Caller must hold the write lock.
func (m *MultiSource) indexLocked(ctx context.Context) (map[string][]ToolOwner, []string, error) {
	if m.index != nil {
		return m.index, m.indexOrder, nil
	}
	claims, orderNames, err := m.gatherLocked(ctx)
	if err != nil {
		return nil, nil, err
	}
	m.index, m.indexOrder = claims, orderNames
	return claims, orderNames, nil
}

// resolveLocked maps a called name to the source id that owns it and the bare
// name to dispatch. It returns the id instead of the source. OwnerOf needs to
// know WHERE a tool came from, and Call only needs to dispatch. Returning the
// id lets both share one resolution path.
func (m *MultiSource) resolveLocked(claims map[string][]ToolOwner, name string, args map[string]any) (sourceID, bare string, err error) {
	if owners, ok := claims[name]; ok {
		if len(owners) == 1 {
			return owners[0].SourceID, name, nil
		}
		if m.resolver != nil {
			id, err := m.resolver(name, owners, args)
			if err != nil {
				return "", "", err
			}
			if _, ok := m.sources[id]; ok {
				return id, name, nil
			}
			return "", "", fmt.Errorf("agent: resolver returned unknown source %q for tool %q", id, name)
		}
		var forms []string
		for _, o := range owners {
			forms = append(forms, qualifiedName(o.SourceID, name))
		}
		return "", "", fmt.Errorf("agent: tool %q is ambiguous; use one of: %s", name, strings.Join(forms, ", "))
	}

	if id, bareName, ok := splitQualified(name); ok {
		if _, exists := m.sources[id]; exists {
			for _, o := range claims[bareName] {
				if o.SourceID == id {
					return id, bareName, nil
				}
			}
		}
	}
	return "", "", fmt.Errorf("%w: %q", ErrUnknownTool, name)
}

func (m *MultiSource) gatherLocked(ctx context.Context) (map[string][]ToolOwner, []string, error) {
	claims := make(map[string][]ToolOwner)
	var orderNames []string
	for _, id := range m.order {
		defs, err := m.sources[id].Tools(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("agent: listing tools from source %q: %w", id, err)
		}
	defs:
		for _, def := range defs {
			// A source that repeats a name has a bug. Keep the first
			// definition, so qualification never emits duplicate names.
			for _, o := range claims[def.Name] {
				if o.SourceID == id {
					continue defs
				}
			}
			if _, seen := claims[def.Name]; !seen {
				orderNames = append(orderNames, def.Name)
			}
			claims[def.Name] = append(claims[def.Name], ToolOwner{SourceID: id, Def: def})
		}
	}
	for _, owners := range claims {
		sort.Slice(owners, func(i, j int) bool { return owners[i].SourceID < owners[j].SourceID })
	}
	return claims, orderNames, nil
}

// qualifiedName joins a source id and a tool name into the disambiguated form.
// MultiSource uses that form when multiple sources claim the same name. "/" is
// the separator. Add forbids it in source ids, and conventional MCP tool names
// don't use it. So splitQualified recovers the pair unambiguously. The
// separator need not be wire-legal. Providers sanitize it for the request and
// restore the real name from a full-string bijection before dispatch. So the
// split always runs on the real name, never on the sanitized wire form.
func qualifiedName(sourceID, name string) string {
	return sourceID + "/" + name
}

func splitQualified(name string) (sourceID, bare string, ok bool) {
	i := strings.Index(name, "/")
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}
