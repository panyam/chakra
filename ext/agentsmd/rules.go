package agentsmd

import (
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// A rule folder holds instruction files that each say when they apply, the
// way Cursor's .cursor/rules and Claude Code's .claude/rules do (#184). A
// rule's frontmatter picks one of four triggers:
//
//   - alwaysApply: true, or a .md rule with no trigger at all, loads into the
//     system prompt with the startup chain.
//   - globs (Cursor) or paths (Claude Code) loads the rule once a tool call
//     names a matching path, in the same per-call block as subdirectory
//     files.
//   - description alone lists the rule in the system prompt, with its path,
//     so the model reads it when the task matches.
//   - a .mdc rule with none of these is Cursor's "manual" rule, which applies
//     only when the user mentions it. It isn't loaded.

// ruleKind is when a rule applies.
type ruleKind int

const (
	ruleAlways ruleKind = iota
	ruleGlob
	ruleDescription
	ruleManual
)

// ruleMeta is what a rule's frontmatter says.
type ruleMeta struct {
	always      bool
	description string
	globs       []string
}

// rule is one rule file.
type rule struct {
	path string // absolute
	root string // the workspace root its globs are relative to
	meta ruleMeta
	kind ruleKind
	text string // the body, without frontmatter
}

func (r rule) doc(label string) doc { return doc{path: r.path, text: r.text, label: label} }

// matches returns the first glob that matches abs, or "".
func (r rule) matches(abs string) string {
	rel, err := filepath.Rel(r.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	rel = filepath.ToSlash(rel)
	for _, g := range r.meta.globs {
		if globMatch(g, rel) {
			return g
		}
	}
	return ""
}

func kindOf(file string, m ruleMeta) ruleKind {
	switch {
	case m.always:
		return ruleAlways
	case len(m.globs) > 0:
		return ruleGlob
	case m.description != "":
		return ruleDescription
	case strings.HasSuffix(file, ".mdc"):
		return ruleManual
	default:
		return ruleAlways
	}
}

// rules reads every file the profile's Rules entries name, for each root.
// An entry is a directory, read recursively for .md and .mdc files, or a
// single file such as .cursorrules. Files are taken in lexical order.
func (l *loader) rules() []rule {
	roots := l.roots
	if l.p.PrimaryOnly && len(roots) > 1 {
		roots = roots[:1]
	}
	var out []rule
	seen := map[string]bool{}
	for _, root := range roots {
		for _, entry := range l.p.Rules {
			for _, f := range ruleFiles(filepath.Join(root, filepath.FromSlash(entry))) {
				if seen[f] {
					continue
				}
				seen[f] = true
				raw := l.r.read(f)
				if raw == "" {
					continue
				}
				meta, body := parseRuleFrontmatter(raw)
				if l.p.StripComments {
					body = strings.TrimSpace(stripComments(body))
				}
				if body == "" {
					continue
				}
				out = append(out, rule{path: f, root: root, meta: meta, kind: kindOf(f, meta), text: body})
			}
		}
	}
	return out
}

func ruleFiles(p string) []string {
	var files []string
	_ = filepath.WalkDir(p, func(f string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if f == p || strings.HasSuffix(f, ".md") || strings.HasSuffix(f, ".mdc") {
			files = append(files, f)
		}
		return nil
	})
	return files
}

// parseRuleFrontmatter splits a rule into its trigger fields and its body.
// It reads keys line by line rather than as YAML, because Cursor writes
// "globs: *.ts", and YAML reads a leading * as an alias. Values may be a
// comma-separated string, a [flow, list] or "- item" lines. Unknown keys are
// ignored. A file without a leading "---" line has no frontmatter.
func parseRuleFrontmatter(src string) (ruleMeta, string) {
	var m ruleMeta
	s := strings.TrimPrefix(src, "\uFEFF")
	first, rest, ok := strings.Cut(s, "\n")
	if !ok || strings.TrimSpace(first) != "---" {
		return m, strings.TrimSpace(src)
	}
	lines := strings.Split(rest, "\n")
	end := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return m, strings.TrimSpace(src)
	}
	key := ""
	for _, line := range lines[:end] {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "- ") {
			if key == "globs" || key == "paths" {
				if v := unquote(strings.TrimSpace(t[2:])); v != "" {
					m.globs = append(m.globs, v)
				}
			}
			continue
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		key, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch key {
		case "alwaysApply":
			m.always = strings.EqualFold(v, "true")
		case "description":
			m.description = unquote(v)
		case "globs", "paths":
			m.globs = append(m.globs, splitList(v)...)
		}
	}
	return m, strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
}

func splitList(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = unquote(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// globMatch matches a slash-separated path relative to the root. "**" spans
// any number of directories, a trailing "/" means everything under that
// directory, and a glob with no "/" matches the file name at any depth, as
// "*.ts" does in Cursor.
func globMatch(glob, rel string) bool {
	glob = strings.TrimPrefix(glob, "./")
	if strings.HasSuffix(glob, "/") {
		glob += "**"
	}
	if !strings.Contains(glob, "/") {
		ok, _ := path.Match(glob, path.Base(rel))
		return ok
	}
	return matchSegments(strings.Split(glob, "/"), strings.Split(rel, "/"))
}

func matchSegments(g, p []string) bool {
	for len(g) > 0 {
		if g[0] == "**" {
			if len(g) == 1 {
				return true
			}
			for i := 0; i <= len(p); i++ {
				if matchSegments(g[1:], p[i:]) {
					return true
				}
			}
			return false
		}
		if len(p) == 0 {
			return false
		}
		if ok, _ := path.Match(g[0], p[0]); !ok {
			return false
		}
		g, p = g[1:], p[1:]
	}
	return len(p) == 0
}
