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
}

// DefaultLlamaCppBaseURL is llama-server's default address.
const DefaultLlamaCppBaseURL = "http://localhost:8080/v1"

// NewLlamaCppProvider returns an OpenAIProvider set up for llama-server
// (#180). It uses guided decoding in the llama.cpp dialect, so a grammar or a
// JSON schema constrains the reply. It also sends "cache_prompt": true. Then
// llama-server processes a prompt prefix shared across requests once, and the
// prefix's tokens come back as Usage.CachedInputTokens. Seeds are sent as
// given.
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
		ExtraBody:     map[string]any{"cache_prompt": true},
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
}

// DefaultOllamaBaseURL is Ollama's default address.
const DefaultOllamaBaseURL = "http://localhost:11434/v1"

// NewOllamaProvider returns an OpenAIProvider for Ollama's /v1 endpoint
// (#180), in the Ollama guided dialect. A GuidedDecoding.JSONSchema holds the
// reply to that schema. The schema is sent as response_format, and Ollama
// decodes under it. Ollama takes no grammar or regex, so a request with either
// is refused with ErrUnsupportedParam before anything is sent. Seeds are sent
// as given.
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
	})
}
