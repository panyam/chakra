package host

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// SamplingConfig lets connected MCP servers ask the session's model for a
// completion (sampling/createMessage). Off unless Enabled: a server that can
// sample spends the user's model budget on prompts the server wrote, so the
// host only advertises the capability when the operator asks for it.
type SamplingConfig struct {
	// Enabled advertises the sampling capability to every server and answers
	// their requests.
	Enabled bool `json:"enabled"`

	// Connection names the model connection that answers. Empty uses the
	// session's chat provider (the active connection). A server's model
	// preferences are hints only and never pick the connection.
	Connection string `json:"connection,omitempty"`

	// MaxTokens caps a reply, whatever the request asks for. Zero leaves the
	// request's own maxTokens in charge.
	MaxTokens int `json:"maxTokens,omitempty"`

	// ReviewResponse shows the model's reply to the user before it goes back
	// to the server, so nothing the model wrote reaches a server unseen.
	ReviewResponse bool `json:"reviewResponse,omitempty"`
}

// samplingState is the session's running total of what sampling has cost.
// It isn't charged to a turn's tree budget: a request arrives on a server's
// connection, not inside a turn the host can attribute it to.
type samplingState struct {
	mu       sync.Mutex
	requests int
	usage    agent.Usage
}

func (s *samplingState) add(u *agent.Usage) (int, agent.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if u != nil {
		s.usage.InputTokens += u.InputTokens
		s.usage.OutputTokens += u.OutputTokens
		s.usage.CachedInputTokens += u.CachedInputTokens
	}
	return s.requests, s.usage
}

// samplingHandler answers one server's sampling requests.
func (a *App) samplingHandler(serverID string) client.SamplingHandler {
	return func(ctx context.Context, req core.CreateMessageRequest) (core.CreateMessageResult, error) {
		return a.sample(ctx, serverID, req)
	}
}

// sample asks the user (unless the approval mode runs everything), calls the
// configured model, lets the user review the reply if configured, and returns
// it to the server. A refusal at either step is an error the server sees.
func (a *App) sample(ctx context.Context, serverID string, req core.CreateMessageRequest) (core.CreateMessageResult, error) {
	cfg := a.cfg.Sampling
	if cfg == nil || !cfg.Enabled {
		return core.CreateMessageResult{}, fmt.Errorf("host: sampling is not enabled")
	}
	msgs := make([]agent.Message, 0, len(req.Messages))
	for i, m := range req.Messages {
		if m.Content.Type != "text" {
			// Images and audio would need the provider's capabilities checked
			// and the user shown something they can judge; refuse clearly
			// rather than drop part of what the server asked about.
			return core.CreateMessageResult{}, fmt.Errorf("host: sampling message %d is %s content; only text is supported", i+1, m.Content.Type)
		}
		switch m.Role {
		case "user":
			msgs = append(msgs, agent.Message{Role: agent.RoleUser, Text: m.Content.Text})
		case "assistant":
			msgs = append(msgs, agent.Message{Role: agent.RoleAssistant, Text: m.Content.Text})
		default:
			return core.CreateMessageResult{}, fmt.Errorf("host: sampling message %d has role %q, want user or assistant", i+1, m.Role)
		}
	}
	maxTokens := req.MaxTokens
	if cfg.MaxTokens > 0 && (maxTokens <= 0 || maxTokens > cfg.MaxTokens) {
		maxTokens = cfg.MaxTokens
	}

	if !a.samplingRunsUnasked() {
		ok, err := a.confirm(ctx, renderSamplingAsk(serverID, req, maxTokens))
		if err != nil {
			return core.CreateMessageResult{}, err
		}
		if !ok {
			return core.CreateMessageResult{}, fmt.Errorf("the user declined the sampling request")
		}
	}

	provider, model, err := a.samplingProvider()
	if err != nil {
		return core.CreateMessageResult{}, err
	}
	resp, err := provider.Generate(ctx, agent.ProviderRequest{
		Instructions: req.SystemPrompt,
		Messages:     msgs,
		MaxTokens:    maxTokens,
		Temperature:  req.Temperature,
	})
	if err != nil {
		return core.CreateMessageResult{}, fmt.Errorf("host: sampling: %w", err)
	}

	if cfg.ReviewResponse {
		ok, err := a.confirm(ctx, fmt.Sprintf("Send this reply to MCP server %q?\n\n%s", serverID, indentBlock(truncateForAsk(resp.Text, 4000))))
		if err != nil {
			return core.CreateMessageResult{}, err
		}
		if !ok {
			return core.CreateMessageResult{}, fmt.Errorf("the user declined to send the sampled reply")
		}
	}

	n, total := a.sampling.add(resp.Usage)
	in, out := 0, 0
	if resp.Usage != nil {
		in, out = resp.Usage.InputTokens, resp.Usage.OutputTokens
	}
	a.emit(HostEvent{Kind: HostMessage, Message: fmt.Sprintf(
		"MCP server %s sampled the model: %d input, %d output tokens (this session: %d requests, %d input, %d output)",
		serverID, in, out, n, total.InputTokens, total.OutputTokens)})

	return core.CreateMessageResult{
		Model:      model,
		StopReason: samplingStopReason(resp.FinishReason),
		Role:       "assistant",
		Content:    core.Content{Type: "text", Text: resp.Text},
	}, nil
}

// samplingRunsUnasked reports whether the approval mode runs everything
// (allow), the one mode in which a sampling request isn't put to the user.
func (a *App) samplingRunsUnasked() bool {
	return a.approval != nil && a.approval.DefaultMode() == agent.ModeAlwaysAllow
}

// samplingProvider is the configured connection, or the session's chat
// provider, and the model name to report.
func (a *App) samplingProvider() (agent.Provider, string, error) {
	if name := a.cfg.Sampling.Connection; name != "" {
		if a.connections == nil {
			return nil, "", fmt.Errorf("host: sampling connection %q: no connections are configured", name)
		}
		p, err := a.connections.Provider(name)
		if err != nil {
			return nil, "", fmt.Errorf("host: sampling connection %q: %w", name, err)
		}
		return p, a.connections.ModelOf(name), nil
	}
	if a.connections != nil {
		return a.chatProvider, a.connections.ActiveModel(), nil
	}
	return a.chatProvider, a.cfg.Model.Model, nil
}

// renderSamplingAsk words the question put to the user: who is asking, with
// what instructions and messages, for how many tokens.
func renderSamplingAsk(serverID string, req core.CreateMessageRequest, maxTokens int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "MCP server %q asks to use your model", serverID)
	if maxTokens > 0 {
		fmt.Fprintf(&b, " (up to %d output tokens)", maxTokens)
	}
	b.WriteString(".\n")
	if req.SystemPrompt != "" {
		fmt.Fprintf(&b, "\nSystem prompt:\n%s\n", indentBlock(truncateForAsk(req.SystemPrompt, 1000)))
	}
	for _, m := range req.Messages {
		fmt.Fprintf(&b, "\n%s:\n%s\n", m.Role, indentBlock(truncateForAsk(m.Content.Text, 1500)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func truncateForAsk(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n… %d more character(s)", len(s)-n)
}

func indentBlock(s string) string { return "  " + strings.ReplaceAll(s, "\n", "\n  ") }

// samplingStopReason maps a provider's finish reason onto MCP's names.
func samplingStopReason(r string) string {
	switch r {
	case "stop", "end_turn", "":
		return "endTurn"
	case "length", "max_tokens":
		return "maxTokens"
	case "stop_sequence":
		return "stopSequence"
	}
	return r
}
