package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// newTestExtension builds a renderer over an empty workspace root, for the
// cases where what is on disk does not matter.
func newTestExtension(t *testing.T) *Extension {
	t.Helper()
	e, err := New(Config{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func renderCall(t *testing.T, name, args string) (string, bool) {
	t.Helper()
	return newTestExtension(t).renderEditApproval(context.Background(), agent.ToolCallInfo{
		Call: agent.ToolCall{Name: name, Args: core.NewRawJSON([]byte(args))},
	})
}

// TestEditApprovalShowsTheDiff is the reason the seam exists. The default host
// prompt would render this call's JSON trimmed to 200 characters, which cuts
// off the change the user is being asked to approve.
func TestEditApprovalShowsTheDiff(t *testing.T) {
	got, ok := renderCall(t, "edit_file", `{
		"path": "main.go",
		"expect_hash": "abc123",
		"edits": [{"old": "func Old() {}", "new": "func New() {}"}]
	}`)
	if !ok {
		t.Fatalf("edit_file should be claimed, got %q", got)
	}
	for _, want := range []string{"main.go", "1 change", "- func Old() {}", "+ func New() {}"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "expect_hash") {
		t.Errorf("the hash is bookkeeping, not something to review:\n%s", got)
	}
}

func TestEditApprovalCountsEveryHunk(t *testing.T) {
	got, ok := renderCall(t, "edit_file", `{
		"path": "main.go",
		"edits": [
			{"old": "one", "new": "ONE"},
			{"old": "two", "new": "TWO"}
		]
	}`)
	if !ok {
		t.Fatal("edit_file should be claimed")
	}
	if !strings.Contains(got, "2 change(s)") {
		t.Errorf("prompt should count the hunks:\n%s", got)
	}
	for _, want := range []string{"- one", "+ ONE", "- two", "+ TWO"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
}

// TestDeletionRendersWithNoAddedLines keeps an empty `new` from printing a
// bare "+" that reads like an added blank line rather than a deletion.
func TestDeletionRendersWithNoAddedLines(t *testing.T) {
	got, ok := renderCall(t, "edit_file", `{"path":"a.go","edits":[{"old":"gone","new":""}]}`)
	if !ok {
		t.Fatal("edit_file should be claimed")
	}
	if !strings.Contains(got, "- gone") {
		t.Errorf("prompt should show the removed line:\n%s", got)
	}
	if strings.Contains(got, "+") {
		t.Errorf("a deletion should add no lines:\n%s", got)
	}
}

func TestWriteApprovalDistinguishesCreateFromReplace(t *testing.T) {
	create, ok := renderCall(t, "write_file", `{"path":"new.go","content":"package main\n"}`)
	if !ok {
		t.Fatal("write_file should be claimed")
	}
	if !strings.Contains(create, "Create new.go") {
		t.Errorf("a create should say so:\n%s", create)
	}

	replace, ok := renderCall(t, "write_file", `{"path":"old.go","content":"package main\n","expect_hash":"abc"}`)
	if !ok {
		t.Fatal("write_file should be claimed")
	}
	if !strings.Contains(replace, "Replace old.go") {
		t.Errorf("a replace should say so, since it destroys content:\n%s", replace)
	}
}

// TestWriteApprovalSaysAnExistingFileWillBeRefused is the prompt telling the
// truth about a call that cannot run. write_file without expect_hash is a
// create, and a create on an existing path is refused, so a "Create old.go?"
// prompt asks the user to approve a write the tool will never perform. The
// prompt names the reason and the two ways forward instead.
func TestWriteApprovalSaysAnExistingFileWillBeRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	render := func(args string) string {
		t.Helper()
		got, ok := e.renderEditApproval(context.Background(), agent.ToolCallInfo{
			Call: agent.ToolCall{Name: "write_file", Args: core.NewRawJSON([]byte(args))},
		})
		if !ok {
			t.Fatal("write_file should be claimed")
		}
		return got
	}

	got := render(`{"path":"old.go","content":"package main\n\nfunc main() {}\n"}`)
	if strings.Contains(got, "Create") {
		t.Errorf("an existing file is not a create, even without expect_hash:\n%s", got)
	}
	for _, want := range []string{"old.go", "already exists", "refused", "expect_hash"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "  func main() {}") {
		t.Errorf("prompt should still show the content being written:\n%s", got)
	}

	// The same path with expect_hash is a replace, and is not described as one
	// that will be refused: it runs if the hash still matches.
	replace := render(`{"path":"old.go","content":"x\n","expect_hash":"abc"}`)
	if !strings.Contains(replace, "Replace old.go") {
		t.Errorf("a replace should say so:\n%s", replace)
	}
	if strings.Contains(replace, "refused") {
		t.Errorf("a replace is not refused up front:\n%s", replace)
	}
}

// TestWriteApprovalOnlyCallsAnExistingFileByItsName keeps a directory from
// being reported as an existing file: write_file refuses that with its own
// message, and claiming "pass expect_hash" about a directory would be advice
// that cannot work.
func TestWriteApprovalOnlyCallsAnExistingFileByItsName(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "somedir"), 0o755); err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := e.renderEditApproval(context.Background(), agent.ToolCallInfo{
		Call: agent.ToolCall{Name: "write_file", Args: core.NewRawJSON([]byte(`{"path":"somedir","content":"x\n"}`))},
	})
	if !ok {
		t.Fatal("write_file should be claimed")
	}
	if !strings.Contains(got, "Create somedir") {
		t.Errorf("a directory is not a file that exists for this purpose:\n%s", got)
	}
}

// TestWriteApprovalCapsItsOwnPreview is the renderer exercising the freedom
// the seam gives it. The default's 200-character trim is wrong for a diff, but
// unbounded output is wrong for a whole-file write, so the renderer picks.
func TestWriteApprovalCapsItsOwnPreview(t *testing.T) {
	var body strings.Builder
	for i := range 200 {
		body.WriteString("line ")
		body.WriteByte(byte('0' + i%10))
		body.WriteString("\n")
	}
	got, ok := renderCall(t, "write_file", `{"path":"big.go","content":`+jsonQuote(body.String())+`}`)
	if !ok {
		t.Fatal("write_file should be claimed")
	}
	if !strings.Contains(got, "more line(s)") {
		t.Errorf("a long write should be capped and say so:\n%s", got[:min(400, len(got))])
	}
	if n := strings.Count(got, "\n"); n > 60 {
		t.Errorf("preview is %d lines, want it bounded", n)
	}
}

// TestUnknownToolsAreDeclined keeps the default covering everything this
// package does not own. Claiming broadly would silently replace the prompt for
// tools whose arguments this renderer knows nothing about.
func TestUnknownToolsAreDeclined(t *testing.T) {
	for _, name := range []string{"read_file", "list_files", "search_files", "some_other_tool"} {
		if got, ok := renderCall(t, name, `{"path":"a.go"}`); ok {
			t.Errorf("%s should not be claimed, got %q", name, got)
		}
	}
}

// TestDeleteApprovalNamesTheFile is the call that destroys content. The
// default rendering trims the JSON, and for a delete that is the whole
// question: which file is about to go.
func TestDeleteApprovalNamesTheFile(t *testing.T) {
	got, ok := renderCall(t, "delete_file", `{"path":"old.go","expect_hash":"abc"}`)
	if !ok {
		t.Fatalf("delete_file should be claimed, got %q", got)
	}
	if !strings.Contains(got, "Delete old.go") {
		t.Errorf("prompt should name the file being deleted:\n%s", got)
	}
	if strings.Contains(got, "expect_hash") {
		t.Errorf("the hash is bookkeeping, not something to review:\n%s", got)
	}
}

// TestMalformedArgumentsDecline is where the generic rendering is the honest
// one: it shows the raw text rather than a summary built from a guess.
func TestMalformedArgumentsDecline(t *testing.T) {
	cases := map[string]string{
		"not json":      `{"path": `,
		"no path":       `{"edits":[{"old":"a","new":"b"}]}`,
		"no edits":      `{"path":"a.go"}`,
		"empty edits":   `{"path":"a.go","edits":[]}`,
		"bad edit item": `{"path":"a.go","edits":["nope"]}`,
		"no content":    `{"path":"a.go"}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if got, ok := renderCall(t, "edit_file", args); ok {
				t.Errorf("should have declined, got %q", got)
			}
		})
	}
}

// TestRendererIsRegistered pins the wiring, since a renderer that is never
// collected is a diff nobody sees.
func TestRendererIsRegistered(t *testing.T) {
	e, err := New(Config{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	rs := e.ApprovalRenderers()
	if len(rs) != 1 {
		t.Fatalf("got %d renderers, want 1", len(rs))
	}
	got, ok := rs[0](context.Background(), agent.ToolCallInfo{
		Call: agent.ToolCall{Name: "edit_file", Args: core.NewRawJSON([]byte(`{"path":"a.go","edits":[{"old":"x","new":"y"}]}`))},
	})
	if !ok || !strings.Contains(got, "a.go") {
		t.Errorf("registered renderer did not render: %q", got)
	}
}

func jsonQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `"`, `\"`), "\n", `\n`) + `"`
}
