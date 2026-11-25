package agent

import (
	"fmt"
	"net/http"
)

// LlamaCppConfig configures a provider for llama.cpp's llama-server. Every
// field is optional.
type LlamaCppConfig struct {
	// BaseURL is the server's OpenAI-compatible API root, including /v1.
	// Empty means DefaultLlamaCppBaseURL.
	BaseURL string

	// Model is sent as the request's model. Empty means "llama.cpp". A
	// llama-server serving one model ignores the name, but chakra always
	// sends one. A server routing between several models reads it.
	Model string

	// APIKey, when set, is sent as a Bearer token, for a server started
	// with --api-key.
	APIKey string

	// HTTPClient overrides http.DefaultClient, as in OpenAIConfig.
	HTTPClient *http.Client

	// MaxTokens caps each reply's length when a request sets no cap. 0 means
	// DefaultLocalMaxTokens. A negative value sends none, and llama-server
	// then generates until the context is full.
	MaxTokens int
}

// DefaultLocalMaxTokens is the reply cap the llama.cpp and Ollama presets
// send when neither the config nor the request sets one. Both servers
// otherwise generate until the context is full. Qwen2.5-Coder 32B on
// Declaire's bench wrote one reply past 6,600 tokens at about 4 tokens a
// second, and the run stalled on it for half an hour. The cap matches
// AnthropicConfig's default.
const DefaultLocalMaxTokens = 4096

// localMaxTokens is the cap a local preset sends for n.
func localMaxTokens(n int) int {
	switch {
	case n == 0:
		return DefaultLocalMaxTokens
	case n < 0:
		return 0
	}
	return n
}

// DefaultLlamaCppBaseURL is llama-server's default address.
const DefaultLlamaCppBaseURL = "http://localhost:8080/v1"

// NewLlamaCppProvider returns an OpenAIProvider set up for llama-server
// (#180). It uses guided decoding in the llama.cpp dialect, so a grammar or a
// JSON schema constrains the reply. It also sends "cache_prompt": true. Then
// llama-server processes a prompt prefix shared across requests once, and the
// prefix's tokens come back as Usage.CachedInputTokens. Seeds are sent as
// given, and so is every SamplingPenalties field, DRY included (#276).
//
// It reads tool calls a model writes as text (OpenAIConfig.TextToolCalls),
// since llama-server parses some models' tool calls and not others',
// Qwen2.5-Coder's among the second.
//
// A reply is capped at MaxTokens, DefaultLocalMaxTokens unless set, when the
// request sets no cap of its own.
//
// The caller sets Temperature. A request with none leaves llama-server
// sampling at its own default, 0.8. A caller wanting greedy, repeatable
// decoding sets 0.
func NewLlamaCppProvider(cfg LlamaCppConfig) (*OpenAIProvider, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultLlamaCppBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = "llama.cpp"
	}
	return NewOpenAIProvider(OpenAIConfig{
		BaseURL:       cfg.BaseURL,
		Model:         cfg.Model,
		APIKey:        cfg.APIKey,
		HTTPClient:    cfg.HTTPClient,
		GuidedDialect: GuidedDialectLlamaCpp,
		Penalties:     []string{PenaltyRepeat, PenaltyFrequency, PenaltyPresence, PenaltyDRY},
		ExtraBody:     map[string]any{"cache_prompt": true},
		TextToolCalls: true,
		MaxTokens:     localMaxTokens(cfg.MaxTokens),
	})
}

// OllamaConfig configures a provider for Ollama's OpenAI-compatible API.
type OllamaConfig struct {
	// BaseURL is Ollama's OpenAI-compatible API root, including /v1. Empty
	// means DefaultOllamaBaseURL.
	BaseURL string

	// Model names the pulled model to run, e.g. "qwen2.5-coder:7b".
	// Required, since Ollama serves many.
	Model string

	// APIKey, when set, is sent as a Bearer token, for an Ollama behind an
	// authenticating proxy.
	APIKey string

	// HTTPClient overrides http.DefaultClient, as in OpenAIConfig.
	HTTPClient *http.Client

	// MaxTokens caps each reply's length when a request sets no cap, as in
	// LlamaCppConfig: 0 means DefaultLocalMaxTokens, and a negative value
	// sends none.
	MaxTokens int
}

// DefaultOllamaBaseURL is Ollama's default address.
const DefaultOllamaBaseURL = "http://localhost:11434/v1"

// NewOllamaProvider returns an OpenAIProvider for Ollama's /v1 endpoint
// (#180), in the Ollama guided dialect. A GuidedDecoding.JSONSchema holds the
// reply to that schema. The schema is sent as response_format, and Ollama
// decodes under it. Ollama takes no grammar or regex, so a request with either
// is refused with ErrUnsupportedParam before anything is sent. Seeds are sent
// as given. Of the SamplingPenalties, Ollama's /v1 reads Frequency and
// Presence. It ignores repeat_penalty there and has no DRY sampler, so those
// are refused. A reply is capped as NewLlamaCppProvider caps one.
func NewOllamaProvider(cfg OllamaConfig) (*OpenAIProvider, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("agent: OllamaConfig requires Model")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultOllamaBaseURL
	}
	return NewOpenAIProvider(OpenAIConfig{
		BaseURL:       cfg.BaseURL,
		Model:         cfg.Model,
		APIKey:        cfg.APIKey,
		HTTPClient:    cfg.HTTPClient,
		GuidedDialect: GuidedDialectOllama,
		MaxTokens:     localMaxTokens(cfg.MaxTokens),
	})
}
