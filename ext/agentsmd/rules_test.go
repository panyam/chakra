package agentsmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRuleFrontmatterReadsCursorsLooseForm(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      ruleMeta
	}{
		{"unquoted star glob, which YAML would read as an alias", "---\nglobs: *.ts, src/**/*.tsx\nalwaysApply: false\n---\nbody",
			ruleMeta{globs: []string{"*.ts", "src/**/*.tsx"}}},
		{"YAML list of paths", "---\npaths:\n  - \"src/api/**\"\n  - lib/*.go\n---\nbody",
			ruleMeta{globs: []string{"src/api/**", "lib/*.go"}}},
		{"flow list and quoted description", "---\ndescription: \"When writing SQL\"\nglobs: [\"db/**\", 'migrations/*']\n---\nbody",
			ruleMeta{description: "When writing SQL", globs: []string{"db/**", "migrations/*"}}},
		{"always", "---\nalwaysApply: true\n---\nbody", ruleMeta{always: true}},
		{"no frontmatter", "just a body", ruleMeta{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, body := parseRuleFrontmatter(c.src)
			if got.always != c.want.always || got.description != c.want.description || !slices.Equal(got.globs, c.want.globs) {
				t.Errorf("meta = %+v, want %+v", got, c.want)
			}
			if !strings.HasSuffix(body, "body") || strings.Contains(body, "---") {
				t.Errorf("body = %q", body)
			}
		})
	}
}

func TestRuleGlobMatch(t *testing.T) {
	for _, c := range []struct {
		glob, path string
		want       bool
	}{
		{"*.ts", "a.ts", true},
		{"*.ts", "src/deep/a.ts", true},
		{"src/**/*.ts", "src/a.ts", true},
		{"src/**/*.ts", "src/x/y/a.ts", true},
		{"src/**/*.ts", "lib/a.ts", false},
		{"src/api/**", "src/api/v1/h.go", true},
		{"src/api/**", "src/apix/h.go", false},
		{"src/*.go", "src/x/a.go", false},
		{"docs/", "docs/a.md", true},
	} {
		if got := globMatch(c.glob, c.path); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func cursorTree(t *testing.T) string {
	return tree(t, map[string]string{
		".git/":                   "",
		"AGENTS.md":               "Agents file.",
		".cursorrules":            "Legacy cursor rules.",
		".cursor/rules/style.mdc": "---\nalwaysApply: true\n---\nAlways use tabs.",
		".cursor/rules/ts.mdc":    "---\nglobs: src/**/*.ts, *.tsx\nalwaysApply: false\n---\nPrefer interfaces over types.",
		".cursor/rules/sql.mdc":   "---\ndescription: When writing database migrations\nalwaysApply: false\n---\nMigrations are append only.",
		".cursor/rules/rare.mdc":  "---\nalwaysApply: false\n---\nOnly when mentioned.",
		"src/app/main.ts":         "",
	})
}

func TestCursorPresetLoadsAgentsMdAndEachRuleKind(t *testing.T) {
	root := cursorTree(t)
	e := newExt(t, preset(t, "cursor"), "", root)
	p := prompt(e)
	inOrder(t, "prompt", p, "Agents file.", "Legacy cursor rules.", "Always use tabs.", "When writing database migrations", ".cursor/rules/sql.mdc")
	for _, absent := range []string{"Prefer interfaces", "Migrations are append only", "Only when mentioned"} {
		if strings.Contains(p, absent) {
			t.Errorf("prompt has %q, which its rule shouldn't load at startup", absent)
		}
	}
	if b := block(t, e); strings.Contains(b, "Prefer interfaces") {
		t.Errorf("a glob rule applied before any path matched:\n%s", b)
	}

	call(t, e, map[string]any{"path": "src/app/main.ts"})
	b := block(t, e)
	if !strings.Contains(b, "Prefer interfaces over types.") || !strings.Contains(b, "src/app/main.ts matches src/**/*.ts") {
		t.Errorf("after touching src/app/main.ts, block =\n%s", b)
	}
	if strings.Count(b, "Prefer interfaces") != 1 {
		t.Errorf("rule shown more than once:\n%s", b)
	}
	call(t, e, map[string]any{"path": "web/x.tsx"})
	if b := block(t, e); strings.Count(b, "Prefer interfaces") != 1 {
		t.Errorf("a second match repeated the rule:\n%s", b)
	}
}

func TestClaudePresetReadsClaudeRules(t *testing.T) {
	root := tree(t, map[string]string{
		"CLAUDE.md":                    "Claude file.",
		".claude/rules/general.md":     "Write short functions.",
		".claude/rules/frontend/ui.md": "---\npaths:\n  - \"web/**/*.tsx\"\n---\nUse the design tokens.",
		"web/src/App.tsx":              "",
	})
	e := newExt(t, preset(t, "claude"), "", root)
	p := prompt(e)
	inOrder(t, "prompt", p, "Claude file.", "Write short functions.")
	if strings.Contains(p, "design tokens") {
		t.Errorf("a paths: rule loaded at startup:\n%s", p)
	}
	call(t, e, map[string]any{"path": "web/src/App.tsx"})
	if b := block(t, e); !strings.Contains(b, "Use the design tokens.") {
		t.Errorf("after touching web/src/App.tsx, block =\n%s", b)
	}
}

func TestProfileCanNameItsOwnRulesDirectory(t *testing.T) {
	root := tree(t, map[string]string{
		"AGENTS.md":         "Agents file.",
		"rules/always.md":   "House style.",
		"rules/go.md":       "---\nglobs: \"**/*.go\"\n---\nRun gofmt.",
		"rules/release.mdc": "---\ndescription: Cutting a release\n---\nTag from main.",
	})
	p := preset(t, "chakra")
	p.Rules = []string{"rules"}
	e := newExt(t, p, "", root)
	inOrder(t, "prompt", prompt(e), "Agents file.", "House style.", "Cutting a release")
	call(t, e, map[string]any{"path": "cmd/main.go"})
	if b := block(t, e); !strings.Contains(b, "Run gofmt.") {
		t.Errorf("block =\n%s", b)
	}
}

// A rules-only profile is valid, and its glob rules still need the
// middleware even with subdirectories off.
func TestGlobRulesWorkWithSubdirsOff(t *testing.T) {
	root := tree(t, map[string]string{"rules/go.md": "---\nglobs: \"*.go\"\n---\nRun gofmt."})
	e := newExt(t, Profile{Rules: []string{"rules"}, Walk: WalkMarker, Subdirs: SubdirsOff, Changes: ChangesFreeze}, "", root)
	call(t, e, map[string]any{"path": "main.go"})
	if b := block(t, e); !strings.Contains(b, "Run gofmt.") {
		t.Errorf("block =\n%s", b)
	}
}

func transform(t *testing.T, e *Extension, input string) {
	t.Helper()
	for _, fn := range e.InputTransforms() {
		got, err := fn(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if got != input {
			t.Errorf("the transform changed the message to %q; a mentioned rule belongs in the block, not in history", got)
		}
	}
}

func TestManualRuleAppliesOnceMentioned(t *testing.T) {
	e := newExt(t, preset(t, "cursor"), "", cursorTree(t))
	var said []string
	e.SetAnnounce(func(l string) { said = append(said, l) })
	prompt(e)
	transform(t, e, "mail me at someone@rare.com")
	absent(t, "after an email address", block(t, e), "Only when mentioned.")

	transform(t, e, "fix this, and follow @rare and @style.mdc")
	b := block(t, e)
	inOrder(t, "block", b, "rare.mdc (rule, applies since you mentioned @rare)", "Only when mentioned.")
	if strings.Contains(b, "Always use tabs.") {
		t.Errorf("mentioning an always rule repeated it in the block:\n%s", b)
	}
	transform(t, e, "@rare again")
	if b := block(t, e); strings.Count(b, "Only when mentioned.") != 1 {
		t.Errorf("a second mention repeated the rule:\n%s", b)
	}
	inOrder(t, "announcements", strings.Join(said, "\n"), "applied when you write @name: .cursor/rules/rare.mdc", "Rule .cursor/rules/rare.mdc applies, since you mentioned @rare.")
}

func TestGlobalRulesComeFromHome(t *testing.T) {
	home := tree(t, map[string]string{
		".claude/rules/mine.md": "My own rule.",
		".claude/rules/go.md":   "---\npaths: \"**/*.go\"\n---\nRun gofmt everywhere.",
	})
	root := tree(t, map[string]string{"CLAUDE.md": "Claude file.", ".claude/rules/proj.md": "Project rule."})
	e := newExt(t, preset(t, "claude"), home, root)
	inOrder(t, "prompt", prompt(e), "Claude file.", "mine.md (your rule, applies always)", "My own rule.", "Project rule.")
	absent(t, "prompt", prompt(e), "Run gofmt everywhere.")
	call(t, e, map[string]any{"path": "cmd/x/main.go"})
	inOrder(t, "block", block(t, e), "cmd/x/main.go matches **/*.go", "Run gofmt everywhere.")
}

func TestNestedRulesApplyWithinTheirDirectory(t *testing.T) {
	root := tree(t, map[string]string{
		".git/":                         "",
		"AGENTS.md":                     "Agents file.",
		"pkg/.cursor/rules/local.mdc":   "---\nalwaysApply: true\n---\nPkg rule.",
		"pkg/.cursor/rules/lib.mdc":     "---\nglobs: lib/*.ts\n---\nPkg lib rule.",
		"pkg/.cursor/rules/release.mdc": "---\ndescription: Releasing pkg\n---\nBump the version.",
	})
	e := newExt(t, preset(t, "cursor"), "", root)
	absent(t, "prompt", prompt(e), "Pkg rule.", "Releasing pkg")
	call(t, e, map[string]any{"path": "lib/a.ts"})
	absent(t, "before pkg is touched", block(t, e), "Pkg rule.", "Pkg lib rule.")

	call(t, e, map[string]any{"path": "pkg/main.go"})
	b := block(t, e)
	inOrder(t, "block", b, "local.mdc (rule, applies within "+filepath.Join(root, "pkg")+")", "Pkg rule.", "Releasing pkg")
	absent(t, "block", b, "Pkg lib rule.")

	call(t, e, map[string]any{"path": "pkg/lib/b.ts"})
	inOrder(t, "block", block(t, e), "pkg/lib/b.ts matches lib/*.ts", "Pkg lib rule.")
}

func TestNestedRulesNeedLazySubdirs(t *testing.T) {
	p := preset(t, "cursor")
	p.Subdirs = SubdirsOff
	if _, err := New(Config{Roots: []string{t.TempDir()}, Profile: p}); err == nil || !strings.Contains(err.Error(), "nestedRules") {
		t.Errorf("err = %v, want a refusal naming nestedRules", err)
	}
}

func TestNoticeShowsRuleEdits(t *testing.T) {
	root := tree(t, map[string]string{
		".git/":           "",
		"AGENTS.md":       "Agents file.",
		"rules/always.md": "Style v1.",
		"rules/go.md":     "---\nglobs: \"*.go\"\n---\nGofmt v1.",
	})
	p := preset(t, "chakra")
	p.Rules = []string{"rules"}
	e := newExt(t, p, "", root)
	var said []string
	e.SetAnnounce(func(l string) { said = append(said, l) })
	first := prompt(e)
	call(t, e, map[string]any{"path": "main.go"})

	write(t, filepath.Join(root, "rules/always.md"), "Style v2.")
	write(t, filepath.Join(root, "rules/go.md"), "---\nglobs: \"*.go\"\n---\nGofmt v2.")
	write(t, filepath.Join(root, "rules/new.md"), "Fresh rule.")
	if prompt(e) != first {
		t.Error("the system prompt changed after a rule edit")
	}
	b := block(t, e)
	inOrder(t, "edited", b, "Style v2.", "Fresh rule.", "Gofmt v2.")
	inOrder(t, "edited", b, "always.md (rule, applies always; changed during this session; this version replaces the one in the system prompt)")
	inOrder(t, "edited", b, "new.md (rule, applies always; new since the system prompt was written)")
	absent(t, "edited", b, "Style v1.", "Gofmt v1.")
	inOrder(t, "announcements", strings.Join(said, "\n"), "Rules changed during this session: rules/always.md, rules/go.md.", "Rules added during this session: rules/new.md.")

	os.Remove(filepath.Join(root, "rules/always.md"))
	inOrder(t, "deleted", block(t, e), filepath.Join(root, "rules/always.md")+" no longer applies")
}

func TestFreezeKeepsRulesAsFirstRead(t *testing.T) {
	root := cursorTree(t)
	e := newExt(t, preset(t, "cursor"), "", root)
	prompt(e)
	call(t, e, map[string]any{"path": "src/app/main.ts"})
	write(t, filepath.Join(root, ".cursor/rules/style.mdc"), "---\nalwaysApply: true\n---\nUse spaces now.")
	write(t, filepath.Join(root, ".cursor/rules/ts.mdc"), "---\nglobs: src/**/*.ts\n---\nTypes are fine now.")
	b := block(t, e)
	absent(t, "block", b, "Use spaces now.", "Types are fine now.")
	inOrder(t, "block", b, "Prefer interfaces over types.")
}
