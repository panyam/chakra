package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra"
)

// deleteExt builds an extension that checkpoints delete_file, which is how the
// surfaces wire it: the same WriteSpec shape as write_file and edit_file,
// keyed by tool name.
func deleteExt(t *testing.T) (*Extension, string) {
	t.Helper()
	work := t.TempDir()
	e, err := New(Config{
		Root: filepath.Join(t.TempDir(), "cp"),
		Writes: []WriteSpec{
			{Tool: "write_file", Paths: pathArg},
			{Tool: "delete_file", Paths: pathArg},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, work
}

// TestUndoBringsBackADeletedFile is the whole point of issue 108. Capture
// happens before dispatch, so the checkpoint holds the content while it still
// exists; /undo writes it back. A delete the checkpoint never captured is one
// /undo reports as an unreversed gap, which is honest but is not a restore.
func TestUndoBringsBackADeletedFile(t *testing.T) {
	e, work := deleteExt(t)
	f := filepath.Join(work, "gone.txt")
	write(t, f, "precious")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := agent.ToolCallInfo{Call: call("delete_file", map[string]any{"path": f})}
	if err := dispatch(t, e, info, func() { os.Remove(f) }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("the delete did not happen (stat err %v)", err)
	}

	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "precious" {
		t.Fatalf("undo restored %q, want %q", got, "precious")
	}
	if !strings.Contains(res.Message, "1 file(s) restored") {
		t.Fatalf("undo report = %q", res.Message)
	}
	// A captured delete is not a gap. Reporting it as one would tell the user
	// a file could not come back when it just did.
	if strings.Contains(res.Message, "NOT undone") {
		t.Fatalf("a captured delete was reported as unreversed: %q", res.Message)
	}
}

// TestDeleteCaptureHoldsTheContentNotJustThePath is the ordering property
// stated as a number. If the snapshot were taken after dispatch there would be
// nothing left to read, the capture would record the path as absent, and /undo
// would delete a file that is already gone while reporting success.
func TestDeleteCaptureHoldsTheContentNotJustThePath(t *testing.T) {
	e, work := deleteExt(t)
	f := filepath.Join(work, "gone.txt")
	write(t, f, "precious")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("delete_file", map[string]any{"path": f})},
		func() { os.Remove(f) }); err != nil {
		t.Fatal(err)
	}

	list, err := e.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Files != 1 {
		t.Fatalf("want one checkpoint holding one file, got %+v", list)
	}
	cp, err := e.store.Load(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := cp.files[f]; got.Kind != kindRegular || got.Hash == "" {
		t.Fatalf("captured %+v, want a regular file with a blob behind it; the snapshot ran after the delete", got)
	}
}

// TestUndoOfADeleteInATurnThatAlsoWrites covers the ordinary case: a turn that
// edits one file and deletes another is one restore point, not two.
func TestUndoOfADeleteInATurnThatAlsoWrites(t *testing.T) {
	e, work := deleteExt(t)
	edit := filepath.Join(work, "edit.txt")
	gone := filepath.Join(work, "gone.txt")
	write(t, edit, "v0")
	write(t, gone, "precious")

	ctx := context.Background()
	if err := e.TurnStart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": edit})},
		func() { write(t, edit, "v1") }); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("delete_file", map[string]any{"path": gone})},
		func() { os.Remove(gone) }); err != nil {
		t.Fatal(err)
	}

	if _, err := e.runUndo(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := read(t, edit); got != "v0" {
		t.Errorf("edit.txt = %q, want %q", got, "v0")
	}
	if got := read(t, gone); got != "precious" {
		t.Fatalf("gone.txt was not restored: %q", got)
	}
}

// TestDeleteOfAnUntrackedToolIsStillAVisibleGap is the other half. A delete
// whose tool has no WriteSpec is not silently ignored: /undo names it, which
// is the only signal the user gets that the file will not come back.
func TestDeleteOfAnUntrackedToolIsStillAVisibleGap(t *testing.T) {
	e, work := deleteExt(t)
	f := filepath.Join(work, "gone.txt")
	write(t, f, "precious")

	ctx := context.Background()
	if err := e.TurnStart(ctx); err != nil {
		t.Fatal(err)
	}
	// "rm_tree" is not in the Writes list, so nothing captures it.
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("rm_tree", map[string]any{"path": f})},
		func() { os.Remove(f) }); err != nil {
		t.Fatal(err)
	}

	if _, err := e.runUndo(ctx, ""); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	gaps := len(e.unreversed)
	e.mu.Unlock()
	if gaps != 1 {
		t.Fatalf("recorded %d gap(s), want 1 for an uncaptured delete", gaps)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("an uncaptured delete must not claim to restore anything")
	}
}
