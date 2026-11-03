package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// PatchTool is the multi-file patch tool's name.
const PatchTool = "apply_patch"

func patchDef() core.ToolDef {
	return core.ToolDef{
		Name:  PatchTool,
		Title: "Apply a patch",
		Description: "Change one or more files in one call: add, delete, update and rename. Pass the whole patch as input:\n\n" +
			"*** Begin Patch\n*** Update File: path/to/file.go\n@@ func Example\n context line\n-removed line\n+added line\n" +
			"*** Add File: path/to/new.go\n+every line of the new file\n*** Delete File: path/to/old.go\n*** End Patch\n\n" +
			"An update may add \"*** Move to: new/path\" after its header to rename the file. Give a few unchanged lines of context around each change, " +
			"so the hunk is found by its content; \"@@ text\" first finds a line such as a function header to search after. " +
			"The patch applies completely or not at all: if one hunk doesn't match, no file changes and the error names the hunk.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"input"},
			"properties": map[string]any{
				"input": map[string]any{"type": "string", "description": "The patch, from *** Begin Patch to *** End Patch."},
			},
		},
		// Absent destructiveHint reads as destructive, as for the other writes;
		// surfaces marks it reversible where a checkpoint covers it.
	}
}

// patchOp is one file change, worked out in full before anything is written.
type patchOp struct {
	fp FilePatch

	wr   *workspaceRoot
	rel  string
	mode os.FileMode
	// had and old are the file's state before the patch, for rollback.
	had bool
	old string
	// nw is what the file (or its move target) becomes.
	nw string

	// to and toRel are an update's move target.
	to    *workspaceRoot
	toRel string
}

// applyPatch runs apply_patch. It works out every file's new content first,
// so a hunk that doesn't match, a path outside the roots, or a file in the
// wrong state stops the call before anything is written. Then it writes each
// file atomically, and if a write fails part-way it puts back the files it
// already changed.
func (s *Source) applyPatch(args map[string]any) *core.ToolResult {
	text, ok := args["input"].(string)
	if !ok || strings.TrimSpace(text) == "" {
		return toolError("apply_patch needs input: the patch, from *** Begin Patch to *** End Patch")
	}
	patches, err := ParsePatch(text)
	if err != nil {
		return toolError("apply_patch: " + err.Error())
	}
	ops, err := s.planPatch(patches)
	if err != nil {
		return toolError("apply_patch: " + err.Error() + ". No file was changed.")
	}
	if err := s.commitPatch(ops); err != nil {
		return toolError("apply_patch: " + err.Error())
	}

	var b strings.Builder
	b.WriteString("Patch applied:\n")
	var files []map[string]any
	for _, op := range ops {
		path, verb := op.wr.abs(op.rel), ""
		entry := map[string]any{}
		switch {
		case op.fp.Kind == PatchAdd:
			verb = "A"
		case op.fp.Kind == PatchDelete:
			verb = "D"
		case op.to != nil:
			verb = "R"
			entry["from"] = path
			path = op.to.abs(op.toRel)
		default:
			verb = "M"
		}
		entry["path"], entry["change"] = path, verb
		if op.fp.Kind != PatchDelete {
			entry["hash"] = Hash(op.nw)
			fmt.Fprintf(&b, "%s %s (hash: %s)\n", verb, path, Hash(op.nw))
		} else {
			fmt.Fprintf(&b, "%s %s\n", verb, path)
		}
		files = append(files, entry)
	}
	return &core.ToolResult{
		Content:           []core.Content{{Type: "text", Text: strings.TrimRight(b.String(), "\n")}},
		StructuredContent: map[string]any{"files": files},
	}
}

func (s *Source) planPatch(patches []FilePatch) ([]patchOp, error) {
	ops := make([]patchOp, 0, len(patches))
	for _, fp := range patches {
		wr, rel, err := s.resolve(fp.Path)
		if err != nil {
			return nil, err
		}
		op := patchOp{fp: fp, wr: wr, rel: rel, mode: 0o644}
		info, statErr := wr.h.Stat(rel)
		switch {
		case statErr == nil && !info.Mode().IsRegular():
			return nil, fmt.Errorf("%s is not a regular file", fp.Path)
		case statErr == nil:
			b, err := wr.h.ReadFile(rel)
			if err != nil {
				return nil, errors.New(escapeMessage(fp.Path, err))
			}
			op.had, op.old, op.mode = true, string(b), info.Mode().Perm()
		case !errors.Is(statErr, os.ErrNotExist):
			return nil, errors.New(escapeMessage(fp.Path, statErr))
		}

		switch fp.Kind {
		case PatchAdd:
			if op.had {
				return nil, fmt.Errorf("can't add %s: it already exists; use *** Update File to change it", fp.Path)
			}
			op.nw = fp.Content
		case PatchDelete:
			if !op.had {
				return nil, fmt.Errorf("can't delete %s: it doesn't exist", fp.Path)
			}
		case PatchUpdate:
			if !op.had {
				return nil, fmt.Errorf("can't update %s: it doesn't exist; use *** Add File to create it", fp.Path)
			}
			op.nw = op.old
			if len(fp.Chunks) > 0 {
				nw, err := ApplyChunks(op.old, fp.Chunks)
				if err != nil {
					return nil, fmt.Errorf("%s: %v", fp.Path, err)
				}
				op.nw = nw
			}
			if fp.MoveTo != "" {
				to, toRel, err := s.resolve(fp.MoveTo)
				if err != nil {
					return nil, err
				}
				if _, err := to.h.Stat(toRel); err == nil {
					return nil, fmt.Errorf("can't move %s to %s: the target exists", fp.Path, fp.MoveTo)
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, errors.New(escapeMessage(fp.MoveTo, err))
				}
				op.to, op.toRel = to, toRel
			}
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// commitPatch writes the planned changes in order. On a failure it undoes the
// ones already made, newest first, and reports both errors if the undo fails
// too, since the workspace is then in a state the caller has to know about.
func (s *Source) commitPatch(ops []patchOp) error {
	var undo []func() error
	fail := func(err error) error {
		var bad []string
		for i := len(undo) - 1; i >= 0; i-- {
			if uerr := undo[i](); uerr != nil {
				bad = append(bad, uerr.Error())
			}
		}
		if len(bad) > 0 {
			return fmt.Errorf("%v, and putting back the files already changed failed too: %s", err, strings.Join(bad, "; "))
		}
		return fmt.Errorf("%v. The files already changed were put back", err)
	}
	restore := func(wr *workspaceRoot, rel, content string, mode os.FileMode) func() error {
		return func() error { return s.writeThroughRoot(wr, rel, content, mode) }
	}
	remove := func(wr *workspaceRoot, rel string) func() error {
		return func() error { return wr.h.Remove(rel) }
	}
	for _, op := range ops {
		switch {
		case op.fp.Kind == PatchDelete:
			if err := op.wr.h.Remove(op.rel); err != nil {
				return fail(fmt.Errorf("can't delete %s: %v", op.fp.Path, err))
			}
			undo = append(undo, restore(op.wr, op.rel, op.old, op.mode))

		case op.to != nil:
			if err := s.mkdirFor(op.to, op.toRel); err != nil {
				return fail(fmt.Errorf("can't move %s to %s: %v", op.fp.Path, op.fp.MoveTo, err))
			}
			if err := s.writeThroughRoot(op.to, op.toRel, op.nw, op.mode); err != nil {
				return fail(fmt.Errorf("can't write %s: %v", op.fp.MoveTo, err))
			}
			undo = append(undo, remove(op.to, op.toRel))
			if err := op.wr.h.Remove(op.rel); err != nil {
				return fail(fmt.Errorf("can't remove %s after moving it: %v", op.fp.Path, err))
			}
			undo = append(undo, restore(op.wr, op.rel, op.old, op.mode))

		default:
			if !op.had {
				if err := s.mkdirFor(op.wr, op.rel); err != nil {
					return fail(fmt.Errorf("can't create %s: %v", op.fp.Path, err))
				}
			}
			if err := s.writeThroughRoot(op.wr, op.rel, op.nw, op.mode); err != nil {
				return fail(fmt.Errorf("can't write %s: %v", op.fp.Path, err))
			}
			if op.had {
				undo = append(undo, restore(op.wr, op.rel, op.old, op.mode))
			} else {
				undo = append(undo, remove(op.wr, op.rel))
			}
		}
	}
	return nil
}

// mkdirFor creates rel's directory through the root, so it can't climb out.
func (s *Source) mkdirFor(wr *workspaceRoot, rel string) error {
	if dir := filepath.Dir(rel); dir != "." {
		return wr.h.MkdirAll(dir, 0o755)
	}
	return nil
}

// patchPaths lists every file a patch call would touch, both ends of a move
// included, as absolute paths. A patch that doesn't parse, or names a path
// outside the roots, touches nothing.
func (s *Source) patchPaths(args map[string]any) []string {
	text, _ := args["input"].(string)
	patches, err := ParsePatch(text)
	if err != nil {
		return nil
	}
	var out []string
	for _, fp := range patches {
		for _, p := range []string{fp.Path, fp.MoveTo} {
			if p == "" {
				continue
			}
			wr, rel, err := s.resolve(p)
			if err != nil {
				return nil
			}
			out = append(out, wr.abs(rel))
		}
	}
	return out
}
