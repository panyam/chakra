package host

import (
	"context"
	"fmt"
	"sort"
	"sync"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/connection"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
)

// ConnectionsConfig is a named registry of model connections with one
// active. A playground config carries it so the user can switch models at
// runtime (/provider). When set, it supersedes Config.Model for the chat
// provider, and Model remains the single-connection quick-start.
//
// Later roles draw from the same registry. /embedder and /judgelm (epic 983)
// each pick an active connection for their role from this one set.
// Connections are model endpoints, and roles are assignments over them.
type ConnectionsConfig struct {
	// Active names the connection used for the chat provider at startup.
	// Must be a key in Connections.
	Active string `json:"active"`

	// Embedder names the connection used for the embedder role (semantic
	// memory), the counterpart of Active for embeddings. Optional; empty
	// means no config-provided embedder (a surface may still supply one by
	// flag). Must be a key in Connections, and that connection must speak an
	// embeddings API. Anthropic has none, so an "anthropic"-typed connection
	// is rejected. Set Dim on the named connection to the embedding width.
	Embedder string `json:"embedder,omitempty"`

	// Connections maps a caller-chosen name to its endpoint + model.
	Connections map[string]ConnectionConfig `json:"connections"`

	// AgentRoles assigns connections to model roles. A server-advertised agent
	// hints at a role with modelRoutingHint, from mcpkit's
	// experimental/ext/agents. The roles are "small", "medium" and "large",
	// each naming a key in Connections. A delegated agent runs on the
	// connection for its hinted role. If that role is unassigned, or its
	// connection lacks the vision the hint asks for, the agent runs on a larger
	// one, and never on a smaller one. When nothing fits, or the agent sends no
	// hint, it runs on the active connection. Optional; empty runs every agent
	// on the active connection.
	AgentRoles map[string]string `json:"agentRoles,omitempty"`

	// StepRoutes sends ranges of each turn's model calls to other
	// connections (#185), e.g. [{"fromStep": 2, "connection": "mini"}] to
	// plan on the active model and continue on a cheaper one. The first
	// route covering a step wins; a step none covers runs on the active
	// connection, so /provider still sets the default. Optional.
	StepRoutes []StepRoute `json:"stepRoutes,omitempty"`
}

// EmbedderConnection returns the connection named by Embedder and true, or a
// zero connection and false when no embedder role is set (or it names an
// undefined connection). It is the read side of the Embedder role, mirroring
// how the active connection is resolved for chat.
func (c *ConnectionsConfig) EmbedderConnection() (ConnectionConfig, bool) {
	if c == nil || c.Embedder == "" {
		return ConnectionConfig{}, false
	}
	conn, ok := c.Connections[c.Embedder]
	return conn, ok
}

// ConnectionConfig is one model endpoint. The type lives in package
// connection, so a program can build providers from a profile without
// importing host (#347).
type ConnectionConfig = connection.Config

// Price is a model's list price in US dollars per million tokens.
type Price = connection.Price

// ThinkingHint delimits a model's inline reasoning.
type ThinkingHint = connection.ThinkingHint

// ProviderBuilder builds a Provider from a resolved connection. Injected
// for tests (a builder that returns StubProviders); DefaultProviderBuilder
// is the real one.
type ProviderBuilder = connection.Builder

// ConnectionTypes replaces how connections of some Types are built, and
// leaves every other Type to DefaultProviderBuilder (#274). See
// connection.Types.
type ConnectionTypes = connection.Types

// DefaultProviderBuilder is connection.Build.
func DefaultProviderBuilder(conn ConnectionConfig) (agent.Provider, error) {
	return connection.Build(conn)
}

// buildProvider is DefaultProviderBuilder with a sink for the Responses
// provider's full-history notices, which the App turns into HostSessionWarn
// events.
func buildProvider(conn ConnectionConfig, onFullHistory func(reason string), types ConnectionTypes) (agent.Provider, error) {
	return connection.Options{Types: types, OnFullHistory: onFullHistory}.Build(conn)
}

// BuildEmbedder is connection.BuildEmbedder.
func BuildEmbedder(conn ConnectionConfig, tp core.TracerProvider) (agent.Embedder, error) {
	return connection.BuildEmbedder(conn, tp)
}

// connectionRegistry holds the named connections, the active one, and a
// cache of built providers. It is safe for concurrent use. SetActive
// switches connections at runtime, and Provider and ActiveProvider return
// built providers. Providers are built lazily and cached, so switching
// back to a used connection is instant.
type connectionRegistry struct {
	mu     sync.Mutex
	conns  map[string]ConnectionConfig
	names  []string // sorted, stable listing order
	active string
	build  ProviderBuilder
	cache  map[string]agent.Provider

	// agentRoles is ConnectionsConfig.AgentRoles, validated. Read-only after
	// construction.
	agentRoles map[string]string
}

// newConnectionRegistry validates cfg (Active must name a connection,
// every connection must resolve a base URL) and returns the registry. A
// nil build uses DefaultProviderBuilder. Providers are not built here.
// Each one is built on the first Provider, ActiveProvider or SetActive call
// that asks for it, so a misconfigured non-active connection does not fail
// startup.
func newConnectionRegistry(cfg *ConnectionsConfig, build ProviderBuilder) (*connectionRegistry, error) {
	if cfg == nil || len(cfg.Connections) == 0 {
		return nil, fmt.Errorf("host: connections config is empty")
	}
	if build == nil {
		build = DefaultProviderBuilder
	}
	names := make([]string, 0, len(cfg.Connections))
	for name, conn := range cfg.Connections {
		if conn.Model == "" && conn.Type != "llamacpp" {
			return nil, fmt.Errorf("host: connection %q has no model", name)
		}
		if _, err := connection.ResolveBaseURL(conn); err != nil {
			return nil, fmt.Errorf("host: connection %q: %w", name, err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	active := cfg.Active
	if active == "" {
		return nil, fmt.Errorf("host: connections config needs an active connection")
	}
	if _, ok := cfg.Connections[active]; !ok {
		return nil, fmt.Errorf("host: active connection %q is not defined", active)
	}
	if cfg.Embedder != "" {
		conn, ok := cfg.Connections[cfg.Embedder]
		if !ok {
			return nil, fmt.Errorf("host: embedder connection %q is not defined", cfg.Embedder)
		}
		if conn.Type == "anthropic" {
			return nil, fmt.Errorf("host: embedder connection %q is anthropic, which has no embeddings API", cfg.Embedder)
		}
		if _, err := connection.ResolveBaseURL(conn); err != nil {
			return nil, fmt.Errorf("host: embedder connection %q: %w", cfg.Embedder, err)
		}
	}
	for role, name := range cfg.AgentRoles {
		switch agents.ModelRole(role) {
		case agents.ModelRoleSmall, agents.ModelRoleMedium, agents.ModelRoleLarge:
		default:
			return nil, fmt.Errorf("host: agentRoles has unknown role %q (want small, medium or large)", role)
		}
		if _, ok := cfg.Connections[name]; !ok {
			return nil, fmt.Errorf("host: agentRoles %q names undefined connection %q", role, name)
		}
	}
	if err := validateStepRoutes(cfg.StepRoutes, cfg.Connections); err != nil {
		return nil, err
	}
	return &connectionRegistry{
		conns:      cfg.Connections,
		names:      names,
		active:     active,
		build:      build,
		cache:      map[string]agent.Provider{},
		agentRoles: cfg.AgentRoles,
	}, nil
}

// Names lists the connection names in stable sorted order.
func (r *connectionRegistry) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.names...)
}

// Active returns the name of the active connection.
func (r *connectionRegistry) Active() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// ModelOf returns the model id the named connection is configured with, or
// "" when it names none or no such connection exists.
func (r *connectionRegistry) ModelOf(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns[name].Model
}

// ActiveModel returns the model id the active connection is configured
// with, or "" when it names none.
func (r *connectionRegistry) ActiveModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns[r.active].Model
}

// providerLocked returns the built provider for name, building and caching
// it on first use. Caller holds r.mu.
func (r *connectionRegistry) providerLocked(name string) (agent.Provider, error) {
	if p, ok := r.cache[name]; ok {
		return p, nil
	}
	p, err := r.build(r.conns[name])
	if err != nil {
		return nil, fmt.Errorf("host: building provider %q: %w", name, err)
	}
	r.cache[name] = p
	return p, nil
}

// Provider builds (or returns the cached) provider for the named connection,
// without making it active. Server-advertised agents use it to run on the
// connection their routing hint resolves to. An unknown name is an error.
func (r *connectionRegistry) Provider(name string) (agent.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.conns[name]; !ok {
		return nil, fmt.Errorf("host: unknown connection %q", name)
	}
	return r.providerLocked(name)
}

// ActiveProvider builds (or returns the cached) provider for the active
// connection.
func (r *connectionRegistry) ActiveProvider() (agent.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.providerLocked(r.active)
}

// SetActive switches the active connection and returns its provider. An
// unknown name is an error and leaves the active connection unchanged; a
// build failure likewise leaves the previous active in place, so a failed
// switch never strands the session without a provider.
func (r *connectionRegistry) SetActive(name string) (agent.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.conns[name]; !ok {
		return nil, fmt.Errorf("host: unknown connection %q", name)
	}
	p, err := r.providerLocked(name)
	if err != nil {
		return nil, err
	}
	r.active = name
	return p, nil
}

// providerSwitch is an agent.Provider whose underlying provider can be
// swapped at runtime. The Runner holds one of these, so /provider never
// rebuilds the Runner; a swap takes effect on the next model call (an
// in-flight stream finishes on the provider it started with).
type providerSwitch struct {
	mu sync.RWMutex
	p  agent.Provider
}

func newProviderSwitch(p agent.Provider) *providerSwitch { return &providerSwitch{p: p} }

func (s *providerSwitch) set(p agent.Provider) {
	s.mu.Lock()
	s.p = p
	s.mu.Unlock()
}

func (s *providerSwitch) current() agent.Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.p
}

// ProviderCapabilities reports the current provider's. A switch changes
// them, so a caller asks again per call rather than caching the answer.
func (s *providerSwitch) ProviderCapabilities() agent.Capabilities {
	return agent.CapabilitiesOf(s.current())
}

func (s *providerSwitch) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return s.current().Stream(ctx, req)
}

func (s *providerSwitch) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return s.current().Generate(ctx, req)
}
