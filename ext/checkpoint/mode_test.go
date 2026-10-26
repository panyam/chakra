package checkpoint

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// #129: /undo put the content back but left every file 0600, so a script lost
// its executable bit and git saw a mode change on a "restored" tree.
func TestRestoreKeepsEachFilesMode(t *testing.T) {
	s, work := newStore(t)
	script := filepath.Join(work, "run.sh")
	plain := filepath.Join(work, "notes.md")
	write(t, script, "#!/bin/sh\necho hi\n")
	write(t, plain, "notes\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}

	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{script, plain} {
		if err := cp.Add(f); err != nil {
			t.Fatal(err)
		}
		write(t, f, "clobbered")
	}
	if _, err := cp.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, script); got != 0o755 {
		t.Errorf("script restored as %v, want -rwxr-xr-x", got)
	}
	if got := modeOf(t, plain); got != 0o644 {
		t.Errorf("plain file restored as %v, want -rw-r--r--", got)
	}
}

func TestRestoreRecreatesADeletedFileWithItsMode(t *testing.T) {
	s, work := newStore(t)
	script := filepath.Join(work, "run.sh")
	write(t, script, "#!/bin/sh\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Add(script); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, script); got != 0o755 {
		t.Errorf("recreated script is %v, want -rwxr-xr-x", got)
	}
}

// A manifest written before modes were recorded has none. Restore keeps the
// mode of the file it replaces, and uses 0644 for one it recreates.
func TestRestoreFromAManifestWithoutModes(t *testing.T) {
	s, work := newStore(t)
	kept := filepath.Join(work, "run.sh")
	gone := filepath.Join(work, "gone.txt")
	write(t, kept, "#!/bin/sh\n")
	write(t, gone, "bye\n")
	if err := os.Chmod(kept, 0o755); err != nil {
		t.Fatal(err)
	}
	cp, err := s.Open("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{kept, gone} {
		if err := cp.Add(f); err != nil {
			t.Fatal(err)
		}
	}

	// Rewrite the manifest as an older build would have, with no mode field.
	raw, err := os.ReadFile(s.manifestPath("turn-1"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, e := range m["files"].(map[string]any) {
		delete(e.(map[string]any), "mode")
	}
	raw, _ = json.Marshal(m)
	if err := os.WriteFile(s.manifestPath("turn-1"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	write(t, kept, "clobbered")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	old, err := s.Load("turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, kept); got != 0o755 {
		t.Errorf("replaced file is %v, want its current -rwxr-xr-x kept", got)
	}
	if got := modeOf(t, gone); got != 0o644 {
		t.Errorf("recreated file is %v, want -rw-r--r--", got)
	}
}
