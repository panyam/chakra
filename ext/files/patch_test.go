package files

import (
	"encoding/json"

	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

func TestParsePatchReadsEverySectionKind(t *testing.T) {
	patches, err := ParsePatch(`*** Begin Patch
*** Add File: a/new.go
+package a
+
*** Delete File: old.go
*** Update File: stats.go
*** Move to: lib/stats.go
@@ func Sum
-	for i := 1; i < n; i++ {
+	for i := 0; i < n; i++ {
@@
 	return t
+}
*** End of File
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 3 {
		t.Fatalf("%d sections", len(patches))
	}
	add, del, up := patches[0], patches[1], patches[2]
	if add.Kind != PatchAdd || add.Path != "a/new.go" || add.Content != "package a\n\n" {
		t.Errorf("add = %+v", add)
	}
	if del.Kind != PatchDelete || del.Path != "old.go" {
		t.Errorf("delete = %+v", del)
	}
	if up.Kind != PatchUpdate || up.MoveTo != "lib/stats.go" || len(up.Chunks) != 2 {
		t.Fatalf("update = %+v", up)
	}
	if c := up.Chunks[0]; c.Context != "func Sum" || len(c.Old) != 1 || len(c.New) != 1 || c.EOF {
		t.Errorf("chunk 1 = %+v", c)
	}
	if c := up.Chunks[1]; !c.EOF || strings.Join(c.Old, "|") != "\treturn t" || strings.Join(c.New, "|") != "\treturn t|}" {
		t.Errorf("chunk 2 = %+v", c)
	}
}

func TestParsePatchRefusesMalformedInput(t *testing.T) {
	for name, text := range map[string]string{
		"no begin":         "*** Update File: a\n-x\n+y\n*** End Patch",
		"no end":           "*** Begin Patch\n*** Update File: a\n-x\n+y",
		"empty":            "*** Begin Patch\n*** End Patch",
		"bad header":       "*** Begin Patch\n*** Rename File: a\n*** End Patch",
		"add without plus": "*** Begin Patch\n*** Add File: a\nhello\n*** End Patch",
		"bad hunk line":    "*** Begin Patch\n*** Update File: a\n~x\n*** End Patch",
		"update no hunks":  "*** Begin Patch\n*** Update File: a\n*** End Patch",
		"same file twice":  "*** Begin Patch\n*** Delete File: a\n*** Delete File: a\n*** End Patch",
		"move onto itself": "*** Begin Patch\n*** Update File: a\n*** Move to: b\n*** Delete File: b\n*** End Patch",
	} {
		if _, err := ParsePatch(text); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestApplyChunksToleratesWhitespaceInThreeSteps(t *testing.T) {
	src := "a\nb  \n  c\nd\n"
	cases := map[string]PatchChunk{
		"exact":           {Old: []string{"a"}, New: []string{"A"}},
		"trailing spaces": {Old: []string{"b"}, New: []string{"B"}},
		"both ends":       {Old: []string{"c"}, New: []string{"C"}},
	}
	for name, c := range cases {
		if _, err := ApplyChunks(src, []PatchChunk{c}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ApplyChunks(src, []PatchChunk{{Old: []string{"zz"}, New: []string{"y"}}}); err == nil || !strings.Contains(err.Error(), "hunk 1") {
		t.Errorf("a hunk matching nothing: %v", err)
	}
}

func TestApplyChunksUsesContextOrderAndEOF(t *testing.T) {
	src := "func A() {\n\tx := 1\n}\nfunc B() {\n\tx := 1\n}\nx := 1\n"
	got, err := ApplyChunks(src, []PatchChunk{{Context: "func B() {", Old: []string{"\tx := 1"}, New: []string{"\tx := 2"}}})
	if err != nil || !strings.Contains(got, "func B() {\n\tx := 2") || !strings.HasPrefix(got, "func A() {\n\tx := 1") {
		t.Errorf("@@ context: %q, %v", got, err)
	}
	got, err = ApplyChunks(src, []PatchChunk{{Old: []string{"x := 1"}, New: []string{"x := 3"}, EOF: true}})
	if err != nil || !strings.HasSuffix(got, "}\nx := 3\n") {
		t.Errorf("EOF anchor: %q, %v", got, err)
	}
	got, err = ApplyChunks("a\nb\n", []PatchChunk{{New: []string{"c"}}})
	if err != nil || got != "a\nb\nc\n" {
		t.Errorf("pure insertion: %q, %v", got, err)
	}
}

func applyPatchCall(t *testing.T, s *Source, patch string) (string, bool) {
	t.Helper()
	res, err := s.Call(context.Background(), PatchTool, map[string]any{"input": patch})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].Text, res.IsError
}

func readPatched(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestApplyPatchAddsDeletesUpdatesAndMoves(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "stats.go", "package stats\n\nfunc Sum() {\n\tfor i := 1; i < n; i++ {\n\t}\n}\n")
	write(t, root, "old.go", "package stats\n")
	out, isErr := applyPatchCall(t, s, `*** Begin Patch
*** Update File: stats.go
*** Move to: lib/stats.go
@@ func Sum() {
-	for i := 1; i < n; i++ {
+	for i := 0; i < n; i++ {
*** Add File: lib/new.go
+package lib
*** Delete File: old.go
*** End Patch`)
	if isErr {
		t.Fatal(out)
	}
	if got := readPatched(t, filepath.Join(root, "lib/stats.go")); !strings.Contains(got, "for i := 0;") {
		t.Errorf("moved file = %q", got)
	}
	for _, gone := range []string{"stats.go", "old.go"} {
		if readPatched(t, filepath.Join(root, gone)) != "<missing>" {
			t.Errorf("%s still exists", gone)
		}
	}
	if got := readPatched(t, filepath.Join(root, "lib/new.go")); got != "package lib\n" {
		t.Errorf("added file = %q", got)
	}
	for _, want := range []string{"R " + filepath.Join(root, "lib/stats.go"), "A " + filepath.Join(root, "lib/new.go"), "D " + filepath.Join(root, "old.go")} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
}

func TestApplyPatchChangesNothingWhenAHunkMisses(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.go", "one\n")
	write(t, root, "b.go", "two\n")
	out, isErr := applyPatchCall(t, s, `*** Begin Patch
*** Update File: a.go
-one
+ONE
*** Update File: b.go
-three
+THREE
*** End Patch`)
	if !isErr || !strings.Contains(out, "b.go") || !strings.Contains(out, "No file was changed") {
		t.Fatalf("result = %q", out)
	}
	if got := readPatched(t, filepath.Join(root, "a.go")); got != "one\n" {
		t.Errorf("a.go changed although the patch failed: %q", got)
	}
}

func TestApplyPatchRollsBackWhenAWriteFails(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.go", "one\n")
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if f, err := os.Create(filepath.Join(locked, "probe")); err == nil {
		f.Close()
		t.Skip("running as a user that can write to a read-only directory")
	}
	out, isErr := applyPatchCall(t, s, `*** Begin Patch
*** Update File: a.go
-one
+ONE
*** Add File: locked/new.go
+package x
*** End Patch`)
	if !isErr || !strings.Contains(out, "put back") {
		t.Fatalf("result = %q", out)
	}
	if got := readPatched(t, filepath.Join(root, "a.go")); got != "one\n" {
		t.Errorf("a.go wasn't put back: %q", got)
	}
}

func TestApplyPatchStaysInsideTheRoots(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.go", "one\n")
	outside := filepath.Join(filepath.Dir(root), "escaped.go")
	t.Cleanup(func() { os.Remove(outside) })
	for name, patch := range map[string]string{
		"add":     "*** Begin Patch\n*** Add File: ../escaped.go\n+x\n*** End Patch",
		"move to": "*** Begin Patch\n*** Update File: a.go\n*** Move to: ../escaped.go\n-one\n+two\n*** End Patch",
		"abs":     "*** Begin Patch\n*** Add File: " + outside + "\n+x\n*** End Patch",
	} {
		// The plan refuses this before any file is touched. The root handle
		// does not have to catch it part-way through the writes.
		if out, isErr := applyPatchCall(t, s, patch); !isErr || !strings.Contains(out, "No file was changed") {
			t.Errorf("%s: %s", name, out)
		}
	}
	if readPatched(t, outside) != "<missing>" {
		t.Error("a patch wrote outside the root")
	}
	if got := readPatched(t, filepath.Join(root, "a.go")); got != "one\n" {
		t.Errorf("a.go changed: %q", got)
	}
}

func TestApplyPatchRefusesFilesInTheWrongState(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "a.go", "one\n")
	write(t, root, "b.go", "two\n")
	for name, patch := range map[string]string{
		"add existing":     "*** Begin Patch\n*** Add File: a.go\n+x\n*** End Patch",
		"delete missing":   "*** Begin Patch\n*** Delete File: none.go\n*** End Patch",
		"update missing":   "*** Begin Patch\n*** Update File: none.go\n-x\n+y\n*** End Patch",
		"move onto a file": "*** Begin Patch\n*** Update File: a.go\n*** Move to: b.go\n-one\n+ONE\n*** End Patch",
	} {
		if out, isErr := applyPatchCall(t, s, patch); !isErr {
			t.Errorf("%s: applied: %s", name, out)
		}
	}
}

func TestEditToolsPicksTheEditingTools(t *testing.T) {
	root := t.TempDir()
	names := func(want []string) map[string]bool {
		s, err := NewSource(Config{Roots: []string{root}, EditTools: want})
		if err != nil {
			t.Fatal(err)
		}
		defs, _ := s.Tools(context.Background())
		got := map[string]bool{}
		for _, d := range defs {
			got[d.Name] = true
		}
		return got
	}
	if got := names(nil); !got["edit_file"] || !got[PatchTool] {
		t.Errorf("default: %v", got)
	}
	if got := names([]string{PatchTool}); got["edit_file"] || !got[PatchTool] || !got["read_file"] {
		t.Errorf("apply_patch only: %v", got)
	}
	if got := names([]string{"edit_file"}); !got["edit_file"] || got[PatchTool] {
		t.Errorf("edit_file only: %v", got)
	}
	for _, bad := range [][]string{{}, {"sed"}} {
		if _, err := NewSource(Config{Roots: []string{root}, EditTools: bad}); err == nil {
			t.Errorf("EditTools %q accepted", bad)
		}
	}
}

func TestPatchWritePathsListEveryTouchedFile(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	e, err := New(Config{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	got := e.WritePaths(map[string]any{"input": "*** Begin Patch\n*** Update File: a.go\n*** Move to: b/a.go\n-x\n+y\n*** Delete File: c.go\n*** Add File: d.go\n+z\n*** End Patch"})
	want := []string{"a.go", "b/a.go", "c.go", "d.go"}
	if len(got) != len(want) {
		t.Fatalf("WritePaths = %q", got)
	}
	for i, w := range want {
		if got[i] != filepath.Join(root, w) {
			t.Errorf("path %d = %q, want %q", i, got[i], filepath.Join(root, w))
		}
	}
}

func TestPatchApprovalShowsEachFile(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	e, err := New(Config{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"input": "*** Begin Patch\n*** Update File: a.go\n*** Move to: b.go\n-x\n+y\n*** Delete File: c.go\n*** Add File: d.go\n+package d\n*** End Patch"})
	q, ok := e.renderEditApproval(context.Background(), agent.ToolCallInfo{Call: agent.ToolCall{Name: PatchTool, Args: core.NewRawJSON(args)}})
	if !ok {
		t.Fatal("apply_patch not claimed")
	}
	for _, want := range []string{"Apply this patch to 3 file(s)?", "Move a.go to b.go", "  - x", "  + y", "Delete c.go", "Add d.go:", "package d"} {
		if !strings.Contains(q, want) {
			t.Errorf("approval lacks %q:\n%s", want, q)
		}
	}
	if _, ok := e.renderEditApproval(context.Background(), agent.ToolCallInfo{Call: agent.ToolCall{Name: PatchTool, Args: core.NewRawJSON([]byte(`{"input":"not a patch"}`))}}); ok {
		t.Error("a patch that doesn't parse was claimed; the generic rendering should show it raw")
	}
}

func TestPromptMatchesTheOfferedEditTools(t *testing.T) {
	root := t.TempDir()
	prompt := func(tools []string) string {
		e, err := New(Config{Roots: []string{root}, EditTools: tools})
		if err != nil {
			t.Fatal(err)
		}
		return e.PromptSections()[0].Section(context.Background())
	}
	if p := prompt([]string{PatchTool}); strings.Contains(p, "expect_hash") || !strings.Contains(p, "apply_patch") {
		t.Errorf("apply_patch only:\n%s", p)
	}
	if p := prompt([]string{"edit_file"}); !strings.Contains(p, "expect_hash") || strings.Contains(p, "apply_patch") {
		t.Errorf("edit_file only:\n%s", p)
	}
}
