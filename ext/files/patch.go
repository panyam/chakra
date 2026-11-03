package files

import (
	"fmt"
	"strings"
)

// The patch envelope apply_patch takes, the format several models are
// trained to write:
//
//	*** Begin Patch
//	*** Update File: path/to/file.go
//	*** Move to: path/to/renamed.go     (optional)
//	@@ func Foo                          (optional context to search for first)
//	 context line
//	-removed line
//	+added line
//	*** End of File                      (optional: the hunk ends the file)
//	*** Add File: new.go
//	+every line of the new file
//	*** Delete File: old.go
//	*** End Patch
const (
	patchBegin  = "*** Begin Patch"
	patchEnd    = "*** End Patch"
	patchAdd    = "*** Add File: "
	patchDelete = "*** Delete File: "
	patchUpdate = "*** Update File: "
	patchMove   = "*** Move to: "
	patchEOF    = "*** End of File"
)

// PatchKind is what a patch does to one file.
type PatchKind int

const (
	// PatchAdd creates a file that must not exist yet.
	PatchAdd PatchKind = iota
	// PatchDelete removes a file that must exist.
	PatchDelete
	// PatchUpdate changes a file in place, or moves it when MoveTo is set.
	PatchUpdate
)

// FilePatch is one file's section of a patch.
type FilePatch struct {
	Kind PatchKind
	Path string
	// MoveTo is the new path of an updated file, or "".
	MoveTo string
	// Content is an added file's text.
	Content string
	// Chunks are an update's hunks, in file order.
	Chunks []PatchChunk
}

// PatchChunk is one hunk of an update: the lines it replaces and the lines
// it puts there, each including the hunk's context lines.
type PatchChunk struct {
	// Context, from an "@@ text" header, is a line to find before looking for
	// the hunk, which narrows where it can match.
	Context string
	Old     []string
	New     []string
	// EOF anchors the hunk to the end of the file.
	EOF bool
}

// ParsePatch reads a patch envelope. A malformed patch is an error naming the
// line, because a section that was silently skipped is a change the model
// believes it made.
func ParsePatch(text string) ([]FilePatch, error) {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != patchBegin {
		return nil, fmt.Errorf("a patch starts with %q", patchBegin)
	}
	if strings.TrimSpace(lines[len(lines)-1]) != patchEnd {
		return nil, fmt.Errorf("a patch ends with %q", patchEnd)
	}
	body := lines[1 : len(lines)-1]

	var out []FilePatch
	seen := map[string]bool{}
	claim := func(path string, n int) error {
		if seen[path] {
			return fmt.Errorf("line %d: %s appears twice in one patch", n, path)
		}
		seen[path] = true
		return nil
	}
	for i := 0; i < len(body); {
		n := i + 2 // 1-based, after "*** Begin Patch"
		line := body[i]
		switch {
		case strings.HasPrefix(line, patchAdd):
			fp := FilePatch{Kind: PatchAdd, Path: strings.TrimSpace(strings.TrimPrefix(line, patchAdd))}
			i++
			var content []string
			for i < len(body) && !strings.HasPrefix(body[i], "*** ") {
				if !strings.HasPrefix(body[i], "+") {
					return nil, fmt.Errorf("line %d: every line of an added file starts with +", i+2)
				}
				content = append(content, strings.TrimPrefix(body[i], "+"))
				i++
			}
			fp.Content = strings.Join(content, "\n")
			if len(content) > 0 {
				fp.Content += "\n"
			}
			if err := claim(fp.Path, n); err != nil {
				return nil, err
			}
			out = append(out, fp)

		case strings.HasPrefix(line, patchDelete):
			fp := FilePatch{Kind: PatchDelete, Path: strings.TrimSpace(strings.TrimPrefix(line, patchDelete))}
			if err := claim(fp.Path, n); err != nil {
				return nil, err
			}
			out = append(out, fp)
			i++

		case strings.HasPrefix(line, patchUpdate):
			fp := FilePatch{Kind: PatchUpdate, Path: strings.TrimSpace(strings.TrimPrefix(line, patchUpdate))}
			i++
			if i < len(body) && strings.HasPrefix(body[i], patchMove) {
				fp.MoveTo = strings.TrimSpace(strings.TrimPrefix(body[i], patchMove))
				i++
			}
			for i < len(body) && !isSectionStart(body[i]) {
				var c PatchChunk
				if strings.HasPrefix(body[i], "@@") {
					c.Context = strings.TrimSpace(strings.TrimPrefix(body[i], "@@"))
					i++
				}
				start := i
				for i < len(body) && !isSectionStart(body[i]) && !strings.HasPrefix(body[i], "@@") {
					l := body[i]
					switch {
					case l == patchEOF:
						c.EOF = true
					case l == "":
						// An empty line inside a hunk is an empty context line;
						// models drop the leading space on blank lines.
						c.Old = append(c.Old, "")
						c.New = append(c.New, "")
					case l[0] == ' ':
						c.Old = append(c.Old, l[1:])
						c.New = append(c.New, l[1:])
					case l[0] == '-':
						c.Old = append(c.Old, l[1:])
					case l[0] == '+':
						c.New = append(c.New, l[1:])
					default:
						return nil, fmt.Errorf("line %d: a hunk line starts with a space, - or +, got %q", i+2, l)
					}
					i++
					if c.EOF {
						break
					}
				}
				if i == start && c.Context == "" {
					return nil, fmt.Errorf("line %d: empty hunk", n)
				}
				if len(c.Old) == 0 && len(c.New) == 0 {
					return nil, fmt.Errorf("line %d: a hunk in %s changes nothing", start+2, fp.Path)
				}
				fp.Chunks = append(fp.Chunks, c)
			}
			if len(fp.Chunks) == 0 && fp.MoveTo == "" {
				return nil, fmt.Errorf("line %d: %s has no hunks", n, fp.Path)
			}
			if err := claim(fp.Path, n); err != nil {
				return nil, err
			}
			if fp.MoveTo != "" {
				if err := claim(fp.MoveTo, n); err != nil {
					return nil, err
				}
			}
			out = append(out, fp)

		case strings.TrimSpace(line) == "":
			i++

		default:
			return nil, fmt.Errorf("line %d: expected *** Add File, *** Delete File or *** Update File, got %q", n, line)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the patch changes no files")
	}
	return out, nil
}

func isSectionStart(l string) bool {
	return strings.HasPrefix(l, patchAdd) || strings.HasPrefix(l, patchDelete) || strings.HasPrefix(l, patchUpdate)
}

// ApplyChunks applies an update's hunks to src and returns the new text. Each
// hunk's lines are found at or after the previous hunk, trying an exact match,
// then one ignoring trailing whitespace, then one ignoring whitespace at both
// ends. A hunk that matches nowhere is an error naming it, and nothing is
// changed.
func ApplyChunks(src string, chunks []PatchChunk) (string, error) {
	lines := strings.Split(src, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	type repl struct {
		at, n int
		with  []string
	}
	var repls []repl
	cursor := 0
	for ci, c := range chunks {
		if c.Context != "" {
			at := seek(lines, []string{c.Context}, cursor, false)
			if at < 0 {
				return "", fmt.Errorf("hunk %d: can't find the line %q its @@ header names", ci+1, c.Context)
			}
			cursor = at + 1
		}
		if len(c.Old) == 0 {
			// A pure insertion goes at the end of the file.
			repls = append(repls, repl{at: len(lines), with: c.New})
			continue
		}
		old, nw := c.Old, c.New
		at := seek(lines, old, cursor, c.EOF)
		if at < 0 && len(old) > 1 && old[len(old)-1] == "" {
			// A trailing blank context line is often the model's, not the file's.
			old = old[:len(old)-1]
			if len(nw) > 0 && nw[len(nw)-1] == "" {
				nw = nw[:len(nw)-1]
			}
			at = seek(lines, old, cursor, c.EOF)
		}
		if at < 0 {
			return "", fmt.Errorf("hunk %d doesn't match the file; it expected these lines:\n%s", ci+1, strings.Join(old, "\n"))
		}
		repls = append(repls, repl{at: at, n: len(old), with: nw})
		cursor = at + len(old)
	}
	for i := len(repls) - 1; i >= 0; i-- {
		r := repls[i]
		tail := append([]string(nil), lines[r.at+r.n:]...)
		lines = append(append(lines[:r.at], r.with...), tail...)
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// seek finds want in lines at or after start, at the end when eof, with three
// levels of tolerance for whitespace. It returns -1 when nothing matches.
func seek(lines, want []string, start int, eof bool) int {
	if len(want) > len(lines) {
		return -1
	}
	norms := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return strings.TrimRight(s, " \t") },
		strings.TrimSpace,
	}
	for _, norm := range norms {
		match := func(at int) bool {
			for j, w := range want {
				if norm(lines[at+j]) != norm(w) {
					return false
				}
			}
			return true
		}
		if eof {
			if at := len(lines) - len(want); at >= start && match(at) {
				return at
			}
		}
		for at := start; at+len(want) <= len(lines); at++ {
			if match(at) {
				return at
			}
		}
	}
	return -1
}
