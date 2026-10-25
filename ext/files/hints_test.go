package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// TestApprovalDispositionUnderReadOnlyAuto runs each tool through a real Runner
// and TieredApproval, so it checks what the annotations do rather than what
// they say. Under read-only-auto a read must run unasked and a write must ask.
func TestApprovalDispositionUnderReadOnlyAuto(t *testing.T) {
	cases := []struct {
		tool    string
		args    string
		wantAsk bool
	}{
		{"read_file", `{"path":"a.txt"}`, false},
		{"list_files", `{}`, false},
		{"search_files", `{"query":"hello"}`, false},
		{"write_file", `{"path":"b.txt","content":"x"}`, true},
		{"edit_file", `{"path":"a.txt","expect_hash":"none","edits":[{"old":"hello","new":"bye"}]}`, true},
		// A delete asks, for the same reason: it is unannotated, which reads
		// as destructive. The checkpoint wiring may later mark it reversible,
		// which is reversible-auto's business and not this table's.
		{"delete_file", `{"path":"a.txt","expect_hash":"none"}`, true},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			e, err := New(Config{Roots: []string{root}})
			if err != nil {
				t.Fatal(err)
			}
			src, _ := e.Tools()

			asked := false
			ask := func(context.Context, agent.ToolCallInfo) (bool, error) {
				asked = true
				return false, nil
			}
			stub := agent.NewStubProvider(
				agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: c.tool, Args: core.NewRawJSON([]byte(c.args))}}},
				agent.StubTurn{Text: "done"},
			)
			r, err := agent.NewRunner(agent.RunnerConfig{
				Provider: stub,
				Tools:    src,
				ToolMiddleware: []agent.ToolMiddleware{
					agent.NewTieredApproval(agent.WithDefaultMode(agent.ModeReadOnlyAuto), agent.WithAsk(ask)).WrapToolCall,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Run(context.Background(), []agent.Message{{Role: agent.RoleUser, Text: "go"}}, nil); err != nil {
				t.Fatal(err)
			}
			if asked != c.wantAsk {
				t.Errorf("%s under read-only-auto: asked = %v, want %v", c.tool, asked, c.wantAsk)
			}
		})
	}
}
