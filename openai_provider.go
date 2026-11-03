package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	ssehttp "github.com/panyam/servicekit/http"
)

// OpenAIConfig configures an OpenAI-compatible chat-completions endpoint
// (OpenAI, lmstudio, vllm, LiteLLM-style proxies, gateways).
type OpenAIConfig struct {
	// BaseURL is the API root including any version prefix, e.g.
	// "http://localhost:1234/v1". The provider appends "/chat/completions".
	BaseURL string

	// APIKey, when non-empty, is sent as a Bearer token. Local servers
	// commonly need none.
	APIKey string

	// Model is the model identifier sent on every request.
	Model string

	// HTTPClient overrides http.DefaultClient. Set this for proxies,
	// custom TLS, or timeouts (note: an overall client timeout also bounds
	// streaming reads; prefer per-request ctx deadlines for streams).
	HTTPClient *http.Client

	// GuidedDialect names the server family whose request fields carry
	// ProviderRequest.Guided. Guided decoding has no OpenAI-standard field,
	// so the provider cannot guess one. Empty means the endpoint takes no
	// constraint and a request carrying one is refused. An unknown value
	// fails NewOpenAIProvider.
	GuidedDialect GuidedDialect

	// ExtraBody holds server-specific top-level request fields sent on every
	// request, such as llama.cpp's "cache_prompt": true. A key the provider
	// sets for the request (model, messages, temperature when set, and so on)
	// keeps the provider's value, so ExtraBody fills gaps and never rewrites
	// the conversation. NewOpenAIProvider takes a shallow copy; changing the
	// map afterwards has no effect.
	ExtraBody map[string]any
}

// GuidedDialect selects how OpenAIProvider encodes ProviderRequest.Guided.
type GuidedDialect string

const (
	// GuidedDialectLlamaCpp targets llama.cpp's llama-server: Grammar goes
	// out as "grammar" (GBNF) and JSONSchema as "json_schema". It has no
	// regex mode, so a Regex constraint is refused.
	GuidedDialectLlamaCpp GuidedDialect = "llamacpp"

	// GuidedDialectVLLM targets vLLM 0.12 and later: the constraint goes out
	// as "structured_outputs" with one of "grammar", "regex" or "json". The
	// older guided_* fields were removed in 0.12 and are not sent.
	GuidedDialectVLLM GuidedDialect = "vllm"

	// GuidedDialectOllama targets Ollama's OpenAI-compatible API, whose only
	// constraint is a JSON schema: JSONSchema goes out as response_format
	// json_schema, which Ollama turns into its native format. A Grammar or
	// Regex is refused, and so is a request also carrying ResponseSchema,
	// which would claim the same field.
	GuidedDialectOllama GuidedDialect = "ollama"
)

// OpenAIProvider implements Provider over the OpenAI-compatible
// chat-completions wire with no SDK dependency (net/http plus servicekit's
// WHATWG-conformant SSE reader). Safe for concurrent use.
type OpenAIProvider struct {
	cfg  OpenAIConfig
	http *http.Client
}

// NewOpenAIProvider validates cfg and returns a provider. BaseURL and Model
// are required.
func NewOpenAIProvider(cfg OpenAIConfig) (*OpenAIProvider, error) {
	if cfg.BaseURL == "" || cfg.Model == "" {
		return nil, fmt.Errorf("agent: OpenAIConfig requires BaseURL and Model")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	switch cfg.GuidedDialect {
	case "", GuidedDialectLlamaCpp, GuidedDialectVLLM, GuidedDialectOllama:
	default:
		return nil, fmt.Errorf("agent: unknown OpenAIConfig.GuidedDialect %q", cfg.GuidedDialect)
	}
	cfg.ExtraBody = maps.Clone(cfg.ExtraBody)
	return &OpenAIProvider{cfg: cfg, http: hc}, nil
}

// ProviderCapabilities reports a seed always, since the provider sends one
// whenever it's set, and the guided kinds its GuidedDialect encodes: grammar
// and JSON schema for llama.cpp, all three for vLLM, JSON schema for Ollama,
// none without a dialect.
func (p *OpenAIProvider) ProviderCapabilities() Capabilities {
	c := Capabilities{Seed: true}
	switch p.cfg.GuidedDialect {
	case GuidedDialectLlamaCpp:
		c.Guided = []string{GuidedGrammar, GuidedJSONSchema}
	case GuidedDialectVLLM:
		c.Guided = []string{GuidedGrammar, GuidedRegex, GuidedJSONSchema}
	case GuidedDialectOllama:
		c.Guided = []string{GuidedJSONSchema}
	}
	return c
}

// check refuses, before anything is sent, a request this endpoint cannot
// honor as asked.
func (p *OpenAIProvider) check(req ProviderRequest) error {
	if req.Guided == nil {
		return nil
	}
	kind, err := req.Guided.kind()
	if err != nil {
		return err
	}
	switch {
	case p.cfg.GuidedDialect == "":
		return fmt.Errorf("%w: Guided (OpenAIConfig.GuidedDialect is not set)", ErrUnsupportedParam)
	case p.cfg.GuidedDialect == GuidedDialectLlamaCpp && kind == GuidedRegex:
		return fmt.Errorf("%w: Guided.Regex (llama.cpp has no regex constraint)", ErrUnsupportedParam)
	case p.cfg.GuidedDialect == GuidedDialectOllama && kind != GuidedJSONSchema:
		return fmt.Errorf("%w: Guided.%s (Ollama takes a JSON schema only)", ErrUnsupportedParam, kind)
	case p.cfg.GuidedDialect == GuidedDialectOllama && req.ResponseSchema.Len() > 0:
		return fmt.Errorf("%w: Guided with ResponseSchema (both are Ollama's response_format)", ErrUnsupportedParam)
	}
	return nil
}

// guidedFields returns the top-level body fields carrying g under the
// configured dialect. check has already validated the pair.
func (p *OpenAIProvider) guidedFields(g *GuidedDecoding) map[string]any {
	var schema any
	if g.JSONSchema.Len() > 0 {
		_ = g.JSONSchema.Bind(&schema)
	}
	switch p.cfg.GuidedDialect {
	case GuidedDialectLlamaCpp:
		if g.Grammar != "" {
			return map[string]any{"grammar": g.Grammar}
		}
		return map[string]any{"json_schema": schema}
	case GuidedDialectVLLM:
		so := map[string]any{}
		switch {
		case g.Grammar != "":
			so["grammar"] = g.Grammar
		case g.Regex != "":
			so["regex"] = g.Regex
		default:
			so["json"] = schema
		}
		return map[string]any{"structured_outputs": so}
	case GuidedDialectOllama:
		return map[string]any{"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "guided", "schema": schema},
		}}
	}
	return nil
}

// ProviderError reports a non-2xx response from the model endpoint. The body
// is included verbatim (truncated to 2 KB) because OpenAI-compatible servers
// put the useful diagnostics there.
type ProviderError struct {
	StatusCode int
	Body       string
}

// Error implements error.
func (e *ProviderError) Error() string {
	return fmt.Sprintf("agent: provider returned HTTP %d: %s", e.StatusCode, e.Body)
}

// Stream implements Provider. Deltas map 1:1 from SSE chunks; tool calls
// arrive as DeltaToolCallStart followed by DeltaToolCallArgs fragments on the
// same index. The stream ends with io.EOF after the servers [DONE] marker.
func (p *OpenAIProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	if err := p.check(req); err != nil {
		return nil, err
	}
	body := p.buildBody(req, true)
	resp, err := p.post(ctx, body)
	if err != nil {
		return nil, err
	}
	_, safeToReal := toolNameMaps(req.Tools)
	st := &openaiStream{nameMap: safeToReal}
	st.sseStream = sseStream{ctx: ctx, body: resp.Body, events: ssehttp.NewSSEEventReader(resp.Body), decode: st.decode}
	return st, nil
}

// Generate implements Provider with a non-streaming request. When
// req.ResponseSchema is set, the request carries response_format json_schema
// and the structured document is returned in ProviderResponse.Text.
func (p *OpenAIProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	if err := p.check(req); err != nil {
		return nil, err
	}
	body := p.buildBody(req, false)
	resp, err := p.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var full struct {
		Choices []struct {
			Message struct {
				Content          string       `json:"content"`
				ReasoningContent string       `json:"reasoning_content"`
				ToolCalls        []oaToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage   *oaUsage   `json:"usage"`
		Timings *oaTimings `json:"timings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		return nil, fmt.Errorf("agent: decode completion: %w", err)
	}
	if len(full.Choices) == 0 {
		return nil, fmt.Errorf("agent: completion had no choices")
	}
	choice := full.Choices[0]
	out := &ProviderResponse{
		Text:         choice.Message.Content,
		Reasoning:    choice.Message.ReasoningContent,
		FinishReason: choice.FinishReason,
	}
	_, safeToReal := toolNameMaps(req.Tools)
	for _, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: realToolName(safeToReal, tc.Function.Name), Args: toolCallArgsFromJSON(json.RawMessage(args))})
	}
	if full.Usage != nil {
		out.Usage = full.Usage.toUsage(full.Timings)
	}
	return out, nil
}

func (p *OpenAIProvider) post(ctx context.Context, body map[string]any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("agent: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(p.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &ProviderError{StatusCode: resp.StatusCode, Body: string(msg)}
	}
	return resp, nil
}

// buildBody produces the chat-completions request. Kept as one method so the
// wire-shape test pins the exact JSON we emit.
func (p *OpenAIProvider) buildBody(req ProviderRequest, stream bool) map[string]any {
	realToSafe, _ := toolNameMaps(req.Tools)
	msgs := make([]map[string]any, 0, len(req.Messages)+1)
	if req.Instructions != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.Instructions})
	}
	for _, m := range req.Messages {
		entry := map[string]any{"role": string(m.Role)}
		switch m.Role {
		case RoleTool:
			entry["tool_call_id"] = m.ToolCallID
			entry["content"] = m.Text
		case RoleAssistant:
			if m.Text != "" || len(m.ToolCalls) == 0 {
				entry["content"] = m.Text
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]map[string]any, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					calls[i] = map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      safeToolName(realToSafe, tc.Name),
							"arguments": string(tc.Args.Raw()),
						},
					}
				}
				entry["tool_calls"] = calls
			}
		default:
			entry["content"] = m.Text
		}
		msgs = append(msgs, entry)
	}

	body := map[string]any{
		"model":    p.cfg.Model,
		"messages": msgs,
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, td := range req.Tools {
			tools[i] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        realToSafe[td.Name],
					"description": td.Description,
					"parameters":  td.InputSchema,
				},
			}
		}
		body["tools"] = tools
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Seed != nil {
		body["seed"] = *req.Seed
	}
	if req.Guided != nil {
		maps.Copy(body, p.guidedFields(req.Guided))
	}
	if len(req.Tools) > 0 && !req.ToolChoice.IsZero() {
		choice := req.ToolChoice
		if choice.Mode == "function" {
			choice.Name = safeToolName(realToSafe, choice.Name) // match the sanitized tools-list name
		}
		if tc := choice.wire(); tc != nil {
			body["tool_choice"] = tc
		}
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	} else if req.ResponseSchema.Len() > 0 {
		var schema any
		if req.ResponseSchema.Bind(&schema) == nil {
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "response",
					"schema": schema,
				},
			}
		}
	}
	for k, v := range p.cfg.ExtraBody {
		if _, set := body[k]; !set {
			body[k] = v
		}
	}
	return body
}

type oaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// oaTimings is llama.cpp's own report, beside usage. Only cache_n is read,
// for builds that predate prompt_tokens_details.
type oaTimings struct {
	CacheN int `json:"cache_n"`
}

func (u *oaUsage) toUsage(t *oaTimings) *Usage {
	out := &Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	switch {
	case u.PromptTokensDetails != nil:
		out.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	case t != nil:
		out.CachedInputTokens = t.CacheN
	}
	return out
}

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content          string       `json:"content"`
			ReasoningContent string       `json:"reasoning_content"`
			Reasoning        string       `json:"reasoning"`
			ToolCalls        []oaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage   *oaUsage   `json:"usage"`
	Timings *oaTimings `json:"timings"`
}

// openaiStream adapts the SSE body to the Stream interface via servicekit's
// WHATWG-conformant SSEEventReader (multi-line data joining, BOM stripping,
// comment skipping). Recv keeps a small queue because one SSE event can
// expand to several Deltas.
type openaiStream struct {
	sseStream
	started map[int]bool
	nameMap map[string]string // safe→real tool name, to reverse sanitized names on tool_call deltas
}

// decode turns one SSE data payload into Deltas. The "[DONE]" sentinel ends the
// stream; otherwise the chunk is unmarshalled and expanded via chunkDeltas.
func (s *openaiStream) decode(payload string) ([]Delta, bool, error) {
	if payload == "[DONE]" {
		return nil, true, nil
	}
	var chunk oaChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return nil, false, fmt.Errorf("agent: bad SSE chunk: %w", err)
	}
	return s.chunkDeltas(chunk), false, nil
}

// chunkDeltas expands one chat-completions chunk into Deltas. It keeps the
// per-index started map so a tool call's first fragment is a DeltaToolCallStart
// and the rest are DeltaToolCallArgs.
func (s *openaiStream) chunkDeltas(chunk oaChunk) []Delta {
	var out []Delta
	if chunk.Usage != nil {
		out = append(out, Delta{Kind: DeltaUsage, Usage: chunk.Usage.toUsage(chunk.Timings)})
	}
	if len(chunk.Choices) == 0 {
		return out
	}
	choice := chunk.Choices[0]
	if choice.Delta.Content != "" {
		out = append(out, Delta{Kind: DeltaText, Text: choice.Delta.Content})
	}
	if r := choice.Delta.ReasoningContent + choice.Delta.Reasoning; r != "" {
		out = append(out, Delta{Kind: DeltaReasoning, Text: r})
	}
	for _, tc := range choice.Delta.ToolCalls {
		if s.started == nil {
			s.started = make(map[int]bool)
		}
		if !s.started[tc.Index] {
			s.started[tc.Index] = true
			out = append(out, Delta{
				Kind:       DeltaToolCallStart,
				Index:      tc.Index,
				ToolCallID: tc.ID,
				ToolName:   realToolName(s.nameMap, tc.Function.Name),
				Text:       tc.Function.Arguments,
			})
			continue
		}
		if tc.Function.Arguments != "" {
			out = append(out, Delta{Kind: DeltaToolCallArgs, Index: tc.Index, Text: tc.Function.Arguments})
		}
	}
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		out = append(out, Delta{Kind: DeltaFinish, FinishReason: *choice.FinishReason})
	}
	return out
}
