// Package anthropic is an agent.Provider over Anthropic's official Go SDK
// (#274). It lives in its own module so that the SDK is a dependency only of
// the code that imports it (CONSTRAINTS A10).
//
// The root module's agent.AnthropicProvider talks to the Messages API with no
// SDK, and so sends no prompt caching (A9). This provider asks the API to
// cache every request. An agent resends the whole conversation on every
// step, and a cached prefix is billed at a tenth of the input price.
//
// The caller builds the SDK client, so auth, retries, timeouts, the base URL
// and the platform (Claude API, Vertex, Bedrock) are the SDK's concern. The
// provider only maps chakra's requests and replies onto the SDK's types.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/ssestream"
	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// defaultMaxTokens is sent when neither Config.MaxTokens nor the request's
// MaxTokens is set. The API requires a cap.
const defaultMaxTokens = 16000

// finalizePrompt follows the model's answer when a structured request ends
// on it.
const finalizePrompt = "Give your answer above as the JSON document the response format describes."

// Config configures a Provider. Only Model is required.
type Config struct {
	// Model is the model id sent on every request, such as
	// "claude-sonnet-5-5".
	Model string

	// MaxTokens caps each reply when ProviderRequest.MaxTokens is zero. Zero
	// means 16000. A reply that hits the cap ends with FinishReason
	// "max_tokens" and no error, and the Runner treats it as the answer. So a
	// long file write wants a high cap.
	MaxTokens int

	// Effort is sent as output_config.effort when set: "low", "medium",
	// "high", "xhigh" or "max". Empty leaves the model's default, which is
	// "medium" on Claude Opus 5.5 and "high" on Claude Sonnet 5.5.
	Effort string
}

// Provider implements agent.Provider over an SDK client. It is safe for
// concurrent use, as the SDK client is.
type Provider struct {
	client *sdk.Client
	cfg    Config
}

// New returns a Provider that sends through client. A nil client or an empty
// Model is an error.
func New(client *sdk.Client, cfg Config) (*Provider, error) {
	if client == nil {
		return nil, errors.New("anthropic: New needs an SDK client")
	}
	if cfg.Model == "" {
		return nil, errors.New("anthropic: Config requires Model")
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	return &Provider{client: client, cfg: cfg}, nil
}

// ProviderCapabilities reports nothing. The Messages API takes no seed, no
// grammar and no sampling penalty, and since API version 2026-10-21 it
// answers 400 to a temperature on every model.
func (p *Provider) ProviderCapabilities() agent.Capabilities { return agent.Capabilities{} }

// check refuses each request field the API would reject or has no place
// for, before anything is sent.
func check(req agent.ProviderRequest) error {
	switch {
	case req.Temperature != nil:
		return fmt.Errorf("%w: Temperature (the Messages API rejects sampling parameters)", agent.ErrUnsupportedParam)
	case req.Seed != nil:
		return fmt.Errorf("%w: Seed (the Messages API has no seed)", agent.ErrUnsupportedParam)
	case req.Guided != nil:
		return fmt.Errorf("%w: Guided (the Messages API has no decoding constraint)", agent.ErrUnsupportedParam)
	case req.Penalties != nil && !(agent.Capabilities{}).CanPenalize(req.Penalties):
		return fmt.Errorf("%w: Penalties (the Messages API has no sampling penalties)", agent.ErrUnsupportedParam)
	}
	return nil
}

// params maps a request onto the SDK's. Every request asks for caching with
// the top-level cache_control, which caches up to the last cacheable block.
// The next step's request starts with the same bytes, so it reads them from
// the cache.
func (p *Provider) params(req agent.ProviderRequest, names agent.ToolNames) (sdk.MessageNewParams, error) {
	maxTokens := p.cfg.MaxTokens
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}
	out := sdk.MessageNewParams{
		Model:        sdk.Model(p.cfg.Model),
		MaxTokens:    int64(maxTokens),
		Messages:     messages(req.Messages, names),
		CacheControl: sdk.NewCacheControlEphemeralParam(),
	}
	if req.Instructions != "" {
		out.System = []sdk.TextBlockParam{{Text: req.Instructions}}
	}
	if p.cfg.Effort != "" {
		out.OutputConfig.Effort = sdk.OutputConfigEffort(p.cfg.Effort)
	}
	for _, td := range req.Tools {
		schema, err := inputSchema(td.InputSchema)
		if err != nil {
			return out, fmt.Errorf("anthropic: tool %q: %w", td.Name, err)
		}
		tool := sdk.ToolParam{Name: names.Wire(td.Name), InputSchema: schema}
		if td.Description != "" {
			tool.Description = sdk.String(td.Description)
		}
		out.Tools = append(out.Tools, sdk.ToolUnionParam{OfTool: &tool})
	}
	if len(req.Tools) > 0 {
		out.ToolChoice = toolChoice(req.ToolChoice, names)
	}
	if req.ResponseSchema.Len() > 0 {
		var schema map[string]any
		if err := req.ResponseSchema.Bind(&schema); err != nil {
			return out, fmt.Errorf("anthropic: ResponseSchema: %w", err)
		}
		out.OutputConfig.Format = sdk.JSONOutputFormatParam{Schema: supportedSchema(schema).(map[string]any)}
		// The Runner's structured finalize sends the history ending on the
		// model's free answer. Current models take no prefill and refuse a
		// conversation that ends on the assistant, so ask for that answer
		// in a user turn.
		if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == agent.RoleAssistant {
			out.Messages = append(out.Messages, sdk.NewUserMessage(sdk.NewTextBlock(finalizePrompt)))
		}
	}
	return out, nil
}

// messages maps the conversation the way agent.AnthropicProvider does, so
// switching providers doesn't change what the model reads. A tool result is
// a user turn, and a RoleSystem note is a user text turn, since chakra's
// notes go anywhere in the history.
func messages(in []agent.Message, names agent.ToolNames) []sdk.MessageParam {
	out := make([]sdk.MessageParam, 0, len(in))
	for _, m := range in {
		switch m.Role {
		case agent.RoleAssistant:
			var blocks []sdk.ContentBlockParamUnion
			if m.Text != "" {
				blocks = append(blocks, sdk.NewTextBlock(m.Text))
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, sdk.NewToolUseBlock(tc.ID, toolInput(tc.Args), names.Wire(tc.Name)))
			}
			out = append(out, sdk.NewAssistantMessage(blocks...))
		case agent.RoleTool:
			out = append(out, sdk.NewUserMessage(sdk.NewToolResultBlock(m.ToolCallID, m.Text, false)))
		default:
			out = append(out, sdk.NewUserMessage(sdk.NewTextBlock(m.Text)))
		}
	}
	return out
}

// unsupportedKeywords are the JSON Schema keywords structured outputs answer
// 400 to: numeric bounds, string lengths and array size limits.
var unsupportedKeywords = []string{
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minLength", "maxLength", "minItems", "maxItems", "uniqueItems",
}

// supportedSchema returns the part of a schema that structured outputs take.
// It drops unsupportedKeywords and sets additionalProperties to false on
// every object, which the API requires. The reply can still break a dropped
// rule. The Runner's structured finalize checks the reply against the full
// schema and asks again, so the rule still holds for what the turn returns.
func supportedSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if slices.Contains(unsupportedKeywords, k) {
				continue
			}
			switch k {
			case "properties", "$defs", "definitions":
				if m, ok := e.(map[string]any); ok {
					sub := make(map[string]any, len(m))
					for name, s := range m {
						sub[name] = supportedSchema(s)
					}
					out[k] = sub
					continue
				}
			}
			out[k] = supportedSchema(e)
		}
		if out["type"] == "object" || out["properties"] != nil {
			out["additionalProperties"] = false
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = supportedSchema(e)
		}
		return out
	}
	return v
}

// toolInput decodes a tool call's arguments into the object tool_use.input
// takes. An empty or unparseable value becomes an empty object.
func toolInput(args core.RawJSON) any {
	var v map[string]any
	if args.Len() == 0 || args.Bind(&v) != nil || v == nil {
		return map[string]any{}
	}
	return v
}

// inputSchema carries a tool's JSON Schema across whole. properties and
// required have fields of their own, and every other keyword rides in
// ExtraFields.
func inputSchema(schema any) (sdk.ToolInputSchemaParam, error) {
	var m map[string]any
	raw, err := json.Marshal(schema)
	if err != nil {
		return sdk.ToolInputSchemaParam{}, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return sdk.ToolInputSchemaParam{}, err
	}
	out := sdk.ToolInputSchemaParam{ExtraFields: map[string]any{}}
	for k, v := range m {
		switch k {
		case "type":
		case "properties":
			out.Properties = v
		case "required":
			if list, ok := v.([]any); ok {
				for _, r := range list {
					if s, ok := r.(string); ok {
						out.Required = append(out.Required, s)
					}
				}
			}
		default:
			out.ExtraFields[k] = v
		}
	}
	return out, nil
}

// toolChoice maps chakra's choice. Auto is the API's default and goes out as
// nothing. Current models answer 400 to a forced choice ("required" or a
// named tool), and that error comes back as the provider's.
func toolChoice(tc agent.ToolChoice, names agent.ToolNames) sdk.ToolChoiceUnionParam {
	switch tc.Mode {
	case "auto":
		return sdk.ToolChoiceUnionParam{OfAuto: &sdk.ToolChoiceAutoParam{}}
	case "required":
		return sdk.ToolChoiceUnionParam{OfAny: &sdk.ToolChoiceAnyParam{}}
	case "none":
		return sdk.ToolChoiceUnionParam{OfNone: &sdk.ToolChoiceNoneParam{}}
	case "function":
		return sdk.ToolChoiceParamOfTool(names.Wire(tc.Name))
	}
	return sdk.ToolChoiceUnionParam{}
}

// usage counts the whole prompt as InputTokens, cache reads and writes
// included, as agent.AnthropicProvider does. CachedInputTokens is the part
// read from the cache.
func usage(input, cacheRead, cacheWrite, output int64) *agent.Usage {
	return &agent.Usage{
		InputTokens:       int(input + cacheRead + cacheWrite),
		OutputTokens:      int(output),
		CachedInputTokens: int(cacheRead),
	}
}

// Generate implements agent.Provider. With a ResponseSchema, the request asks
// for structured output, and Text holds the JSON document. It streams under
// the hood, so a high MaxTokens works here too.
func (p *Provider) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	if err := check(req); err != nil {
		return nil, err
	}
	names := agent.NewToolNames(req.Tools)
	params, err := p.params(req, names)
	if err != nil {
		return nil, err
	}
	// The SDK refuses a non-streaming call whose max_tokens could run past
	// ten minutes, so Generate streams and folds the events into a message.
	events := p.client.Messages.NewStreaming(ctx, params)
	defer events.Close()
	var msg sdk.Message
	for events.Next() {
		if err := msg.Accumulate(events.Current()); err != nil {
			return nil, err
		}
	}
	if err := events.Err(); err != nil {
		return nil, err
	}
	out := &agent.ProviderResponse{
		FinishReason: string(msg.StopReason),
		Usage:        usage(msg.Usage.InputTokens, msg.Usage.CacheReadInputTokens, msg.Usage.CacheCreationInputTokens, msg.Usage.OutputTokens),
	}
	var text, reason strings.Builder
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case sdk.TextBlock:
			text.WriteString(b.Text)
		case sdk.ThinkingBlock:
			reason.WriteString(b.Thinking)
		case sdk.ToolUseBlock:
			out.ToolCalls = append(out.ToolCalls, agent.ToolCall{ID: b.ID, Name: names.Real(b.Name), Args: core.NewRawJSON(b.Input)})
		}
	}
	out.Text, out.Reasoning = text.String(), reason.String()
	return out, nil
}

// Stream implements agent.Provider. The SDK's events map onto chakra's
// deltas: thinking and text deltas, a tool_use block's start and its argument
// fragments, then the stop reason and the usage. Recv returns io.EOF after
// message_stop.
func (p *Provider) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	if err := check(req); err != nil {
		return nil, err
	}
	names := agent.NewToolNames(req.Tools)
	params, err := p.params(req, names)
	if err != nil {
		return nil, err
	}
	return &stream{events: p.client.Messages.NewStreaming(ctx, params), names: names}, nil
}

type stream struct {
	events  *ssestream.Stream[sdk.MessageStreamEventUnion]
	names   agent.ToolNames
	pending []agent.Delta
	done    bool
	// The prompt's counts arrive on message_start, and the output's on
	// message_delta, so the usage delta waits for both.
	input, cacheRead, cacheWrite int64
}

func (s *stream) Recv() (agent.Delta, error) {
	for len(s.pending) == 0 {
		if s.done {
			return agent.Delta{}, io.EOF
		}
		if !s.events.Next() {
			if err := s.events.Err(); err != nil {
				return agent.Delta{}, err
			}
			return agent.Delta{}, io.EOF
		}
		s.pending = s.deltas(s.events.Current())
	}
	d := s.pending[0]
	s.pending = s.pending[1:]
	return d, nil
}

func (s *stream) deltas(ev sdk.MessageStreamEventUnion) []agent.Delta {
	switch e := ev.AsAny().(type) {
	case sdk.MessageStartEvent:
		u := e.Message.Usage
		s.input, s.cacheRead, s.cacheWrite = u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens
	case sdk.ContentBlockStartEvent:
		if tu, ok := e.ContentBlock.AsAny().(sdk.ToolUseBlock); ok {
			return []agent.Delta{{Kind: agent.DeltaToolCallStart, Index: int(e.Index), ToolCallID: tu.ID, ToolName: s.names.Real(tu.Name)}}
		}
	case sdk.ContentBlockDeltaEvent:
		switch d := e.Delta.AsAny().(type) {
		case sdk.TextDelta:
			if d.Text != "" {
				return []agent.Delta{{Kind: agent.DeltaText, Text: d.Text}}
			}
		case sdk.InputJSONDelta:
			if d.PartialJSON != "" {
				return []agent.Delta{{Kind: agent.DeltaToolCallArgs, Index: int(e.Index), Text: d.PartialJSON}}
			}
		case sdk.ThinkingDelta:
			if d.Thinking != "" {
				return []agent.Delta{{Kind: agent.DeltaReasoning, Text: d.Thinking}}
			}
		}
	case sdk.MessageDeltaEvent:
		var out []agent.Delta
		if e.Delta.StopReason != "" {
			out = append(out, agent.Delta{Kind: agent.DeltaFinish, FinishReason: string(e.Delta.StopReason)})
		}
		return append(out, agent.Delta{Kind: agent.DeltaUsage, Usage: usage(s.input, s.cacheRead, s.cacheWrite, e.Usage.OutputTokens)})
	case sdk.MessageStopEvent:
		s.done = true
	}
	return nil
}

func (s *stream) Close() error { return s.events.Close() }
