package surfaces

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/files"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// TestUndoBringsBackADeletedFile is issue 108 end to end: a real host runs a
// turn in which the model calls delete_file, and /undo restores the content.
//
// The unit tests either side of this one prove the tool deletes and the
// checkpoint restores; only the wiring decides whether the two are ever
// introduced. A delete_file missing from fileWrites deletes the file and says
// so in the gap report, which is honest but is not a safety net.
func TestUndoBringsBackADeletedFile(t *testing.T) {
	ws := t.TempDir()
	const original = "the content that comes back\n"
	if err := os.WriteFile(filepath.Join(ws, "doomed.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{ws}, CheckpointStore: t.TempDir()})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	cfg := &host.Config{
		Model:        host.ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"},
		Instructions: "test",
		Approval:     &host.ApprovalConfig{Mode: "allow"},
	}
	hash := files.Hash(original)
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""),
		host.WithProvider(agent.NewStubProvider(
			agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "delete_file", Args: core.NewRawJSON([]byte(`{"path":"doomed.txt","expect_hash":"` + hash + `"}`))}}},
			agent.StubTurn{Text: "done"},
		)),
		host.WithExtension(exts...),
	)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	if err := app.RunTurn(context.Background(), "delete it"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	doomed := filepath.Join(ws, "doomed.txt")
	if _, err := os.Stat(doomed); !os.IsNotExist(err) {
		t.Fatalf("the delete did not reach the file (stat err %v)", err)
	}

	res, err := app.Dispatch(context.Background(), "/undo")
	if err != nil {
		t.Fatalf("/undo: %v", err)
	}
	got, err := os.ReadFile(doomed)
	if err != nil {
		t.Fatalf("/undo said %q but the file is still gone: %v", res.Message, err)
	}
	if string(got) != original {
		t.Fatalf("/undo restored %q, want %q", got, original)
	}
}

// TestDeleteIsReversibleUnderReversibleAuto pins the annotation half: with a
// checkpoint wired, delete_file declares destructiveHint false, so
// reversible-auto runs it unasked. Without one it stays destructive and asks.
func TestDeleteIsReversibleUnderReversibleAuto(t *testing.T) {
	for _, c := range []struct {
		name         string
		noCheckpoint bool
		wantAsk      bool
	}{
		{"checkpoint on", false, false},
		{"no checkpoint", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws := t.TempDir()
			if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{ws}, CheckpointStore: t.TempDir(), NoCheckpoint: c.noCheckpoint})
			if err != nil {
				t.Fatalf("WorkspaceExtensions: %v", err)
			}
			var src agent.ToolSource
			for _, e := range exts {
				if strings.Contains(e.Name(), "file") {
					if src, err = e.Tools(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if src == nil {
				t.Fatal("no files extension")
			}

			asked := false
			ask := func(context.Context, agent.ToolCallInfo) (bool, error) {
				asked = true
				return false, nil
			}
			args := `{"path":"a.txt","expect_hash":"` + files.Hash("x\n") + `"}`
			r, err := agent.NewRunner(agent.RunnerConfig{
				Provider: agent.NewStubProvider(
					agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "delete_file", Args: core.NewRawJSON([]byte(args))}}},
					agent.StubTurn{Text: "done"},
				),
				Tools: src,
				ToolMiddleware: []agent.ToolMiddleware{
					agent.NewTieredApproval(agent.WithDefaultMode(agent.ModeReversibleAuto), agent.WithAsk(ask)).WrapToolCall,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Run(context.Background(), []agent.Message{{Role: agent.RoleUser, Text: "go"}}, nil); err != nil {
				t.Fatal(err)
			}
			if asked != c.wantAsk {
				t.Errorf("delete_file under reversible-auto, %s: asked = %v, want %v", c.name, asked, c.wantAsk)
			}
		})
	}
}
