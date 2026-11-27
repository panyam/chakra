package agent

import "testing"

// Gemini's OpenAI endpoint moves every system message into its system
// instructions. A turn that ends on a system note, such as a TurnEnd
// hook's refusal, then ends on the model's turn, and Gemini refuses it with
// "Requests ending with a model turn are not supported" (#365). With
// LateSystemAsUser, a system message after the conversation has started
// goes out as a user message. A leading one stays a system message.
func TestLateSystemAsUserSendsANoteAfterTheStartAsAUserMessage(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Text: "context up front"},
		{Role: RoleUser, Text: "What is 2 + 3?"},
		{Role: RoleAssistant, Text: "It's five."},
		{Role: RoleSystem, Text: "Answer with the number alone."},
	}
	roles := func(cfg OpenAIConfig) []string {
		p, err := NewOpenAIProvider(cfg)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range p.buildBody(ProviderRequest{Instructions: "be brief", Messages: msgs}, false)["messages"].([]map[string]any) {
			out = append(out, m["role"].(string))
		}
		return out
	}
	want := []string{"system", "system", "user", "assistant", "user"}
	if got := roles(OpenAIConfig{BaseURL: "http://x", Model: "m", LateSystemAsUser: true}); !equalStrings(got, want) {
		t.Errorf("roles with LateSystemAsUser = %v, want %v", got, want)
	}
	plain := []string{"system", "system", "user", "assistant", "system"}
	if got := roles(OpenAIConfig{BaseURL: "http://x", Model: "m"}); !equalStrings(got, plain) {
		t.Errorf("roles without it = %v, want %v", got, plain)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
