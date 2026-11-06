package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/chakra"
)

// A key set on the connection reaches the endpoint and wins over APIKeyEnv,
// and it never appears when the connection is marshalled into a config.
func TestDefaultProviderBuilder_APIKeyInHand(t *testing.T) {
	t.Setenv("SOME_KEY_ENV", "from-env")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		http.Error(w, "no", http.StatusTeapot)
	}))
	defer srv.Close()

	conn := ConnectionConfig{BaseURL: srv.URL, Model: "m", APIKey: "in-hand", APIKeyEnv: "SOME_KEY_ENV"}
	p, err := DefaultProviderBuilder(conn)
	if err != nil {
		t.Fatalf("DefaultProviderBuilder: %v", err)
	}
	_, _ = p.Generate(context.Background(), agent.ProviderRequest{Messages: []agent.Message{{Role: agent.RoleUser, Text: "hi"}}})
	if got != "Bearer in-hand" {
		t.Fatalf("Authorization = %q, want the in-hand key", got)
	}

	b, err := json.Marshal(conn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "in-hand") {
		t.Fatalf("marshalled connection carries the key: %s", b)
	}
}

// An anthropic connection's maxTokens reaches the request as max_tokens, and
// a connection without it keeps the provider's default (#264).
func TestDefaultProviderBuilder_AnthropicMaxTokens(t *testing.T) {
	var got []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body.MaxTokens)
		http.Error(w, "no", http.StatusTeapot)
	}))
	defer srv.Close()

	var conn ConnectionConfig
	if err := json.Unmarshal([]byte(`{"type":"anthropic","baseUrl":"`+srv.URL+`","model":"m","maxTokens":64000}`), &conn); err != nil {
		t.Fatal(err)
	}
	for _, c := range []ConnectionConfig{conn, {Type: "anthropic", BaseURL: srv.URL, Model: "m"}} {
		p, err := DefaultProviderBuilder(c)
		if err != nil {
			t.Fatalf("DefaultProviderBuilder: %v", err)
		}
		_, _ = p.Generate(context.Background(), agent.ProviderRequest{Messages: []agent.Message{{Role: agent.RoleUser, Text: "hi"}}})
	}
	if len(got) != 2 || got[0] != 64000 || got[1] != 4096 {
		t.Fatalf("max_tokens sent = %v, want [64000 4096]", got)
	}
}
