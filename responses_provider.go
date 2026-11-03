package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	ssehttp "github.com/panyam/servicekit/http"
)

// ResponsesConfig configures a provider for OpenAI's Responses API
// (POST {BaseURL}/responses).
type ResponsesConfig struct {
	// BaseURL is the API root including the version prefix, e.g.
	// "https://api.openai.com/v1". The provider appends "/responses".
	BaseURL string

	// APIKey, when non-empty, is sent as a Bearer token.
	APIKey string

	// Model is the model identifier sent on every request.
	Model string

	// HTTPClient overrides http.DefaultClient. As with OpenAIConfig, an
	// overall client timeout also bounds streaming reads.
	HTTPClient *http.Client

	// ReasoningEffort, when set, is sent as reasoning.effort ("minimal",
	// "low", "medium", "high"). Setting it or ReasoningSummary sends the
	// reasoning object, which only reasoning models accept: a non-reasoning
	// model rejects the request with a 400.
	ReasoningEffort string

	// ReasoningSummary, when set, is sent as reasoning.summary ("auto",
	// "concise", "detailed"). The summary text streams as DeltaReasoning.
	// Empty with ReasoningEffort set defaults to "auto", since a reasoning
	// model with no summary shows the surface nothing while it thinks.
	ReasoningSummary string

	// NoStore sends store:false and never chains requests, so every request
	// carries the full history and the model starts each step without its
	// earlier reasoning. Set it for an organization with zero data retention,
	// where the server refuses to store responses.
	NoStore bool

	// CursorCacheSize bounds how many response ids the provider remembers,
	// one per distinct conversation prefix it has answered. Zero means 256.
	// An evicted conversation falls back to full history once and then
	// chains again.
	CursorCacheSize int

	// OnFullHistory, when set, is called when a request that continues a
	// conversation (its history holds an assistant message) has to send the
	// full history instead of chaining from a stored response. reason says
	// why. It is not called for the first request of a conversation, nor
	// when NoStore is set. It runs on the calling goroutine before the
	// request is sent and must not block.
	OnFullHistory func(reason string)
}

// Reasons passed to ResponsesConfig.OnFullHistory.
const (
	// FullHistoryNoCursor means no remembered response matches the history
	// before its last assistant message: the history was compacted, edited,
	// forked or resumed, the provider switched, or the entry was evicted.
	FullHistoryNoCursor = "no stored response matches this history"

	// FullHistoryStaleCursor means the server no longer had the remembered
	// response (expired or deleted), so the request was sent again with full
	// history.
	FullHistoryStaleCursor = "the stored response is gone from the server"
)

// ResponsesProvider implements Provider over OpenAI's Responses API with no
// SDK dependency. Safe for concurrent use.
//
// Reasoning continuity across tool steps comes from server-side storage: each
// response is stored (store:true), and the provider remembers its id keyed by
// a hash of the conversation it answered. A later request whose history
// extends that conversation with tool results or user messages only is sent
// as previous_response_id plus the new messages, so the server replays the
// model's reasoning items. Nothing opaque crosses the Provider seam, which
// keeps Message wire-clean (A2) and the loop unaware of the cursor (A9).
//
// Any other history (compacted, edited, forked, resumed from a RunStore, or
// first seen by this provider instance) goes out in full, without the earlier
// reasoning, and OnFullHistory says so.
type ResponsesProvider struct {
	cfg     ResponsesConfig
	http    *http.Client
	cursors *cursorCache
}

// NewResponsesProvider validates cfg and returns a provider. BaseURL and
// Model are required.
func NewResponsesProvider(cfg ResponsesConfig) (*ResponsesProvider, error) {
	if cfg.BaseURL == "" || cfg.Model == "" {
		return nil, fmt.Errorf("agent: ResponsesConfig requires BaseURL and Model")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	size := cfg.CursorCacheSize
	if size <= 0 {
		size = 256
	}
	return &ResponsesProvider{cfg: cfg, http: hc, cursors: newCursorCache(size)}, nil
}

// ProviderCapabilities reports nothing: the Responses API has no seed and no
// grammar or regex constraint.
func (p *ResponsesProvider) ProviderCapabilities() Capabilities { return Capabilities{} }

func (p *ResponsesProvider) check(req ProviderRequest) error {
	if req.Seed != nil {
		return fmt.Errorf("%w: Seed (the Responses API has no seed)", ErrUnsupportedParam)
	}
	if req.Guided != nil {
		return fmt.Errorf("%w: Guided (the Responses API has no guided decoding)", ErrUnsupportedParam)
	}
	return nil
}

// Stream implements Provider. Text, reasoning summaries and function calls
// stream as their Delta kinds; the stream ends with io.EOF after
// response.completed. A response cut short by max_output_tokens finishes
// with FinishReason "length"; a failed response surfaces as a Recv error.
func (p *ResponsesProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	if err := p.check(req); err != nil {
		return nil, err
	}
	resp, plan, err := p.send(ctx, req, true)
	if err != nil {
		return nil, err
	}
	_, safeToReal := toolNameMaps(req.Tools)
	st := &responsesStream{p: p, plan: plan, nameMap: safeToReal}
	st.sseStream = sseStream{ctx: ctx, body: resp.Body, events: ssehttp.NewSSEEventReader(resp.Body), decode: st.decode}
	return st, nil
}

// Generate implements Provider with a non-streaming request. When
// req.ResponseSchema is set, the request carries text.format json_schema and
// the structured document is returned in ProviderResponse.Text.
func (p *ResponsesProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	if err := p.check(req); err != nil {
		return nil, err
	}
	resp, plan, err := p.send(ctx, req, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var full rsResponse
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		return nil, fmt.Errorf("agent: decode response: %w", err)
	}
	if full.Error != nil {
		return nil, fmt.Errorf("agent: response failed: %s", full.Error.Message)
	}
	_, safeToReal := toolNameMaps(req.Tools)
	out := full.toProviderResponse(safeToReal)
	p.remember(plan, full.ID, out)
	return out, nil
}

// requestPlan is what one request was sent as: the cursor key of the history
// it carried, so the reply can be remembered under history + reply.
type requestPlan struct {
	history []Message
}

// send builds and posts the request, chaining from a remembered response when
// the history allows it. A server that no longer has that response gets the
// request again with full history.
func (p *ResponsesProvider) send(ctx context.Context, req ProviderRequest, stream bool) (*http.Response, requestPlan, error) {
	plan := requestPlan{history: req.Messages}
	input, prev := req.Messages, ""
	if !p.cfg.NoStore {
		if k := lastAssistant(req.Messages) + 1; k > 0 {
			if id, ok := p.cursors.get(cursorKey(req.Messages[:k])); ok {
				input, prev = req.Messages[k:], id
			} else {
				p.notify(FullHistoryNoCursor)
			}
		}
	}
	resp, err := p.post(ctx, p.buildBody(req, input, prev, stream))
	if err != nil && prev != "" && isStaleCursor(err) {
		p.cursors.drop(cursorKey(req.Messages[:lastAssistant(req.Messages)+1]))
		p.notify(FullHistoryStaleCursor)
		resp, err = p.post(ctx, p.buildBody(req, req.Messages, "", stream))
	}
	return resp, plan, err
}

func (p *ResponsesProvider) notify(reason string) {
	if p.cfg.OnFullHistory != nil {
		p.cfg.OnFullHistory(reason)
	}
}

// remember stores id under the history the request carried plus the
// assistant message the Runner will append for out.
func (p *ResponsesProvider) remember(plan requestPlan, id string, out *ProviderResponse) {
	if p.cfg.NoStore || id == "" {
		return
	}
	next := append(plan.history[:len(plan.history):len(plan.history)], Message{Role: RoleAssistant, Text: out.Text, ToolCalls: out.ToolCalls})
	p.cursors.put(cursorKey(next), id)
}

func lastAssistant(msgs []Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleAssistant {
			return i
		}
	}
	return -1
}

// isStaleCursor reports a refusal of previous_response_id. OpenAI answers a
// missing or expired response with a 400 or 404 whose body names the
// parameter or the previous_response_not_found code.
func isStaleCursor(err error) bool {
	var pe *ProviderError
	if !errors.As(err, &pe) || (pe.StatusCode != http.StatusNotFound && pe.StatusCode != http.StatusBadRequest) {
		return false
	}
	return strings.Contains(pe.Body, "previous_response")
}

func (p *ResponsesProvider) post(ctx context.Context, body map[string]any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("agent: encode request: %w", err)
	}
	return sendWithRetry(ctx, p.http, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimSuffix(p.cfg.BaseURL, "/")+"/responses", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if p.cfg.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		}
		return httpReq, nil
	})
}

// buildBody produces the Responses request carrying input, chained from prev
// when non-empty. Instructions and tools are sent on every request, since the
// server does not carry them over from the previous response.
func (p *ResponsesProvider) buildBody(req ProviderRequest, input []Message, prev string, stream bool) map[string]any {
	realToSafe, _ := toolNameMaps(req.Tools)
	items := make([]map[string]any, 0, len(input))
	for _, m := range input {
		switch m.Role {
		case RoleTool:
			items = append(items, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Text})
		case RoleAssistant:
			if m.Text != "" || len(m.ToolCalls) == 0 {
				items = append(items, map[string]any{"role": "assistant", "content": m.Text})
			}
			for _, tc := range m.ToolCalls {
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      safeToolName(realToSafe, tc.Name),
					"arguments": string(tc.Args.Raw()),
				})
			}
		default:
			items = append(items, map[string]any{"role": string(m.Role), "content": m.Text})
		}
	}

	body := map[string]any{
		"model": p.cfg.Model,
		"input": items,
		"store": !p.cfg.NoStore,
	}
	if prev != "" {
		body["previous_response_id"] = prev
	}
	if req.Instructions != "" {
		body["instructions"] = req.Instructions
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, td := range req.Tools {
			tools[i] = map[string]any{
				"type":        "function",
				"name":        realToSafe[td.Name],
				"description": td.Description,
				"parameters":  td.InputSchema,
			}
		}
		body["tools"] = tools
		if tc := responsesToolChoice(req.ToolChoice, realToSafe); tc != nil {
			body["tool_choice"] = tc
		}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if p.cfg.ReasoningEffort != "" || p.cfg.ReasoningSummary != "" {
		r := map[string]any{}
		if p.cfg.ReasoningEffort != "" {
			r["effort"] = p.cfg.ReasoningEffort
		}
		summary := p.cfg.ReasoningSummary
		if summary == "" {
			summary = "auto"
		}
		r["summary"] = summary
		body["reasoning"] = r
	}
	if stream {
		body["stream"] = true
	} else if req.ResponseSchema.Len() > 0 {
		var schema any
		if req.ResponseSchema.Bind(&schema) == nil {
			body["text"] = map[string]any{"format": map[string]any{
				"type":   "json_schema",
				"name":   "response",
				"schema": schema,
			}}
		}
	}
	return body
}

// responsesToolChoice is ToolChoice in the Responses form, which names a
// forced function at the top level rather than under "function".
func responsesToolChoice(tc ToolChoice, realToSafe map[string]string) any {
	switch tc.Mode {
	case "auto", "required", "none":
		return tc.Mode
	case "function":
		return map[string]any{"type": "function", "name": safeToolName(realToSafe, tc.Name)}
	}
	return nil
}

// rsResponse is the response object, as Generate returns it and as
// response.completed, response.incomplete and response.failed carry it.
type rsResponse struct {
	ID                string         `json:"id"`
	Status            string         `json:"status"`
	Output            []rsOutputItem `json:"output"`
	Usage             *rsUsage       `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type rsOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

type rsUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (u *rsUsage) toUsage() *Usage {
	out := &Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
	if u.InputTokensDetails != nil {
		out.CachedInputTokens = u.InputTokensDetails.CachedTokens
	}
	return out
}

// finishReason maps the response status onto the chat-completions
// vocabulary the rest of the SDK already compares against: "tool_calls",
// "stop", or the incomplete reason with max_output_tokens spelled "length".
func (r *rsResponse) finishReason(hasCalls bool) string {
	if r.Status == "incomplete" && r.IncompleteDetails != nil {
		if r.IncompleteDetails.Reason == "max_output_tokens" {
			return "length"
		}
		return r.IncompleteDetails.Reason
	}
	if hasCalls {
		return "tool_calls"
	}
	return "stop"
}

func (r *rsResponse) toProviderResponse(safeToReal map[string]string) *ProviderResponse {
	out := &ProviderResponse{}
	var text, reason strings.Builder
	for _, it := range r.Output {
		switch it.Type {
		case "message":
			for _, c := range it.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				}
			}
		case "reasoning":
			for _, s := range it.Summary {
				reason.WriteString(s.Text)
			}
		case "function_call":
			args := it.Arguments
			if args == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: it.CallID, Name: realToolName(safeToReal, it.Name), Args: toolCallArgsFromJSON(json.RawMessage(args))})
		}
	}
	out.Text, out.Reasoning = text.String(), reason.String()
	out.FinishReason = r.finishReason(len(out.ToolCalls) > 0)
	if r.Usage != nil {
		out.Usage = r.Usage.toUsage()
	}
	return out
}

// rsEvent is one streaming event. Only the fields the provider reads are
// declared.
type rsEvent struct {
	Type        string        `json:"type"`
	OutputIndex int           `json:"output_index"`
	Delta       string        `json:"delta"`
	Item        *rsOutputItem `json:"item"`
	Response    *rsResponse   `json:"response"`
	Code        string        `json:"code"`
	Message     string        `json:"message"`
}

// responsesStream adapts the Responses event stream to the Stream interface.
// Tool calls are numbered densely in the order they start, so Delta.Index
// does not depend on how many message or reasoning items came first.
type responsesStream struct {
	sseStream
	p       *ResponsesProvider
	plan    requestPlan
	nameMap map[string]string
	calls   map[int]int // output_index → Delta.Index
	acc     DeltaAccumulator
}

func (s *responsesStream) decode(payload string) ([]Delta, bool, error) {
	var ev rsEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return nil, false, fmt.Errorf("agent: bad SSE event: %w", err)
	}
	var out []Delta
	switch ev.Type {
	case "response.output_text.delta":
		out = append(out, Delta{Kind: DeltaText, Text: ev.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		out = append(out, Delta{Kind: DeltaReasoning, Text: ev.Delta})
	case "response.output_item.added":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			if s.calls == nil {
				s.calls = map[int]int{}
			}
			idx := len(s.calls)
			s.calls[ev.OutputIndex] = idx
			out = append(out, Delta{Kind: DeltaToolCallStart, Index: idx, ToolCallID: ev.Item.CallID, ToolName: realToolName(s.nameMap, ev.Item.Name), Text: ev.Item.Arguments})
		}
	case "response.function_call_arguments.delta":
		if idx, ok := s.calls[ev.OutputIndex]; ok && ev.Delta != "" {
			out = append(out, Delta{Kind: DeltaToolCallArgs, Index: idx, Text: ev.Delta})
		}
	case "response.completed", "response.incomplete":
		if ev.Response == nil {
			return nil, true, nil
		}
		if ev.Response.Usage != nil {
			out = append(out, Delta{Kind: DeltaUsage, Usage: ev.Response.Usage.toUsage()})
		}
		out = append(out, Delta{Kind: DeltaFinish, FinishReason: ev.Response.finishReason(len(s.calls) > 0)})
		for _, d := range out {
			s.acc.Add(d)
		}
		s.p.remember(s.plan, ev.Response.ID, s.acc.Result())
		return out, true, nil
	case "response.failed":
		msg := "response failed"
		if ev.Response != nil && ev.Response.Error != nil {
			msg = ev.Response.Error.Message
		}
		return nil, true, fmt.Errorf("agent: %s", msg)
	case "error":
		return nil, true, fmt.Errorf("agent: stream error %s: %s", ev.Code, ev.Message)
	}
	for _, d := range out {
		s.acc.Add(d)
	}
	return out, false, nil
}
