package surfaces

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/chakra/provider/anthropic"
)

// agentchat and agentweb build an "anthropic" connection through Anthropic's
// SDK, so every request asks for prompt caching. Other types keep the host's
// providers.
func TestAnthropicConnectionsUseTheSDK(t *testing.T) {
	var body map[string]any
	var key string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("X-Api-Key")
		if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			key = b
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer ts.Close()

	p, err := ConnectionTypes().Build(host.ConnectionConfig{Type: "anthropic", BaseURL: ts.URL, Model: "claude-sonnet-5-5", APIKey: "k1", MaxTokens: 64000, ReasoningEffort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*anthropic.Provider); !ok {
		t.Fatalf("anthropic built %T, want the SDK provider", p)
	}
	if _, err := p.Generate(context.Background(), agent.ProviderRequest{Messages: []agent.Message{{Role: agent.RoleUser, Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	oc, _ := body["output_config"].(map[string]any)
	if key != "k1" || body["cache_control"] == nil || body["max_tokens"] != 64000.0 || oc["effort"] != "medium" {
		t.Errorf("key %q, body %v", key, body)
	}

	p, err = ConnectionTypes().Build(host.ConnectionConfig{Type: "ollama", Model: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*agent.OpenAIProvider); !ok {
		t.Errorf("ollama built %T", p)
	}
}
