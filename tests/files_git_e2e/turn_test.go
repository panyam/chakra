package files_git_e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/files"
	"github.com/panyam/chakra/ext/git"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func toolCall(id, name string, args map[string]any) agent.ToolCall {
	raw, _ := json.Marshal(args)
	return agent.ToolCall{ID: id, Name: name, Args: core.NewRawJSON(raw)}
}

// In one turn, ext/files edits one file, creates one and deletes one. The
// turn reports exactly those three changes. The patch holds each change, the
// surface gets the summary, and /diff shows it. Before the turn the user
// staged a change of their own. That change is not in the turn's diff, and
// their index is unchanged. As a control, the next turn writes nothing and
// changes no files.
func TestTheTurnDiffIsWhatExtFilesWrote(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "t@example.com")
	gitIn(t, dir, "config", "user.name", "T")
	for name, body := range map[string]string{"a.txt": "one\ntwo\n", "gone.txt": "bye\n", "staged.txt": "s\n"} {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "initial")
	os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("s\nuser's own\n"), 0o644)
	gitIn(t, dir, "add", "staged.txt")
	indexBefore := gitIn(t, dir, "diff", "--cached")

	fx, err := files.New(files.Config{Roots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	gx, err := git.New(git.Config{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{
			toolCall("1", "edit_file", map[string]any{"path": "a.txt", "expect_hash": files.Hash("one\ntwo\n"), "edits": []any{map[string]any{"old": "two", "new": "TWO"}}}),
			toolCall("2", "write_file", map[string]any{"path": "src/new.go", "content": "package src\n"}),
			toolCall("3", "delete_file", map[string]any{"path": "gone.txt", "expect_hash": files.Hash("bye\n")}),
		}},
		agent.StubTurn{Text: "done"},
		agent.StubTurn{Text: "nothing to do"},
	)
	var out strings.Builder
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://unused", Model: "stub"}}
	app, err := host.NewApp(cfg, &out, strings.NewReader(""), host.WithProvider(stub), host.WithExtension(fx, gx))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "make the changes"); err != nil {
		t.Fatal(err)
	}

	d := gx.LastTurn()
	if d.Files != 3 || d.Added != 2 || d.Deleted != 2 {
		t.Errorf("counted %d files +%d -%d, want 3 +2 -2\n%s", d.Files, d.Added, d.Deleted, d.Stat)
	}
	for _, want := range []string{"-two\n+TWO", "+++ b/src/new.go", "+package src", "--- a/gone.txt", "-bye"} {
		if !strings.Contains(d.Patch, want) {
			t.Errorf("patch lacks %q:\n%s", want, d.Patch)
		}
	}
	if strings.Contains(d.Patch, "user's own") {
		t.Errorf("the user's staged change from before the turn is in the turn's diff")
	}
	if got := gitIn(t, dir, "diff", "--cached"); got != indexBefore {
		t.Errorf("the user's index changed:\n%s", got)
	}
	if !strings.Contains(out.String(), "git: this turn changed 3 files (+2 -2); /diff shows it") {
		t.Errorf("not announced:\n%s", out.String())
	}
	res, err := app.Dispatch(context.Background(), "/diff stat")
	if err != nil || !strings.Contains(res.Message, "src/new.go") || strings.Contains(res.Message, "+package") {
		t.Errorf("/diff stat: %v\n%s", err, res.Message)
	}

	if err := app.RunTurn(context.Background(), "anything else?"); err != nil {
		t.Fatal(err)
	}
	if d := gx.LastTurn(); !d.Empty() || d.Summary() != "this turn changed no files" {
		t.Errorf("control: a turn that wrote nothing reported %+v", d)
	}
}
