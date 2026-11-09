package host

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/panyam/chakra"
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

// ConnectionConfig is one model endpoint. Type resolves to a base URL via
// the built-in table (lmstudio / openai / ollama); BaseURL overrides it
// for anything else. Exactly one of a known Type or a BaseURL must
// resolve, or construction fails.
type ConnectionConfig struct {
	// Type names a built-in endpoint profile ("lmstudio", "llamacpp",
	// "openai", "openai-responses", "ollama", "gemini", "anthropic",
	// "openrouter", "litellm"). "openai-responses" builds through
	// agent.NewResponsesProvider against OpenAI's /v1/responses, which keeps
	// a reasoning model's reasoning across tool steps.
	// "llamacpp" and "ollama" build through agent.NewLlamaCppProvider and
	// agent.NewOllamaProvider, so a llama-server connection takes a grammar
	// and caches its prompt with no further settings. All but
	// "anthropic" speak the OpenAI wire; "anthropic" uses the native
	// Messages-API provider (x-api-key auth, /v1/messages). "openrouter" and
	// "litellm" are model-router gateways (set BaseURL to override the
	// litellm proxy's local default). Optional when BaseURL is set.
	Type string `json:"type,omitempty"`

	// BaseURL is the API root including any version prefix, e.g.
	// "http://localhost:1234/v1". Overrides Type's default.
	BaseURL string `json:"baseUrl,omitempty"`

	// Model is the model identifier sent on every request. Required, except
	// for "llamacpp", where a llama-server serving one model ignores it.
	Model string `json:"model"`

	// APIKeyEnv names the environment variable holding the bearer key.
	// Empty means unauthenticated (local endpoints).
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`

	// APIKey is the key itself, for a caller that has one in hand rather than
	// in the environment (agentweb, where the browser supplies it). It wins
	// over APIKeyEnv. It is never read from or written to a config file.
	APIKey string `json:"-"`

	// MaxTokens caps each reply's length. Read only for Type "anthropic",
	// whose API requires a cap on every request. Zero keeps the provider's
	// default of 4096, which cuts off a long file write or patch partway
	// through, and the turn then goes on as if the model had stopped there.
	// Set it to what the model allows for agentic work (#264).
	MaxTokens int `json:"maxTokens,omitempty"`

	// Dim is the embedding vector width, meaningful only when this
	// connection is used as an embedder (the ConnectionsConfig.Embedder
	// role). It must match the embedding model's true output width; the
	// durable pgvector store sizes its vector column from it. Ignored for
	// chat connections.
	Dim int `json:"dim,omitempty"`

	// ThinkingHint describes how this model delimits inline reasoning, for
	// models that emit it in the text stream (e.g. <think>…</think>). When
	// set, DefaultProviderBuilder wraps the provider so the delimited text is
	// re-tagged as reasoning (agent.NewThinkingProvider). A nil hint or empty
	// CloseTag leaves the provider unwrapped.
	ThinkingHint *ThinkingHint `json:"thinkingHint,omitempty"`

	// Vision marks a model that accepts image input. It is read only when
	// this connection is assigned an agent role (ConnectionsConfig.AgentRoles),
	// where an agent hinting vision skips connections without it.
	Vision bool `json:"vision,omitempty"`

	// ReasoningEffort is sent as the Responses API's reasoning.effort, with
	// summary "auto" so the reasoning streams as thinking. Read only for
	// Type "openai-responses", and only reasoning models accept it: leave it
	// empty for any other model, or the request is refused. A binary that
	// routes "anthropic" through Anthropic's SDK (ConnectionTypes) sends it
	// as output_config.effort there, and the host's own provider ignores it.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`

	// Temperature overrides whether this connection's model takes a
	// temperature, and agent.Capabilities.Temperature reports that answer.
	// Nil keeps the type's default: true for the OpenAI-wire types,
	// false for "openai-responses" and "anthropic". Set false for OpenAI's
	// reasoning models on an OpenAI-wire connection (gpt-5 and later, the o
	// series), and true for a non-reasoning model on "openai-responses". The
	// "llamacpp", "ollama" and "anthropic" types ignore it, since their
	// answer doesn't vary by model. It changes what the provider reports, not
	// what it sends.
	Temperature *bool `json:"temperature,omitempty"`

	// ResponseSchema overrides whether this connection's endpoint takes
	// response_format json_schema. A ResponseSchema (the structured finalize)
	// sends that format. Nil means it does. Set false for DeepSeek, which
	// answers 400 to json_schema: the schema then goes in the system message
	// and the request asks for json_object (agent.OpenAIConfig.NoResponseSchema).
	// Read only by the OpenAI-wire types other than "llamacpp" and "ollama".
	ResponseSchema *bool `json:"responseSchema,omitempty"`

	// Price is what this connection's model costs, so a surface can report a
	// run's cost (#327). Nil means the cost is unknown, as for a local model.
	// The host keeps no price table, because vendor prices change and a
	// benchmark needs the prices it ran with written down beside the run.
	Price *Price `json:"price,omitempty"`
}

// Price is a model's list price in US dollars per million tokens.
type Price struct {
	// Input is the rate for prompt tokens that no cache served.
	Input float64 `json:"input"`

	// CachedInput is the rate for prompt tokens the provider's cache served
	// (agent.Usage.CachedInputTokens). Nil bills them at Input.
	CachedInput *float64 `json:"cachedInput,omitempty"`

	// Output is the rate for completion tokens, reasoning included.
	Output float64 `json:"output"`
}

// Cost prices one usage report at the connection's Price. It returns false
// when the connection has no Price or the provider reported no usage, since
// either way the cost is unknown rather than zero.
//
// Anthropic bills cache writes above the input rate. Usage has no field for
// them and counts them in InputTokens, so Cost bills them at Input and comes
// out low for a call that wrote the cache.
func (c ConnectionConfig) Cost(u *agent.Usage) (float64, bool) {
	if c.Price == nil || u == nil {
		return 0, false
	}
	cachedRate := c.Price.Input
	if c.Price.CachedInput != nil {
		cachedRate = *c.Price.CachedInput
	}
	cached := min(u.CachedInputTokens, u.InputTokens)
	fresh := u.InputTokens - cached
	return (float64(fresh)*c.Price.Input + float64(cached)*cachedRate + float64(u.OutputTokens)*c.Price.Output) / 1e6, true
}

// ThinkingHint delimits a model's inline reasoning. An empty OpenTag
// means reasoning starts at the stream head and runs until CloseTag (the
// "no open tag" models); an empty CloseTag means the hint is inert.
type ThinkingHint struct {
	OpenTag  string `json:"openTag,omitempty"`
	CloseTag string `json:"closeTag,omitempty"`
}

// connectionBaseURLs maps a built-in Type to its default API root: local
// runtimes, the OpenAI cloud, Gemini's OpenAI-compatible endpoint, Anthropic,
// and the OpenAI-wire model routers (OpenRouter cloud, LiteLLM proxy). Anything
// else sets BaseURL explicitly. Every type here is OpenAI-wire EXCEPT
// "anthropic". DefaultProviderBuilder routes "anthropic" to the native
// Messages-API provider (see below). Its base still resolves here, so registry
// validation and BaseURL overrides work the same for every type. A router is
// just a connection whose base URL is the gateway; "litellm" defaults to the
// proxy's local port and is typically overridden with BaseURL for a hosted
// deployment.
var connectionBaseURLs = map[string]string{
	"lmstudio":         "http://localhost:1234/v1",
	"llamacpp":         agent.DefaultLlamaCppBaseURL,
	"openai":           "https://api.openai.com/v1",
	"openai-responses": "https://api.openai.com/v1",
	"ollama":           agent.DefaultOllamaBaseURL,
	"gemini":           "https://generativelanguage.googleapis.com/v1beta/openai",
	"anthropic":        "https://api.anthropic.com",
	"openrouter":       "https://openrouter.ai/api/v1",
	"litellm":          "http://localhost:4000/v1",
}

// ProviderBuilder builds a Provider from a resolved connection. Injected
// for tests (a builder that returns StubProviders); DefaultProviderBuilder
// is the real OpenAI-compatible path.
type ProviderBuilder func(ConnectionConfig) (agent.Provider, error)

// DefaultProviderBuilder builds the provider for a connection: the native
// Anthropic Messages-API provider when Type is "anthropic", the llama.cpp or
// Ollama preset for "llamacpp" and "ollama", otherwise a plain OpenAI-wire
// provider (lmstudio / openai / gemini / any BaseURL).
// The base URL resolves from BaseURL or the Type table and the key from
// APIKey, else APIKeyEnv.
func DefaultProviderBuilder(conn ConnectionConfig) (agent.Provider, error) {
	return buildProvider(conn, nil, nil)
}

// ConnectionTypes replaces how connections of some Types are built, and
// leaves every other Type to DefaultProviderBuilder (#274). A binary that
// links a vendor SDK uses it to route one Type to that SDK, since the host
// module carries no SDK itself (CONSTRAINTS A10). agentchat and agentweb
// build "anthropic" through Anthropic's SDK this way, which sends prompt
// caching.
//
// A builder gets the connection with APIKey and BaseURL resolved: the key
// from APIKey, else APIKeyEnv, and the base URL from BaseURL, else the
// Type's default. The host still applies ThinkingHint to what it returns.
type ConnectionTypes map[string]ProviderBuilder

// Build is DefaultProviderBuilder with t's overrides.
func (t ConnectionTypes) Build(conn ConnectionConfig) (agent.Provider, error) {
	return buildProvider(conn, nil, t)
}

// buildProvider is DefaultProviderBuilder with a sink for the Responses
// provider's full-history notices, which the App turns into HostSessionWarn
// events.
func buildProvider(conn ConnectionConfig, onFullHistory func(reason string), types ConnectionTypes) (agent.Provider, error) {
	base, err := resolveBaseURL(conn)
	if err != nil {
		return nil, err
	}
	key := conn.APIKey
	if key == "" && conn.APIKeyEnv != "" {
		key = os.Getenv(conn.APIKeyEnv)
	}
	var p agent.Provider
	if build, ok := types[conn.Type]; ok {
		resolved := conn
		resolved.APIKey, resolved.BaseURL = key, base
		p, err = build(resolved)
	} else {
		p, err = builtinProvider(conn, base, key, onFullHistory)
	}
	if err != nil {
		return nil, err
	}
	// A ThinkingHint activates the inline-reasoning parser: a model that emits
	// "<think>…</think>" in its text stream gets that content re-tagged as
	// reasoning, so the surface renders it as thinking. Inert when CloseTag is
	// empty (NewThinkingProvider returns p unwrapped).
	if h := conn.ThinkingHint; h != nil {
		p = agent.NewThinkingProvider(p, h.OpenTag, h.CloseTag)
	}
	return p, nil
}

// builtinProvider builds the provider for one of the host's own Types.
func builtinProvider(conn ConnectionConfig, base, key string, onFullHistory func(reason string)) (p agent.Provider, err error) {
	switch conn.Type {
	case "anthropic":
		p, err = agent.NewAnthropicProvider(agent.AnthropicConfig{
			BaseURL:   base,
			Model:     conn.Model,
			APIKey:    key,
			MaxTokens: conn.MaxTokens,
		})
	case "openai-responses":
		p, err = agent.NewResponsesProvider(agent.ResponsesConfig{
			BaseURL:         base,
			Model:           conn.Model,
			APIKey:          key,
			ReasoningEffort: conn.ReasoningEffort,
			Temperature:     conn.Temperature != nil && *conn.Temperature,
			OnFullHistory:   onFullHistory,
		})
	case "llamacpp":
		p, err = agent.NewLlamaCppProvider(agent.LlamaCppConfig{BaseURL: base, Model: conn.Model, APIKey: key})
	case "ollama":
		p, err = agent.NewOllamaProvider(agent.OllamaConfig{BaseURL: base, Model: conn.Model, APIKey: key})
	default:
		p, err = agent.NewOpenAIProvider(agent.OpenAIConfig{
			BaseURL:          base,
			Model:            conn.Model,
			APIKey:           key,
			NoTemperature:    conn.Temperature != nil && !*conn.Temperature,
			NoResponseSchema: conn.ResponseSchema != nil && !*conn.ResponseSchema,
		})
	}
	return p, err
}

// BuildEmbedder builds an agent.Embedder from an embedder connection, the
// counterpart of DefaultProviderBuilder for the embedder role. Embeddings are
// an OpenAI-wire concern (an /embeddings endpoint), so every supported type
// except "anthropic" resolves through the OpenAI-compatible embedder. Anthropic
// exposes no embeddings API, so that type is rejected. tp opts the embedder
// into its agent.embed span; nil is fine.
func BuildEmbedder(conn ConnectionConfig, tp core.TracerProvider) (agent.Embedder, error) {
	if conn.Type == "anthropic" {
		return nil, fmt.Errorf("host: connection type %q has no embeddings API; use an openai/gemini/local embedder", conn.Type)
	}
	base, err := resolveBaseURL(conn)
	if err != nil {
		return nil, err
	}
	var key string
	if conn.APIKeyEnv != "" {
		key = os.Getenv(conn.APIKeyEnv)
	}
	return agent.NewOpenAIEmbedder(agent.OpenAIEmbedderConfig{
		BaseURL:        base,
		Model:          conn.Model,
		APIKey:         key,
		TracerProvider: tp,
	})
}

// resolveBaseURL returns the endpoint root: BaseURL wins, else the Type
// table, else an error naming the fix.
func resolveBaseURL(conn ConnectionConfig) (string, error) {
	if conn.BaseURL != "" {
		return conn.BaseURL, nil
	}
	if base, ok := connectionBaseURLs[conn.Type]; ok {
		return base, nil
	}
	if conn.Type == "" {
		return "", fmt.Errorf("host: connection needs a baseUrl or a known type (%s)", knownTypes())
	}
	return "", fmt.Errorf("host: unknown connection type %q (set baseUrl, or use one of %s)", conn.Type, knownTypes())
}

func knownTypes() string {
	ts := make([]string, 0, len(connectionBaseURLs))
	for t := range connectionBaseURLs {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	out := ""
	for i, t := range ts {
		if i > 0 {
			out += ", "
		}
		out += t
	}
	return out
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
		if _, err := resolveBaseURL(conn); err != nil {
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
		if _, err := resolveBaseURL(conn); err != nil {
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
