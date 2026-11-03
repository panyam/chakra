package agentsmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// doc is one loaded file as the model sees it.
type doc struct {
	path  string // absolute
	text  string // after comment stripping, trimmed
	label string // shown after the path in the heading, may be empty
}

// fileStat records a file's size and mtime. A cached read is reused only
// while both still match.
type fileStat struct {
	size  int64
	mtime time.Time
}

type cachedFile struct {
	stat fileStat
	text string
}

// reader reads files through a stat-checked cache. Checking a directory for
// changes before every model call then needs one stat per file, plus a read
// only for a file that actually changed.
type reader struct {
	cache map[string]cachedFile
}

// read returns a file's text, "" when it is missing, unreadable, a directory
// or empty after trimming. Empty files are skipped, as Codex does.
func (r *reader) read(path string) string {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		delete(r.cache, path)
		return ""
	}
	st := fileStat{size: fi.Size(), mtime: fi.ModTime()}
	if c, ok := r.cache[path]; ok && c.stat == st {
		return c.text
	}
	b, err := os.ReadFile(path)
	if err != nil {
		delete(r.cache, path)
		return ""
	}
	text := strings.TrimSpace(string(b))
	r.cache[path] = cachedFile{stat: st, text: text}
	return text
}

// loader resolves which files apply where, for one profile and set of roots.
type loader struct {
	p     Profile
	roots []string // cleaned, symlinks resolved
	home  string
	r     reader
}

// chainDirs returns the startup directories, outermost first, deduplicated
// across roots.
func (l *loader) chainDirs() []string {
	roots := l.roots
	if l.p.PrimaryOnly && len(roots) > 1 {
		roots = roots[:1]
	}
	var out []string
	seen := map[string]bool{}
	for _, root := range roots {
		for _, d := range l.chainFor(root) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

func (l *loader) chainFor(root string) []string {
	var up []string // root first, climbing
	switch l.p.Walk {
	case WalkFSRoot:
		for d := root; ; d = filepath.Dir(d) {
			if filepath.Dir(d) == d {
				break // the filesystem root itself is not read
			}
			up = append(up, d)
		}
	default:
		markers := l.p.Markers
		if len(markers) == 0 {
			markers = []string{".git"}
		}
		found := false
		for d := root; ; d = filepath.Dir(d) {
			up = append(up, d)
			if hasAny(d, markers) {
				found = true
				break
			}
			if filepath.Dir(d) == d {
				break
			}
		}
		if !found {
			up = []string{root}
		}
	}
	out := make([]string, len(up))
	for i, d := range up {
		out[len(up)-1-i] = d
	}
	return out
}

func hasAny(dir string, names []string) bool {
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// global returns the user's own file and its imports, if configured.
func (l *loader) global() []doc {
	if l.p.GlobalDir == "" {
		return nil
	}
	dir := l.p.GlobalDir
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if l.home == "" {
			return nil
		}
		dir = filepath.Join(l.home, strings.TrimPrefix(dir, "~"))
	}
	docs := l.primary(dir, true)
	for i := range docs {
		if docs[i].label == "" {
			docs[i].label = "your global file"
		}
	}
	return docs
}

// chain returns the startup chain's files, outermost first. Fallback names
// apply only when no directory in it has a primary file.
func (l *loader) chain(dirs []string) []doc {
	var docs []doc
	for _, d := range dirs {
		docs = append(docs, l.primary(d, false)...)
	}
	if len(docs) > 0 || len(l.p.Fallback) == 0 {
		return docs
	}
	for _, d := range dirs {
		docs = append(docs, l.fallback(d)...)
	}
	return docs
}

// subdir returns one lazily read directory's files.
func (l *loader) subdir(dir string) []doc {
	if docs := l.primary(dir, false); len(docs) > 0 {
		return docs
	}
	return l.fallback(dir)
}

// primary reads a directory's override or first present name, then its
// local file, each followed by what it imports.
func (l *loader) primary(dir string, global bool) []doc {
	var docs []doc
	main := ""
	if l.p.Override != "" {
		main = l.pick(dir, []string{l.p.Override})
	}
	if main == "" {
		main = l.pick(dir, l.p.Names)
	}
	if main != "" {
		docs = append(docs, l.expand(main, global)...)
	}
	if l.p.Local != "" && !global {
		if p := l.pick(dir, []string{l.p.Local}); p != "" {
			docs = append(docs, l.expand(p, false)...)
		}
	}
	return docs
}

func (l *loader) fallback(dir string) []doc {
	if p := l.pick(dir, l.p.Fallback); p != "" {
		return l.expand(p, false)
	}
	return nil
}

// pick returns the first name in dir with non-empty text.
func (l *loader) pick(dir string, names []string) string {
	for _, n := range names {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if l.r.read(p) != "" {
			return p
		}
	}
	return ""
}

// maxImportDepth copies Claude Code's limit. An imported file may import
// others, four hops at most.
const maxImportDepth = 4

// expand returns path's doc followed by its imports, depth first.
func (l *loader) expand(path string, global bool) []doc {
	var out []doc
	seen := map[string]bool{}
	var walk func(p, by string, depth int)
	walk = func(p, by string, depth int) {
		if seen[p] {
			return
		}
		seen[p] = true
		text := l.r.read(p)
		if text == "" {
			return
		}
		if l.p.StripComments {
			text = strings.TrimSpace(stripComments(text))
		}
		d := doc{path: p, text: text}
		if by != "" {
			d.label = "imported by " + by
		}
		out = append(out, d)
		if !l.p.Imports || depth >= maxImportDepth {
			return
		}
		for _, ref := range importRefs(text) {
			target := l.resolveImport(ref, filepath.Dir(p))
			if target == "" || (!global && !l.inRoots(target)) {
				// A repository file can't pull in the user's own files.
				// Otherwise a pull request could send ~/.ssh to the model.
				continue
			}
			walk(target, p, depth+1)
		}
	}
	walk(path, "", 0)
	return out
}

func (l *loader) resolveImport(ref, dir string) string {
	switch {
	case ref == "~" || strings.HasPrefix(ref, "~/"):
		if l.home == "" {
			return ""
		}
		ref = filepath.Join(l.home, strings.TrimPrefix(ref, "~"))
	case !filepath.IsAbs(ref):
		ref = filepath.Join(dir, ref)
	}
	return filepath.Clean(ref)
}

// inRoots reports whether path is inside a workspace root.
func (l *loader) inRoots(path string) bool {
	return l.rootOf(path) != ""
}

// rootOf returns the workspace root that contains path, or "".
func (l *loader) rootOf(path string) string {
	for _, r := range l.roots {
		if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
			return r
		}
	}
	return ""
}

var fence = regexp.MustCompile("(?m)^[ \t]*(```|~~~)")

// outsideFences calls fn on each stretch of text outside fenced code blocks
// and returns the text with those stretches replaced by fn's result.
func outsideFences(text string, fn func(string) string) string {
	var b strings.Builder
	in := false
	rest := text
	for {
		loc := fence.FindStringIndex(rest)
		if loc == nil {
			break
		}
		// The fence line belongs to the code block on both ends.
		lineEnd := strings.IndexByte(rest[loc[1]:], '\n')
		end := len(rest)
		if lineEnd >= 0 {
			end = loc[1] + lineEnd + 1
		}
		if in {
			b.WriteString(rest[:end])
		} else {
			b.WriteString(fn(rest[:loc[0]]))
			b.WriteString(rest[loc[0]:end])
		}
		in = !in
		rest = rest[end:]
	}
	if in {
		b.WriteString(rest)
	} else {
		b.WriteString(fn(rest))
	}
	return b.String()
}

var blockComment = regexp.MustCompile(`(?ms)^[ \t]*<!--.*?-->[ \t]*\n?`)

// stripComments drops HTML comments that start a line, outside code blocks.
func stripComments(text string) string {
	return outsideFences(text, func(s string) string { return blockComment.ReplaceAllString(s, "") })
}

var (
	codeSpan  = regexp.MustCompile("`[^`\n]*`")
	importRef = regexp.MustCompile(`(?:^|[ \t])@((?:[^\s\\"']|\\ )+)`)
)

// importRefs returns the @path references in text, outside code spans and
// fenced blocks. A backslash before a space keeps the space in the path; a
// quoted path is not an import.
func importRefs(text string) []string {
	var refs []string
	outsideFences(text, func(s string) string {
		for _, line := range strings.Split(codeSpan.ReplaceAllString(s, " "), "\n") {
			for _, m := range importRef.FindAllStringSubmatch(line, -1) {
				refs = append(refs, strings.ReplaceAll(m[1], `\ `, " "))
			}
		}
		return s
	})
	return refs
}
