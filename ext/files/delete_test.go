package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteRemovesTheFile(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "original\n")

	text, isErr := call(t, s, "delete_file", map[string]any{
		"path": "notes.md", "expect_hash": Hash("original\n"),
	})
	if isErr {
		t.Fatalf("delete_file failed: %s", text)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("the file is still there (stat err %v)", err)
	}
	if !strings.Contains(text, "deleted") {
		t.Errorf("reply should say what it did:\n%s", text)
	}
	if !strings.Contains(text, p) {
		t.Errorf("reply should name the file it deleted:\n%s", text)
	}
}

// TestDeleteNeedsExpectHash is the same discipline write_file has, for the
// same reason: a delete aimed at content nobody looked at is a delete of a file
// that has since changed. There is no way to spell "remove whatever is there".
func TestDeleteNeedsExpectHash(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "original\n")

	text, isErr := call(t, s, "delete_file", map[string]any{"path": "notes.md"})
	if !isErr {
		t.Fatalf("delete_file must refuse without expect_hash, got: %s", text)
	}
	if got := readFile(t, p); got != "original\n" {
		t.Errorf("the file was deleted despite the refusal: %q", got)
	}
	if !strings.Contains(text, "read the file first") || !strings.Contains(text, "expect_hash") {
		t.Errorf("refusal should say how to fix it: %s", text)
	}
}

func TestDeleteRefusesAStaleHash(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "notes.md", "original\n")

	text, isErr := call(t, s, "delete_file", map[string]any{
		"path": "notes.md", "expect_hash": Hash("something else"),
	})
	if !isErr {
		t.Fatalf("delete_file must refuse a stale hash, got: %s", text)
	}
	if got := readFile(t, p); got != "original\n" {
		t.Errorf("the file was deleted despite the stale hash: %q", got)
	}
	if !strings.Contains(text, "re-read it") {
		t.Errorf("refusal should say how to fix it: %s", text)
	}
}

func TestDeleteAMissingFileSaysSo(t *testing.T) {
	s, _ := newTestSource(t)
	text, isErr := call(t, s, "delete_file", map[string]any{
		"path": "ghost.txt", "expect_hash": Hash("anything"),
	})
	if !isErr {
		t.Fatalf("delete_file should refuse a missing file, got: %s", text)
	}
	// A missing file and a stale hash are different mistakes with different
	// fixes, so the message must not read as though the content changed.
	if strings.Contains(text, "re-read it") {
		t.Errorf("a missing file should not be reported as a stale one: %s", text)
	}
}

// TestDeleteRefusesToEscapeTheRoot is the same boundary as every other tool
// here. A delete is the worst one to get wrong: reading a file outside the root
// leaks it, writing one corrupts it, and deleting one destroys it.
func TestDeleteRefusesToEscapeTheRoot(t *testing.T) {
	s, _ := newTestSource(t)
	for _, path := range []string{"../escaped.txt", "/etc/passwd"} {
		text, isErr := call(t, s, "delete_file", map[string]any{"path": path, "expect_hash": "x"})
		if !isErr {
			t.Fatalf("delete_file should refuse %s, got: %s", path, text)
		}
		if !strings.Contains(text, "outside every workspace root") {
			t.Errorf("refusal should say why: %s", text)
		}
	}
}

// TestDeleteRefusesASymlink mirrors the write side. Removing the link is
// unremarkable, but the tool locates the file by reading it to check the hash,
// and a read through a link is a read of a file outside the root.
func TestDeleteRefusesASymlink(t *testing.T) {
	s, root := newTestSource(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "important.conf")
	const original = "keep me\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "innocent.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	text, isErr := call(t, s, "delete_file", map[string]any{
		"path": "innocent.txt", "expect_hash": Hash(original),
	})
	if !isErr {
		t.Fatalf("delete_file should refuse a symlink, got: %s", text)
	}
	if got := readFile(t, target); got != original {
		t.Fatalf("deleted a file outside every workspace root: %q", got)
	}
}

// TestDeleteADirectorySaysItIsNotAFile pins the message, not a safety
// property. Without the check the removal still fails, but the caller is told
// its content changed and is invited to re-read a directory, which can never
// work.
func TestDeleteADirectorySaysItIsNotAFile(t *testing.T) {
	s, root := newTestSource(t)
	if err := os.MkdirAll(filepath.Join(root, "somedir"), 0o755); err != nil {
		t.Fatal(err)
	}

	text, isErr := call(t, s, "delete_file", map[string]any{
		"path": "somedir", "expect_hash": "x",
	})
	if !isErr {
		t.Fatalf("delete_file should refuse a directory, got: %s", text)
	}
	if !strings.Contains(text, "not a regular file") {
		t.Errorf("refusal should say what it found, not read as a stale hash: %s", text)
	}
	if _, err := os.Stat(filepath.Join(root, "somedir")); err != nil {
		t.Errorf("the directory should still be there: %v", err)
	}
}

func TestDeleteNeedsAPath(t *testing.T) {
	s, _ := newTestSource(t)
	text, isErr := call(t, s, "delete_file", map[string]any{"expect_hash": "x"})
	if !isErr {
		t.Fatalf("delete_file should refuse a missing path, got: %s", text)
	}
	if !strings.Contains(text, "path") {
		t.Errorf("refusal should name the missing argument: %s", text)
	}
}

// TestDeletePathsFeedsACheckpointWriteSpec is the property /undo depends on.
// The checkpoint captures BEFORE dispatch, so it can only see the file if this
// path resolver points at it beforehand. A delete absent from this list is one
// the checkpoint never asks about, and /undo then reports a file that cannot
// come back.
func TestDeletePathsFeedsACheckpointWriteSpec(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "notes.md", "x")

	got := e.WritePaths(map[string]any{"path": "notes.md"})
	if len(got) != 1 || got[0] != filepath.Join(root, "notes.md") {
		t.Fatalf("WritePaths(delete_file) = %v, want [%s]", got, filepath.Join(root, "notes.md"))
	}
}
