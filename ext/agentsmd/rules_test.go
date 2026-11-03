package agentsmd

import (
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
