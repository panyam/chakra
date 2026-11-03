package agent

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// A grant answers an ask the gate would otherwise raise, and nothing else: a
// forbidden verdict, an always-ask verdict and a RuleDeny still win (#187).
func TestGrantSkipsTheAskButNotARefusal(t *testing.T) {
	granted := func(_ context.Context, info ToolCallInfo) bool { return info.Call.Name != "other" }
	classify := func(_ context.Context, info ToolCallInfo) (CallClass, bool) {
		switch string(info.Call.Args.Raw()) {
		case `"forbidden"`:
			return CallClass{Disposition: DispositionForbidden}, true
		case `"escalate"`:
			return CallClass{Disposition: DispositionAlwaysAsk}, true
		case `"destructive"`:
			return CallClass{Disposition: DispositionDestructive}, true
		}
		return CallClass{}, false
	}
	cases := []struct {
		name, tool, args string
		rules            []TieredOption
		wantRan, wantAsk bool
	}{
		{"granted call runs unasked", "shell", `{}`, nil, true, false},
		{"granted destructive call runs unasked", "shell", `"destructive"`, nil, true, false},
		{"granted beats a RuleAsk", "shell", `{}`, []TieredOption{WithToolRule("shell", RuleAsk)}, true, false},
		{"ungranted call is still asked", "other", `{}`, nil, false, true},
		{"forbidden beats a grant", "shell", `"forbidden"`, nil, false, false},
		{"always-ask beats a grant", "shell", `"escalate"`, nil, false, true},
		{"RuleDeny beats a grant", "shell", `{}`, []TieredOption{WithToolRule("shell", RuleDeny)}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asked := false
			ask := func(context.Context, ToolCallInfo) (bool, error) { asked = true; return false, nil }
			opts := append([]TieredOption{WithAsk(ask), WithClassifier(classify), WithGrants(granted)}, c.rules...)
			gate := NewTieredApproval(opts...)
			ran := false
			info := ToolCallInfo{Call: ToolCall{Name: c.tool, Args: core.NewRawJSON([]byte(c.args))}}
			_, _ = gate.WrapToolCall(context.Background(), info, func(context.Context, ToolCallInfo) (*core.ToolResult, error) {
				ran = true
				return &core.ToolResult{}, nil
			})
			if ran != c.wantRan || asked != c.wantAsk {
				t.Errorf("ran=%v asked=%v, want ran=%v asked=%v", ran, asked, c.wantRan, c.wantAsk)
			}
		})
	}
}
