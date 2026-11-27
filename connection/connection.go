// Package connection turns a model connection, as an agentchat.json profile
// writes it, into an agent.Provider (#347). A connection is one model
// endpoint: its type or base URL, its model, where its key comes from, and
// what it costs.
//
// The package depends on the root module alone, so a program that builds
// providers from a profile needn't import host and its servers. host uses
// it for its own connections, so there is one builder.
package connection

import (
	"fmt"
	"os"
	"sort"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Config is one model endpoint. Type resolves to a base URL through the
// built-in table, and BaseURL overrides it for anything else. Exactly one of
// a known Type or a BaseURL must resolve, or Build fails.
type Config struct {
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

	// MaxTokens caps each reply's length. Read for Type "anthropic", whose
	// API requires a cap on every request, and for "llamacpp" and "ollama",
	// whose servers otherwise generate until the context is full. Zero keeps
	// the provider's default of 4096 (agent.DefaultLocalMaxTokens for the
	// local two), which cuts off a long file write or patch partway through,
	// and the turn then goes on as if the model had stopped there. Set it to
	// what the model allows for agentic work (#264). For a local server, a
	// negative value sends no cap.
	MaxTokens int `json:"maxTokens,omitempty"`

	// Dim is the embedding vector width, meaningful only when this
	// connection is used as an embedder (host's ConnectionsConfig.Embedder
	// role). It must match the embedding model's true output width; the
	// durable pgvector store sizes its vector column from it. Ignored for
	// chat connections.
	Dim int `json:"dim,omitempty"`

	// ThinkingHint describes how this model delimits inline reasoning, for
	// models that emit it in the text stream (e.g. <think>…</think>). When
	// set, Build wraps the provider so the delimited text is re-tagged as
	// reasoning (agent.NewThinkingProvider). A nil hint or empty CloseTag
	// leaves the provider unwrapped.
	ThinkingHint *ThinkingHint `json:"thinkingHint,omitempty"`

	// Vision marks a model that accepts image input. It is read only when
	// this connection is assigned an agent role (host's
	// ConnectionsConfig.AgentRoles), where an agent hinting vision skips
	// connections without it.
	Vision bool `json:"vision,omitempty"`

	// ReasoningEffort is sent as the Responses API's reasoning.effort, with
	// summary "auto" so the reasoning streams as thinking. On the plain
	// OpenAI wire it is sent as reasoning_effort, which OpenAI's reasoning
	// models and Gemini's OpenAI endpoint read; Gemini thinks only when it
	// is set. Only reasoning models accept it: leave it empty for any other
	// model, or the request is refused. llamacpp, ollama and the root
	// module's anthropic provider ignore it. A binary that
	// routes "anthropic" through Anthropic's SDK (Types) sends it as
	// output_config.effort there, and the root module's provider ignores it.
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
	// chakra keeps no price table, because vendor prices change and a
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
func (c Config) Cost(u *agent.Usage) (float64, bool) {
	if c.Price == nil || u == nil {
		return 0, false
	}
	return c.Price.Cost(*u), true
}

// Cost prices u in dollars. Cached input tokens go at CachedInput, or at
// Input when CachedInput is nil, and the rest of the input at Input. A
// cached count above InputTokens is capped at it, since a provider that
// reports more cached tokens than prompt tokens is miscounting.
//
// Anthropic bills cache writes above the input rate. Usage has no field for
// them and counts them in InputTokens, so Cost bills them at Input and comes
// out low for a call that wrote the cache.
func (p Price) Cost(u agent.Usage) float64 {
	cachedRate := p.Input
	if p.CachedInput != nil {
		cachedRate = *p.CachedInput
	}
	cached := min(u.CachedInputTokens, u.InputTokens)
	fresh := u.InputTokens - cached
	return (float64(fresh)*p.Input + float64(cached)*cachedRate + float64(u.OutputTokens)*p.Output) / 1e6
}

// ThinkingHint delimits a model's inline reasoning. An empty OpenTag
// means reasoning starts at the stream head and runs until CloseTag (the
// "no open tag" models); an empty CloseTag means the hint is inert.
type ThinkingHint struct {
	OpenTag  string `json:"openTag,omitempty"`
	CloseTag string `json:"closeTag,omitempty"`
}

// baseURLs maps a built-in Type to its default API root: local runtimes, the
// OpenAI cloud, Gemini's OpenAI-compatible endpoint, Anthropic, and the
// OpenAI-wire model routers (OpenRouter cloud, LiteLLM proxy). Anything else
// sets BaseURL. Every type here speaks the OpenAI wire except "anthropic",
// which Build routes to the native Messages-API provider. Its base still
// resolves here, so validation and BaseURL overrides work the same for every
// type. "litellm" defaults to the proxy's local port, and a hosted
// deployment sets BaseURL.
var baseURLs = map[string]string{
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

// ResolveBaseURL returns the connection's endpoint root: BaseURL when set,
// else the Type's default, else an error naming the known types.
func ResolveBaseURL(c Config) (string, error) {
	if c.BaseURL != "" {
		return c.BaseURL, nil
	}
	if base, ok := baseURLs[c.Type]; ok {
		return base, nil
	}
	if c.Type == "" {
		return "", fmt.Errorf("connection: needs a baseUrl or a known type (%s)", knownTypes())
	}
	return "", fmt.Errorf("connection: unknown type %q (set baseUrl, or use one of %s)", c.Type, knownTypes())
}

func knownTypes() string {
	ts := make([]string, 0, len(baseURLs))
	for t := range baseURLs {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	return strings.Join(ts, ", ")
}

// apiKey is APIKey, else the value of APIKeyEnv, else empty.
func apiKey(c Config) string {
	if c.APIKey == "" && c.APIKeyEnv != "" {
		return os.Getenv(c.APIKeyEnv)
	}
	return c.APIKey
}

// Builder builds a Provider from a connection. Build is the real one, and a
// test injects one that returns StubProviders.
type Builder func(Config) (agent.Provider, error)

// Types replaces how connections of some Types are built, and leaves every
// other Type to Build (#274). A binary that links a vendor SDK uses it to
// route one Type to that SDK, since the root module carries no SDK
// (CONSTRAINTS A10). agentchat and agentweb build "anthropic" through
// Anthropic's SDK this way, which sends prompt caching.
//
// A Builder in Types gets the connection with APIKey and BaseURL resolved:
// the key from APIKey, else APIKeyEnv, and the base URL from BaseURL, else
// the Type's default. ThinkingHint still applies to what it returns.
type Types map[string]Builder

// Build is the package's Build with t's overrides.
func (t Types) Build(c Config) (agent.Provider, error) {
	return Options{Types: t}.Build(c)
}

// Options are what Build can take beyond the connection.
type Options struct {
	// Types overrides how some Types are built.
	Types Types
	// OnFullHistory receives the Responses provider's notices that it resent
	// the full history (agent.ResponsesConfig.OnFullHistory). host turns
	// them into warnings. Nil drops them.
	OnFullHistory func(reason string)
}

// Build is the package's Build with o's overrides and sink.
func (o Options) Build(c Config) (agent.Provider, error) {
	base, err := ResolveBaseURL(c)
	if err != nil {
		return nil, err
	}
	key := apiKey(c)
	var p agent.Provider
	if build, ok := o.Types[c.Type]; ok {
		resolved := c
		resolved.APIKey, resolved.BaseURL = key, base
		p, err = build(resolved)
	} else {
		p, err = builtin(c, base, key, o.OnFullHistory)
	}
	if err != nil {
		return nil, err
	}
	// A model that writes "<think>…</think>" in its text gets that part
	// re-tagged as reasoning. NewThinkingProvider returns p unwrapped when
	// CloseTag is empty.
	if h := c.ThinkingHint; h != nil {
		p = agent.NewThinkingProvider(p, h.OpenTag, h.CloseTag)
	}
	return p, nil
}

// Build builds the provider for a connection: the native Anthropic
// Messages-API provider when Type is "anthropic", the Responses provider for
// "openai-responses", the llama.cpp or Ollama preset for "llamacpp" and
// "ollama", and otherwise a plain OpenAI-wire provider. The base URL comes
// from BaseURL or the Type table, and the key from APIKey, else APIKeyEnv.
// It contacts no endpoint.
func Build(c Config) (agent.Provider, error) {
	return Options{}.Build(c)
}

func builtin(c Config, base, key string, onFullHistory func(reason string)) (agent.Provider, error) {
	switch c.Type {
	case "anthropic":
		return agent.NewAnthropicProvider(agent.AnthropicConfig{
			BaseURL:   base,
			Model:     c.Model,
			APIKey:    key,
			MaxTokens: c.MaxTokens,
		})
	case "openai-responses":
		return agent.NewResponsesProvider(agent.ResponsesConfig{
			BaseURL:         base,
			Model:           c.Model,
			APIKey:          key,
			ReasoningEffort: c.ReasoningEffort,
			Temperature:     c.Temperature != nil && *c.Temperature,
			OnFullHistory:   onFullHistory,
		})
	case "llamacpp":
		return agent.NewLlamaCppProvider(agent.LlamaCppConfig{BaseURL: base, Model: c.Model, APIKey: key, MaxTokens: c.MaxTokens})
	case "ollama":
		return agent.NewOllamaProvider(agent.OllamaConfig{BaseURL: base, Model: c.Model, APIKey: key, MaxTokens: c.MaxTokens})
	default:
		var extra map[string]any
		if c.ReasoningEffort != "" {
			extra = map[string]any{"reasoning_effort": c.ReasoningEffort}
		}
		return agent.NewOpenAIProvider(agent.OpenAIConfig{
			BaseURL:           base,
			Model:             c.Model,
			APIKey:            key,
			NoTemperature:     c.Temperature != nil && !*c.Temperature,
			NoResponseSchema:  c.ResponseSchema != nil && !*c.ResponseSchema,
			ExtraBody:         extra,
			ThoughtSignatures: c.Type == "gemini",
			LateSystemAsUser:  c.Type == "gemini",
		})
	}
}

// BuildEmbedder builds an agent.Embedder from an embedder connection, the
// counterpart of Build for the embedder role. Embeddings are an OpenAI-wire
// concern (an /embeddings endpoint), so every type except "anthropic"
// resolves through the OpenAI-compatible embedder, and "anthropic", which
// has no embeddings API, is refused. The key comes from APIKey, else
// APIKeyEnv, as for Build. tp opts the embedder into its
// agent.embed span; nil is fine.
func BuildEmbedder(c Config, tp core.TracerProvider) (agent.Embedder, error) {
	if c.Type == "anthropic" {
		return nil, fmt.Errorf("connection: type %q has no embeddings API; use an openai/gemini/local embedder", c.Type)
	}
	base, err := ResolveBaseURL(c)
	if err != nil {
		return nil, err
	}
	return agent.NewOpenAIEmbedder(agent.OpenAIEmbedderConfig{
		BaseURL:        base,
		Model:          c.Model,
		APIKey:         apiKey(c),
		TracerProvider: tp,
	})
}
