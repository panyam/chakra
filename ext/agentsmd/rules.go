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
//     once the user writes @name, where name is the file name without its
//     extension (#240). Any rule not already loaded can be mentioned.
//
// Rule folders come from three places. Profile.Rules are relative to each
// workspace root. Profile.GlobalRules are the user's own, such as
// ~/.claude/rules, and their globs match paths under any root.
// Profile.NestedRules are read inside each subdirectory the agent touches,
// as Cursor reads .cursor/rules there, and their globs are relative to that
// subdirectory.

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
	// root is the directory its globs are relative to: a workspace root, or
	// the subdirectory a nested rule was found in. It is empty for a global
	// rule, whose globs are relative to whichever root holds the path.
	root string
	// within is the subdirectory a nested rule applies in, and empty
	// otherwise.
	within string
	global bool
	meta   ruleMeta
	kind   ruleKind
	text   string // the body, without frontmatter
}

// name is what the user writes after @ to apply the rule.
func (r rule) name() string {
	return strings.TrimSuffix(strings.TrimSuffix(filepath.Base(r.path), ".mdc"), ".md")
}

// same reports whether two reads of a rule say the same thing.
func (r rule) same(o rule) bool {
	return r.kind == o.kind && r.text == o.text && r.meta.description == o.meta.description &&
		strings.Join(r.meta.globs, "\x00") == strings.Join(o.meta.globs, "\x00")
}

// alwaysLabel labels an always rule in the system prompt or the block.
func (r rule) alwaysLabel() string {
	switch {
	case r.within != "":
		return "rule, applies within " + r.within
	case r.global:
		return "your rule, applies always"
	}
	return "rule, applies always"
}

func (r rule) doc(label string) doc { return doc{path: r.path, text: r.text, label: label} }

// matches returns the first glob that matches abs, or "". root is the
// workspace root holding abs, for a global rule.
func (r rule) matches(abs, root string) string {
	if r.root != "" {
		root = r.root
	}
	if root == "" {
		return ""
	}
	rel, err := filepath.Rel(root, abs)
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

// rules reads the rules that apply from the start: the user's GlobalRules,
// then each root's Rules. An entry is a directory, read recursively for .md
// and .mdc files, or a single file such as .cursorrules. Files are taken in
// lexical order.
func (l *loader) rules() []rule {
	roots := l.roots
	if l.p.PrimaryOnly && len(roots) > 1 {
		roots = roots[:1]
	}
	var out []rule
	seen := map[string]bool{}
	for _, entry := range l.p.GlobalRules {
		dir := entry
		if dir == "~" || strings.HasPrefix(dir, "~/") {
			if l.home == "" {
				continue
			}
			dir = filepath.Join(l.home, strings.TrimPrefix(dir, "~"))
		}
		out = l.readRules(out, seen, dir, rule{global: true})
	}
	for _, root := range roots {
		for _, entry := range l.p.Rules {
			out = l.readRules(out, seen, filepath.Join(root, filepath.FromSlash(entry)), rule{root: root})
		}
	}
	return out
}

// nestedRules reads the NestedRules folders inside dir, a subdirectory below
// a root.
func (l *loader) nestedRules(dir string) []rule {
	var out []rule
	seen := map[string]bool{}
	for _, entry := range l.p.NestedRules {
		out = l.readRules(out, seen, filepath.Join(dir, filepath.FromSlash(entry)), rule{root: dir, within: dir})
	}
	return out
}

// readRules appends the rules under p to out, each a copy of base with its
// file's fields filled in.
func (l *loader) readRules(out []rule, seen map[string]bool, p string, base rule) []rule {
	for _, f := range ruleFiles(p) {
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
		r := base
		r.path, r.meta, r.kind, r.text = f, meta, kindOf(f, meta), body
		out = append(out, r)
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
