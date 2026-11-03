package surfaces

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/ext/lsp"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// TestWorkspaceExtensionsDisabledByDefault pins that an empty Roots produces no
// extensions at all, so a surface can wire this unconditionally and a user who
// passed no workspace flag gets exactly today's behaviour.
func TestWorkspaceExtensionsDisabledByDefault(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	if exts != nil {
		t.Fatalf("expected no extensions for an empty Roots, got %d", len(exts))
	}
}

// TestWorkspaceExtensionsCheckpointFirst pins the checkpoint-first order.
// Nothing else catches a mistake in it.
//
// Extensions apply in registration order and their middleware keeps it, so the
// checkpoint snapshot has to be installed ahead of the tool that writes.
// Reversed, both orders still construct and run. The only symptom is an /undo
// that restores the post-write content. No construction-time error shows that,
// and no tool-level test does either. Asserting the order here is the cheapest
// way to catch a reversal.
func TestWorkspaceExtensionsCheckpointFirst(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	if len(exts) != 3 {
		t.Fatalf("expected checkpoint + files + agentsmd, got %d", len(exts))
	}
	if got := exts[0].Name(); !strings.Contains(got, "checkpoint") {
		t.Errorf("checkpoint must register first so it snapshots before the write; first was %q", got)
	}
	if got := exts[1].Name(); !strings.Contains(got, "file") {
		t.Errorf("expected the files extension second, got %q", got)
	}
	if got := exts[2].Name(); got != "agentsmd" {
		t.Errorf("expected the instruction files last, got %q", got)
	}
}

// goExec is a one-command exec config that constructs on every platform: "go"
// is on PATH wherever these tests run, and Unconfined sidesteps Linux having no
// sandbox backend.
func goExec() *exec.Config {
	return &exec.Config{
		Sandbox:  exec.Unconfined(),
		Commands: []exec.CommandSpec{{Name: "vet", Argv: []string{"go", "vet", "./..."}, Description: "Vet.", ReadOnly: true}},
	}
}

// TestWorkspaceExtensionsExecOrder pins exec between files and lsp. Exec comes
// after checkpoint and the file tools, and the test above guards their order.
// It comes before lsp, which has to be innermost to see writes after they
// happen.
func TestWorkspaceExtensionsExecOrder(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}, Exec: goExec()})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	var names []string
	for _, e := range exts {
		names = append(names, e.Name())
	}
	if len(exts) != 4 || !strings.Contains(names[0], "checkpoint") || !strings.Contains(names[1], "file") || names[2] != "exec" || names[3] != "agentsmd" {
		t.Fatalf("want checkpoint, files, exec, agentsmd; got %v", names)
	}
}

// TestWorkspaceExtensionsExecNeedsRoots pins that asking for exec or language
// servers without a root is an error, not the silent nil an empty config gets.
func TestWorkspaceExtensionsExecNeedsRoots(t *testing.T) {
	if _, err := WorkspaceExtensions(WorkspaceConfig{Exec: goExec()}); err == nil {
		t.Error("exec without roots must be an error")
	}
	if _, err := WorkspaceExtensions(WorkspaceConfig{LanguageServers: []lsp.ServerSpec{{Command: []string{"gopls"}}}}); err == nil {
		t.Error("language servers without roots must be an error")
	}
}

// TestWorkspaceExtensionsNoCheckpoint pins that opting out drops the safety net
// but keeps the file tools.
func TestWorkspaceExtensionsNoCheckpoint(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}, NoCheckpoint: true})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	if len(exts) != 2 || exts[1].Name() != "agentsmd" {
		t.Fatalf("expected the files extension and agentsmd, got %d", len(exts))
	}
	if got := exts[0].Name(); strings.Contains(got, "checkpoint") {
		t.Errorf("NoCheckpoint still registered checkpoint: %q", got)
	}
}

// TestWorkspaceExtensionsReachAnApp is the test for issue 1293. It builds a
// real host.App through host.WithExtension. Then it asserts that every seam
// from the extensions actually arrives.
//
// Before this test, only hand-rolled test doubles inside host covered
// applyExtensions. The five file tools, the /undo command and the diff
// approval renderer were reachable in principle, but nothing registered them.
func TestWorkspaceExtensionsReachAnApp(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{ws}})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}

	cfg := &host.Config{
		Model:        host.ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"},
		Instructions: "test",
	}
	var out bytes.Buffer
	app, err := host.NewApp(cfg, &out, strings.NewReader(""),
		host.WithProvider(stubProvider{}),
		host.WithExtension(exts...),
	)
	if err != nil {
		t.Fatalf("NewApp with workspace extensions: %v", err)
	}

	// Tools: assert through App.Tools, which writes the listing a user sees.
	// No accessor returns tool definitions. Adding one to check a wiring
	// property would widen a surface, and #1240 is trying to narrow it. The
	// printed listing asks the same question at the seam that ships.
	if err := app.Tools(context.Background()); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	listing := out.String()
	for _, want := range []string{"read_file", "edit_file", "write_file", "list_files", "search_files"} {
		if !strings.Contains(listing, want) {
			t.Errorf("tool %q did not reach the app; listing was:\n%s", want, listing)
		}
	}

	// Commands: checkpoint's /undo and /checkpoints reach the registry.
	for _, want := range []string{"undo", "checkpoints"} {
		if _, ok := app.Commands().Lookup(want); !ok {
			t.Errorf("command /%s did not reach the app", want)
		}
	}
}

// stubProvider keeps NewApp from needing a reachable model endpoint.
type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }

func (stubProvider) Generate(context.Context, agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return &agent.ProviderResponse{}, nil
}

func (stubProvider) Stream(context.Context, agent.ProviderRequest) (agent.Stream, error) {
	return emptyStream{}, nil
}

type emptyStream struct{}

func (emptyStream) Recv() (agent.Delta, error) { return agent.Delta{}, io.EOF }
func (emptyStream) Close() error               { return nil }

// TestWorkspaceExtensionsSkipsLSPByDefault pins that language servers are
// opt-in. checkpoint is opt-out because it costs a directory. A language
// server costs a subprocess and an index of the whole tree, so a caller who
// did not ask for one does not get one.
func TestWorkspaceExtensionsSkipsLSPByDefault(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	for _, e := range exts {
		if e.Name() == "lsp" {
			t.Fatal("a language server was started without being configured")
		}
	}
}

// TestWorkspaceExtensionsFailsOnAnUnstartableServer pins that a misconfigured
// server stops construction rather than yielding an agent whose diagnostics are
// permanently and silently empty.
func TestWorkspaceExtensionsFailsOnAnUnstartableServer(t *testing.T) {
	_, err := WorkspaceExtensions(WorkspaceConfig{
		Roots:           []string{t.TempDir()},
		LanguageServers: []lsp.ServerSpec{{Command: []string{"definitely-not-a-language-server-xyz"}, Extensions: []string{".go"}}},
	})
	if err == nil {
		t.Fatal("want an error for a server that cannot start")
	}
	if !strings.Contains(err.Error(), "lsp") {
		t.Fatalf("error should name the failing extension: %v", err)
	}
}

// TestWorkspaceRepoMapNeedsALanguageServer pins that the map is not silently
// enabled without the servers it draws symbols from.
func TestWorkspaceRepoMapNeedsALanguageServer(t *testing.T) {
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}, RepoMap: true})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	for _, e := range exts {
		if e.Name() == "lsp" {
			t.Fatal("RepoMap alone must not start a language server")
		}
	}
}

// TestCheckpointedWritesAreReversible pins that the files extension's writes
// count as reversible exactly when a checkpoint covers them. Under
// reversible-auto with a checkpoint, /undo can restore an edit, so the edit
// runs unasked. With --no-checkpoint the same edit asks, since nothing could
// take it back.
func TestCheckpointedWritesAreReversible(t *testing.T) {
	for _, c := range []struct {
		name         string
		noCheckpoint bool
		wantAsk      bool
	}{
		{"checkpoint on", false, false},
		{"no checkpoint", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}, CheckpointStore: t.TempDir(), NoCheckpoint: c.noCheckpoint})
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
			r, err := agent.NewRunner(agent.RunnerConfig{
				Provider: agent.NewStubProvider(
					agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "write_file", Args: core.NewRawJSON([]byte(`{"path":"a.txt","content":"x"}`))}}},
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
				t.Errorf("write_file under reversible-auto, %s: asked = %v, want %v", c.name, asked, c.wantAsk)
			}
		})
	}
}

// TestEditApprovalBroadcastsTheDiff follows an edit_file approval from the
// files renderer to the ask every surface receives. The browser shows only
// HostEvent.Elicit.Message, so if the diff is not in it, a web user approves
// an edit they cannot see.
func TestEditApprovalBroadcastsTheDiff(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{ws}, CheckpointStore: t.TempDir()})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	edit := `{"path":"hello.txt","expect_hash":"x","edits":[{"old":"hi","new":"bye"}]}`
	cfg := &host.Config{
		Model:        host.ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"},
		Instructions: "test",
		Approval:     &host.ApprovalConfig{Mode: "read-only-auto"},
	}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""),
		host.WithProvider(agent.NewStubProvider(
			agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "edit_file", Args: core.NewRawJSON([]byte(edit))}}},
			agent.StubTurn{Text: "done"},
		)),
		host.WithExtension(exts...),
	)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := app.Subscribe(ctx)
	done := make(chan error, 1)
	go func() { done <- app.RunTurn(ctx, "go") }()

	var msg string
	for msg == "" {
		select {
		case ev := <-events:
			if ev.Kind == host.HostElicitRequest && ev.Elicit != nil {
				msg = ev.Elicit.Message
				// The local responder may already have declined; either way
				// the turn ends.
				_ = app.RespondToAsk(int(ev.AskID), core.ElicitationResult{Action: "decline"}, "web")
			}
		case err := <-done:
			t.Fatalf("turn ended without an approval ask (err %v)", err)
		}
	}
	<-done
	for _, want := range []string{"hello.txt", "- hi", "+ bye"} {
		if !strings.Contains(msg, want) {
			t.Errorf("broadcast ask is missing %q:\n%s", want, msg)
		}
	}
}

// TestUndoRestoresWhenTheProcessRunsElsewhere tests issue 69. make
// dogfood-web runs agentweb from surfaces/web, so the process's working
// directory is not the workspace root. The model names a file relative to the
// root. The checkpoint has to snapshot that file, not one under the process
// cwd. Otherwise /undo restores nothing and still reports success.
func TestUndoRestoresWhenTheProcessRunsElsewhere(t *testing.T) {
	ws := t.TempDir()
	t.Chdir(t.TempDir())

	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{ws}, CheckpointStore: t.TempDir()})
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	cfg := &host.Config{
		Model:        host.ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"},
		Instructions: "test",
		Approval:     &host.ApprovalConfig{Mode: "allow"},
	}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""),
		host.WithProvider(agent.NewStubProvider(
			agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "write_file", Args: core.NewRawJSON([]byte(`{"path":"new.txt","content":"created\n"}`))}}},
			agent.StubTurn{Text: "done"},
		)),
		host.WithExtension(exts...),
	)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	if err := app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	created := filepath.Join(ws, "new.txt")
	if got, err := os.ReadFile(created); err != nil || string(got) != "created\n" {
		t.Fatalf("the write did not land: %q (%v)", got, err)
	}

	res, err := app.Dispatch(context.Background(), "/undo")
	if err != nil {
		t.Fatalf("/undo: %v", err)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("/undo said %q but new.txt is still in the workspace (stat err %v)", res.Message, err)
	}
}

// TestWorkspaceExtensionsOffloadingReachesExec pins the wiring: with
// Offloading set, exec returns whole results and its prompt says how to search
// a stored one.
func TestWorkspaceExtensionsOffloadingReachesExec(t *testing.T) {
	prompt := func(offloading bool) string {
		exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}, Exec: goExec(), Offloading: offloading})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range exts {
			if e.Name() == "exec" {
				return e.(interface{ PromptSections() []host.PromptSection }).PromptSections()[0].Section(context.Background())
			}
		}
		t.Fatal("no exec extension")
		return ""
	}
	if !strings.Contains(prompt(true), "read_tool_result") {
		t.Error("Offloading must reach exec")
	}
	if strings.Contains(prompt(false), "read_tool_result") {
		t.Error("exec must keep its short view without Offloading")
	}
}

// TestWorkspaceInstructionsReachThePrompt covers the agentchat --workspace
// path for #146. An AGENTS.md in the workspace root reaches the model's system
// prompt through WorkspaceExtensions, and NoInstructions keeps it out.
func TestWorkspaceInstructionsReachThePrompt(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("Never edit the tests."), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, off := range []bool{false, true} {
		exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{ws}, NoInstructions: off})
		if err != nil {
			t.Fatal(err)
		}
		stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
		cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"}, Instructions: "test"}
		app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(stub), host.WithExtension(exts...))
		if err != nil {
			t.Fatal(err)
		}
		if err := app.RunTurn(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		app.Close()
		got := strings.Contains(stub.Requests()[0].Instructions, "Never edit the tests.")
		if got == off {
			t.Errorf("NoInstructions=%v: AGENTS.md in the system prompt = %v", off, got)
		}
	}
}

// TestUndoRestoresEveryFileAPatchTouched tests the checkpoint wiring for
// apply_patch. One patch adds, changes, moves and deletes files, and one /undo
// puts every file back the way it was.
func TestUndoRestoresEveryFileAPatchTouched(t *testing.T) {
	ws := t.TempDir()
	for name, body := range map[string]string{"keep.go": "one\n", "move.go": "two\n", "gone.go": "three\n"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{ws}, CheckpointStore: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: keep.go\n-one\n+ONE\n*** Update File: move.go\n*** Move to: sub/moved.go\n-two\n+TWO\n*** Delete File: gone.go\n*** Add File: added.go\n+four\n*** End Patch"
	args, _ := json.Marshal(map[string]string{"input": patch})
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"}, Instructions: "test", Approval: &host.ApprovalConfig{Mode: "allow"}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""),
		host.WithProvider(agent.NewStubProvider(
			agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "apply_patch", Args: core.NewRawJSON(args)}}},
			agent.StubTurn{Text: "done"},
		)),
		host.WithExtension(exts...),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(ws, name))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	if read("keep.go") != "ONE\n" || read("sub/moved.go") != "TWO\n" || read("added.go") != "four\n" || read("gone.go") != "<missing>" {
		t.Fatalf("the patch didn't land: keep=%q moved=%q added=%q gone=%q", read("keep.go"), read("sub/moved.go"), read("added.go"), read("gone.go"))
	}
	if _, err := app.Dispatch(context.Background(), "/undo"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"keep.go": "one\n", "move.go": "two\n", "gone.go": "three\n", "sub/moved.go": "<missing>", "added.go": "<missing>"} {
		if got := read(name); got != want {
			t.Errorf("after /undo %s = %q, want %q", name, got, want)
		}
	}
}

func TestWorkspaceFileEditTools(t *testing.T) {
	var cfg WorkspaceConfig
	if err := cfg.LoadFile(writeWorkspaceFile(t, `{"editTools": ["apply_patch"]}`)); err != nil {
		t.Fatal(err)
	}
	exts, err := WorkspaceExtensions(WorkspaceConfig{Roots: []string{t.TempDir()}, EditTools: cfg.EditTools, NoCheckpoint: true})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := exts[0].Tools()
	defs, _ := src.Tools(context.Background())
	got := map[string]bool{}
	for _, d := range defs {
		got[d.Name] = true
	}
	if got["edit_file"] || !got["apply_patch"] {
		t.Errorf("tools = %v, want apply_patch without edit_file", got)
	}
}
