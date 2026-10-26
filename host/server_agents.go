package host

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
)

// delegateArgs is the {task} shape a server-advertised agent's delegate tool
// advertises to the supervisor model. It matches the default AgentSource input
// schema, so the supervisor's args pass straight through to the child.
type delegateArgs struct {
	Task string `json:"task"`
}

// delegateSchema is the delegate tool's input schema, built once.
var delegateSchema = mustSchema[delegateArgs]()

func mustSchema[T any]() any {
	var s any
	if err := json.Unmarshal(core.GenerateSchema[T](), &s); err != nil {
		panic(fmt.Sprintf("host: schema for delegate args: %v", err))
	}
	return s
}

// listChangedFallbackTTL is how long a roster or definition is kept when the
// server sends no ttlMs but does declare listChanged. The notification already
// tells the host when the roster changes, so a server that relies on it should
// not cost an agents/list on every index rebuild and an agents/get on every
// delegation. A server without listChanged gets no fallback, which is the
// spec's reading of an absent ttlMs.
const listChangedFallbackTTL = 5 * time.Minute

// registerServerAgents discovers each connected server's advertised agent
// roster (experimental/ext/agents) and adds a lazy delegate source per server
// to the main aggregate, so the supervisor model can delegate to a
// server-advertised agent exactly like any other sub-agent.
//
// Progressive disclosure is the point: only the roster (agents/list, routing
// fields, no tool schemas) is pulled to build the delegate tools. An agent's
// instructions and scoped tools (agents/get) resolve on first delegation, so a
// large roster never bloats the supervisor's context. This mirrors
// catalog-mode skills.
//
// Both lookups go through an agentsclient.Cache, which keeps each one for the
// server's ttlMs. A notifications/agents/list_changed from the server drops the
// cached roster and the aggregate's tool index, so added and removed agents
// show up on the supervisor's next step.
//
// Per-server failures degrade to a warning (never a boot failure): a server
// that is unreachable, does not speak the extension, or has an empty roster
// and no listChanged is skipped. Discovery runs over the servers connected at
// boot; a server that connects later does not retroactively contribute a
// roster, matching the static membership of config-declared sub-agents.
func (a *App) registerServerAgents(multi *agent.MultiSource, models agentModels, tp core.TracerProvider, mp core.MeterProvider, servers []ServerConfig, clientByID map[string]*client.Client) {
	for _, sc := range servers {
		if sc.Agents != nil && !*sc.Agents {
			continue
		}
		c := clientByID[sc.ID]
		if c == nil {
			continue
		}
		ac := agentsclient.New(c)
		if !ac.SupportsAgents() {
			continue
		}
		cache, listChanged := newServerAgentCache(c)
		roster, err := cache.ListAgents(context.Background())
		if err != nil {
			a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("discover agents on %s: %v", sc.ID, err)})
			continue
		}
		if len(roster) == 0 && !listChanged {
			continue
		}
		src := newServerAgentSource(serverAgentDeps{
			catalog:         cache,
			backing:         agent.NewClientSource(c),
			models:          models,
			maxSteps:        a.cfg.MaxSteps,
			tp:              tp,
			mp:              mp,
			onEvent:         func(e agent.SubAgentEvent) { a.emit(HostEvent{Kind: HostSubAgentEvent, SubAgent: e}) },
			onRosterChanged: multi.Invalidate,
		})
		if err := multi.Add("serveragents:"+sc.ID, src); err != nil {
			a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("register agents for %s: %v", sc.ID, err)})
			continue
		}
		a.listWatch.set(sc.ID, agents.NotificationListChanged, func() {
			cache.Invalidate()
			multi.Invalidate()
		})
		a.emit(HostEvent{Kind: HostMessage, Message: fmt.Sprintf("server %s advertises %d delegatable agent(s)", sc.ID, len(roster))})
	}
	multi.Invalidate()
}

// newServerAgentCache builds the roster and definition cache for one server,
// with listChangedFallbackTTL when the server declares listChanged, and
// reports whether it did.
func newServerAgentCache(c *client.Client) (*agentsclient.Cache, bool) {
	listChanged := serverDeclaresAgentsListChanged(c)
	var opts []agentsclient.CacheOption
	if listChanged {
		opts = append(opts, agentsclient.WithFallbackTTL(listChangedFallbackTTL))
	}
	return agentsclient.NewCache(agentsclient.New(c), opts...), listChanged
}

// serverDeclaresAgentsListChanged reports whether the server's agents
// extension settings carry listChanged: true.
func serverDeclaresAgentsListChanged(c *client.Client) bool {
	settings, ok := c.ServerExtensionCapability(agents.ExtensionID)
	if !ok {
		return false
	}
	v, _ := settings["listChanged"].(bool)
	return v
}

// listWatchers routes a server's list_changed notifications (agents, events)
// to the handler registered for that server and method. The hook has to exist
// when the client is built, before discovery has made the state it refreshes,
// so the client's notification callback looks the handler up here. chakra's
// clients use the session wire, where these arrive on the GET stream through
// client.WithNotificationCallback; on the stateless wire an extension's own
// subscriptions/listen helper delivers them instead (see watchEventTypes).
type listWatchers struct {
	mu sync.Mutex
	by map[string]func() // serverID + " " + method
}

func (w *listWatchers) set(serverID, method string, fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.by == nil {
		w.by = map[string]func(){}
	}
	w.by[serverID+" "+method] = fn
}

// notify returns the client notification callback for one server. It runs on
// the client's reader goroutine, so a handler must not block.
func (w *listWatchers) notify(serverID string) func(method string, params any) {
	return func(method string, _ any) {
		w.mu.Lock()
		fn := w.by[serverID+" "+method]
		w.mu.Unlock()
		if fn != nil {
			fn()
		}
	}
}

// agentModels maps an agent's modelRoutingHint to a provider. Roles name
// connections in the registry, and the table's Default is the empty name,
// meaning the main provider, so /provider switching still applies to every
// agent the hint does not place elsewhere.
type agentModels struct {
	main  agent.Provider
	reg   *ConnectionRegistry
	table agentsclient.RoleTable[string]
}

// newAgentModels builds the role table from Connections.AgentRoles. A nil
// registry, or one with no agent roles, sends every agent to main.
func newAgentModels(main agent.Provider, reg *ConnectionRegistry) agentModels {
	m := agentModels{main: main, reg: reg}
	if reg == nil {
		return m
	}
	m.table.Roles = map[agents.ModelRole]agentsclient.RoleModel[string]{}
	for role, name := range reg.agentRoles {
		m.table.Roles[agents.ModelRole(role)] = agentsclient.RoleModel[string]{Model: name, Vision: reg.conns[name].Vision}
	}
	return m
}

// modelChoice is what pick decided, recorded on the agents.resolve span.
type modelChoice struct {
	provider   agent.Provider
	connection string // "" means the main provider
	how        agentsclient.Resolution
	err        error // a role connection that failed to build; main was used
}

func (m agentModels) pick(h *agents.ModelRoutingHint) modelChoice {
	name, how := m.table.Resolve(h)
	if name == "" || m.reg == nil {
		return modelChoice{provider: m.main, how: how}
	}
	p, err := m.reg.Provider(name)
	if err != nil {
		return modelChoice{provider: m.main, how: agentsclient.ResolvedDefault, err: err}
	}
	return modelChoice{provider: p, connection: name, how: how}
}

// agentCatalog is the roster and definition lookup a serverAgentSource reads.
// *agentsclient.Cache satisfies it.
type agentCatalog interface {
	ListAgents(ctx context.Context) ([]agents.AgentCard, error)
	GetAgent(ctx context.Context, name string) (agents.GetAgentResult, error)
}

// serverAgentDeps bundles what one server's lazy delegate source needs.
type serverAgentDeps struct {
	catalog  agentCatalog
	backing  agent.ToolSource
	models   agentModels
	maxSteps int
	tp       core.TracerProvider
	mp       core.MeterProvider
	onEvent  func(agent.SubAgentEvent)

	// onRosterChanged drops the aggregate's tool index. It runs when a
	// delegation finds the agent gone, so the stale delegate tool disappears.
	onRosterChanged func()
}

// serverAgentSource is the lazy delegate ToolSource for one server's advertised
// agents. It advertises one delegate tool per roster entry (from agents/list)
// and resolves an agent's definition (agents/get) into a Runner-backed
// agent.AgentSource on delegation. The child is kept and reused until the
// definition the catalog returns changes, and is rebuilt then. It is the host
// half of the issue-1144 bridge; agent.NewServerAgentSource is the adapter.
type serverAgentSource struct {
	deps serverAgentDeps

	mu     sync.Mutex
	byTool map[string]agents.AgentCard // delegate tool name -> card, from the last listing
	built  map[string]builtAgent       // agent name -> resolved child
}

// builtAgent is a resolved child and the definition it was built from.
type builtAgent struct {
	src    *agent.AgentSource
	detail agents.GetAgentResult
}

func newServerAgentSource(deps serverAgentDeps) *serverAgentSource {
	if deps.tp == nil {
		deps.tp = core.NoopTracerProvider{}
	}
	return &serverAgentSource{
		deps:   deps,
		byTool: map[string]agents.AgentCard{},
		built:  map[string]builtAgent{},
	}
}

// describeAgent folds a roster entry's routing fields (description and
// capability labels) into the delegate tool's description, so the supervisor
// model can route without the agent's tool schemas.
func describeAgent(sum agents.AgentCard) string {
	var b strings.Builder
	b.WriteString(sum.Description)
	if len(sum.Capabilities) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("Capabilities: " + strings.Join(sum.Capabilities, ", ") + ".")
	}
	return b.String()
}

// Tools returns one delegate tool per agent in the catalog's roster. No
// agents/get is issued, so the scoped schemas stay out of the supervisor's view
// until the agent is actually delegated to. The aggregate memoizes the result
// until its index is invalidated. When the roster cannot be fetched, the last
// listing is served, because an error here would fail the whole aggregate and
// with it every other server's tools.
func (s *serverAgentSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	roster, err := s.deps.catalog.ListAgents(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		// Name is unique within a server, per the extension, so it is the
		// delegate tool name and never collides within this source.
		byTool := make(map[string]agents.AgentCard, len(roster))
		for _, card := range roster {
			if _, dup := byTool[card.Name]; !dup {
				byTool[card.Name] = card
			}
		}
		s.byTool = byTool
		for name := range s.built {
			if _, ok := byTool[name]; !ok {
				delete(s.built, name)
			}
		}
	}
	names := make([]string, 0, len(s.byTool))
	for name := range s.byTool {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]core.ToolDef, len(names))
	for i, name := range names {
		out[i] = core.ToolDef{Name: name, Description: describeAgent(s.byTool[name]), InputSchema: delegateSchema}
	}
	return out, nil
}

// Call resolves the named agent and runs it. An unknown delegate name is
// ErrUnknownTool (a dispatch miss the aggregate can qualify). A resolution
// failure is a model-visible IsError result so the supervisor's turn
// continues, mirroring how AgentSource turns a child failure into an IsError
// result rather than aborting the turn. The two failures the extension names
// get their own wording: agent_not_found also drops the stale delegate from
// the aggregate, and invalid_agent_definition says the fault is the server's.
func (s *serverAgentSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	card, ok := s.card(name)
	if !ok {
		// An agent added since the last listing is still reachable: list
		// once more before calling it a miss.
		_, _ = s.Tools(ctx)
		if card, ok = s.card(name); !ok {
			return nil, fmt.Errorf("%w: %q", agent.ErrUnknownTool, name)
		}
	}
	child, err := s.resolve(ctx, card)
	if err != nil {
		return &core.ToolResult{IsError: true, Content: []core.Content{{Type: "text", Text: s.describeResolveError(card.Name, err)}}}, nil
	}
	return child.Call(ctx, name, args)
}

func (s *serverAgentSource) card(name string) (agents.AgentCard, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	card, ok := s.byTool[name]
	return card, ok
}

func (s *serverAgentSource) describeResolveError(name string, err error) string {
	switch {
	case agentsclient.IsAgentNotFound(err):
		s.mu.Lock()
		delete(s.byTool, name)
		delete(s.built, name)
		s.mu.Unlock()
		if s.deps.onRosterChanged != nil {
			s.deps.onRosterChanged()
		}
		return fmt.Sprintf("agent %q no longer exists on this server and has been removed from your tools", name)
	case agentsclient.IsInvalidAgentDefinition(err):
		return fmt.Sprintf("the server's definition of agent %q is invalid, so it cannot run: %v", name, err)
	default:
		return fmt.Sprintf("could not resolve agent %q: %v", name, err)
	}
}

// resolve returns the child AgentSource for an agent. The first resolution of
// an agent runs inside an agents.resolve span, so a trace reads supervisor ->
// resolve(agent.id) -> get -> child. Later delegations ask the catalog again,
// which answers from its cache while the TTL holds. When the definition comes
// back unchanged the existing child is reused and nothing is spanned. When it
// changed, the child is rebuilt under a fresh agents.resolve span, which then
// sits beside the get rather than around it. The catalog call runs outside the
// lock so concurrent delegations to different agents don't serialize on the
// network. A lost race rebuilds harmlessly and the last stored wins.
func (s *serverAgentSource) resolve(ctx context.Context, card agents.AgentCard) (*agent.AgentSource, error) {
	id := card.Name
	s.mu.Lock()
	prev, had := s.built[id]
	s.mu.Unlock()

	var span core.Span
	if !had {
		ctx, span = s.deps.tp.StartSpan(ctx, "agents.resolve", core.Attribute{Key: "mcp.agent.id", Value: id})
		defer span.End()
	}
	detail, err := s.deps.catalog.GetAgent(ctx, id)
	if err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return nil, err
	}
	if had && reflect.DeepEqual(prev.detail, detail) {
		return prev.src, nil
	}
	if span == nil {
		ctx, span = s.deps.tp.StartSpan(ctx, "agents.resolve", core.Attribute{Key: "mcp.agent.id", Value: id})
		defer span.End()
	}

	choice := s.deps.models.pick(detail.ModelRoutingHint)
	span.SetAttribute("mcp.agent.model_resolution", choice.how.String())
	if choice.connection != "" {
		span.SetAttribute("mcp.agent.connection", choice.connection)
	}
	if choice.err != nil {
		span.RecordError(choice.err)
	}
	child, err := agent.NewServerAgentSource(agent.ServerAgentConfig{
		Name:           card.Name,
		Description:    describeAgent(card),
		Instructions:   detail.Instructions,
		Tools:          detail.Tools,
		Backing:        s.deps.backing,
		Provider:       choice.provider,
		MaxSteps:       s.deps.maxSteps,
		OnEvent:        s.deps.onEvent,
		TracerProvider: s.deps.tp,
		MeterProvider:  s.deps.mp,
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	s.mu.Lock()
	s.built[id] = builtAgent{src: child, detail: detail}
	s.mu.Unlock()
	return child, nil
}
