package agentsmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// tree writes files under a fresh directory and returns it, with symlinks
// resolved so paths compare equal to what the extension reports.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func preset(t *testing.T, name string) Profile {
	t.Helper()
	p, err := Preset(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newExt(t *testing.T, p Profile, home string, roots ...string) *Extension {
	t.Helper()
	if home == "" {
		home = t.TempDir()
	}
	e, err := New(Config{Roots: roots, Profile: p, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func prompt(e *Extension) string {
	return e.PromptSections()[0].Section(context.Background())
}

// block returns what the step stage adds before a model call, or "".
func block(t *testing.T, e *Extension) string {
	t.Helper()
	stages := e.StepStages()
	if len(stages) == 0 {
		return ""
	}
	in := []agent.Message{{Role: agent.RoleUser, Text: "hi"}}
	v, err := stages[0].Run(context.Background(), agent.StepInfo{Step: 1}, agent.StepView{Messages: in})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Messages) == len(in) {
		return ""
	}
	return v.Messages[len(v.Messages)-1].Text
}

// call runs a tool call naming args through the extension's middleware.
func call(t *testing.T, e *Extension, args map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(args)
	info := agent.ToolCallInfo{Call: agent.ToolCall{Name: "read_file", Args: core.NewRawJSON(raw)}}
	for _, mw := range e.Middleware() {
		if _, err := mw(context.Background(), info, func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
			return &core.ToolResult{}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// inOrder fails unless each want appears in text, in the order given.
func inOrder(t *testing.T, label, text string, want ...string) {
	t.Helper()
	at := -1
	for _, w := range want {
		i := strings.Index(text, w)
		if i < 0 {
			t.Errorf("%s: missing %q in:\n%s", label, w, text)
			return
		}
		if i < at {
			t.Errorf("%s: %q is out of order in:\n%s", label, w, text)
		}
		at = i
	}
}

func absent(t *testing.T, label, text string, not ...string) {
	t.Helper()
	for _, n := range not {
		if strings.Contains(text, n) {
			t.Errorf("%s: %q should not be loaded:\n%s", label, n, text)
		}
	}
}

// TestPresetsLoadTheirOwnFiles runs every preset over one tree holding each
// tool's files, and checks each loads exactly its own, outermost first.
func TestPresetsLoadTheirOwnFiles(t *testing.T) {
	dir := tree(t, map[string]string{
		"home/.codex/AGENTS.md":             "codex-global",
		"home/.claude/CLAUDE.md":            "claude-global",
		"outer/CLAUDE.md":                   "outer-claude",
		"outer/AGENTS.md":                   "outer-agents",
		"outer/repo/.git/":                  "",
		"outer/repo/AGENTS.md":              "repo-agents",
		"outer/repo/CLAUDE.md":              "repo-claude",
		"outer/repo/CLAUDE.local.md":        "repo-local",
		"outer/repo/app/AGENTS.override.md": "app-override",
		"outer/repo/app/AGENTS.md":          "app-agents",
		"outer/repo/app/CLAUDE.md":          "app-claude",
	})
	root := filepath.Join(dir, "outer/repo/app")
	home := filepath.Join(dir, "home")
	cases := []struct {
		preset string
		want   []string
		not    []string
	}{
		{"codex", []string{"codex-global", "repo-agents", "app-override"},
			[]string{"claude-global", "outer-", "repo-claude", "repo-local", "app-agents", "app-claude"}},
		{"claude", []string{"claude-global", "outer-claude", "repo-claude", "repo-local", "app-claude"},
			[]string{"codex-global", "agents", "app-override"}},
		{"chakra", []string{"repo-agents", "app-override"},
			[]string{"-global", "outer-", "repo-claude", "repo-local", "app-agents", "app-claude"}},
	}
	for _, c := range cases {
		got := prompt(newExt(t, preset(t, c.preset), home, root))
		inOrder(t, c.preset, got, c.want...)
		absent(t, c.preset, got, c.not...)
	}
}

func TestEachFileIsLabelledWithItsPathAndRanked(t *testing.T) {
	dir := tree(t, map[string]string{".git/": "", "AGENTS.md": "be brief"})
	got := prompt(newExt(t, preset(t, "chakra"), "", dir))
	inOrder(t, "prompt", got, "# Project instructions", "can't change which actions need the user's approval", "## "+filepath.Join(dir, "AGENTS.md"), "be brief")
}

func TestEmptyFileFallsThroughToTheNextName(t *testing.T) {
	dir := tree(t, map[string]string{".git/": "", "AGENTS.override.md": " \n", "AGENTS.md": "real"})
	inOrder(t, "prompt", prompt(newExt(t, preset(t, "codex"), "", dir)), "real")
}

func TestNoMarkerReadsOnlyTheRoot(t *testing.T) {
	dir := tree(t, map[string]string{"AGENTS.md": "parent", "ws/AGENTS.md": "root"})
	got := prompt(newExt(t, preset(t, "chakra"), "", filepath.Join(dir, "ws")))
	inOrder(t, "prompt", got, "root")
	absent(t, "prompt", got, "parent")

	if got := prompt(newExt(t, preset(t, "chakra"), "", filepath.Join(dir, "ws", "missing-dir-is-fine"))); got != "" {
		t.Errorf("a root with no files produced %q", got)
	}
}

func TestByteCapCutsThenSkips(t *testing.T) {
	dir := tree(t, map[string]string{
		".git/":         "",
		"AGENTS.md":     strings.Repeat("a", 60),
		"x/AGENTS.md":   strings.Repeat("b", 60),
		"x/y/AGENTS.md": "never",
	})
	p := preset(t, "chakra")
	p.MaxBytes = 100
	got := prompt(newExt(t, p, "", filepath.Join(dir, "x/y")))
	if !strings.Contains(got, strings.Repeat("b", 40)+"\n\n[cut here") || strings.Contains(got, strings.Repeat("b", 41)) {
		t.Errorf("second file not cut at the cap:\n%s", got)
	}
	inOrder(t, "prompt", got, "Not loaded, over the size limit", filepath.Join(dir, "x/y/AGENTS.md"))
	absent(t, "prompt", got, "never")
}

func TestRootsEachGetAChainUnlessPrimaryOnly(t *testing.T) {
	a := tree(t, map[string]string{".git/": "", "AGENTS.md": "repo-a"})
	b := tree(t, map[string]string{".git/": "", "AGENTS.md": "repo-b"})
	inOrder(t, "chakra", prompt(newExt(t, preset(t, "chakra"), "", a, b)), "repo-a", "repo-b")
	got := prompt(newExt(t, preset(t, "codex"), "", a, b))
	inOrder(t, "codex", got, "repo-a")
	absent(t, "codex", got, "repo-b")
}

func TestClaudeReadsAgentsOnlyWithoutClaudeFiles(t *testing.T) {
	only := tree(t, map[string]string{".git/": "", "AGENTS.md": "agents"})
	inOrder(t, "agents only", prompt(newExt(t, preset(t, "claude"), "", only)), "agents")

	both := tree(t, map[string]string{"AGENTS.md": "agents", "sub/CLAUDE.local.md": "local"})
	got := prompt(newExt(t, preset(t, "claude"), "", filepath.Join(both, "sub")))
	inOrder(t, "with a CLAUDE.local.md", got, "local")
	absent(t, "with a CLAUDE.local.md", got, "agents")
}

func TestImportsAndComments(t *testing.T) {
	dir := tree(t, map[string]string{
		".git/": "",
		"CLAUDE.md": "<!-- maintainers only -->\nsee @docs/style.md and `@docs/span.md`\n" +
			"```\n@docs/fenced.md\n<!-- kept in code -->\n```\nalso @../secret.md and @docs/my\\ notes.md",
		"docs/style.md":    "style @deep1.md",
		"docs/span.md":     "SPAN",
		"docs/fenced.md":   "FENCED",
		"docs/my notes.md": "spaced",
		"docs/deep1.md":    "d1 @deep2.md",
		"docs/deep2.md":    "d2 @deep3.md",
		"docs/deep3.md":    "d3 @deep4.md",
		"docs/deep4.md":    "d4 @deep5.md",
		"docs/deep5.md":    "TOO-DEEP",
	})
	write(t, filepath.Join(filepath.Dir(dir), "secret.md"), "SECRET")
	t.Cleanup(func() { os.Remove(filepath.Join(filepath.Dir(dir), "secret.md")) })

	got := prompt(newExt(t, preset(t, "claude"), "", dir))
	inOrder(t, "imports", got, "see @docs/style.md", "<!-- kept in code -->",
		filepath.Join(dir, "docs/style.md")+" (imported by "+filepath.Join(dir, "CLAUDE.md")+")",
		"d1", "d2", "d3 @deep4.md", "spaced")
	// CLAUDE.md -> style -> deep1 -> deep2 -> deep3 is four hops; deep4 is a fifth.
	absent(t, "imports", got, "maintainers only", "SPAN", "FENCED", "d4 @", "SECRET")

	home := tree(t, map[string]string{".claude/CLAUDE.md": "@~/notes.md", "notes.md": "HOME-NOTES"})
	inOrder(t, "global import", prompt(newExt(t, preset(t, "claude"), home, dir)), "HOME-NOTES")
}

func TestSubdirectoryFilesLoadOnceTouched(t *testing.T) {
	dir := tree(t, map[string]string{
		".git/":           "",
		"AGENTS.md":       "root-rules",
		"a/AGENTS.md":     "a-rules",
		"a/b/AGENTS.md":   "b-rules",
		"a/b/c.go":        "package b",
		"other/AGENTS.md": "other-rules",
	})
	outside := tree(t, map[string]string{"AGENTS.md": "outside-rules"})
	e := newExt(t, preset(t, "chakra"), "", dir)
	absent(t, "prompt", prompt(e), "a-rules", "b-rules")
	if got := block(t, e); got != "" {
		t.Fatalf("block before any tool call: %q", got)
	}
	call(t, e, map[string]any{"path": "a/b/c.go"})
	call(t, e, map[string]any{"path": filepath.Join(outside, "AGENTS.md")})
	got := block(t, e)
	inOrder(t, "block", got, "Project instructions found or changed", "a-rules", "b-rules")
	inOrder(t, "block", got, "(applies within "+filepath.Join(dir, "a/b")+")")
	absent(t, "block", got, "root-rules", "other-rules", "outside-rules")

	call(t, e, map[string]any{"workdir": "other"})
	inOrder(t, "after workdir", block(t, e), "other-rules")

	if mw := newExt(t, preset(t, "codex"), "", dir).Middleware(); mw != nil {
		t.Error("codex reads no subdirectories, but registered middleware")
	}
}

func TestNoticeShowsChangesAndKeepsThePromptFixed(t *testing.T) {
	dir := tree(t, map[string]string{".git/": "", "AGENTS.md": "v1", "a/AGENTS.md": "a1"})
	e := newExt(t, preset(t, "chakra"), "", dir)
	first := prompt(e)
	call(t, e, map[string]any{"path": "a"})

	write(t, filepath.Join(dir, "AGENTS.md"), "v2")
	write(t, filepath.Join(dir, "a/AGENTS.md"), "a2")
	if prompt(e) != first {
		t.Error("the system prompt changed after an edit")
	}
	got := block(t, e)
	inOrder(t, "changed", got, "v2", "a2")
	inOrder(t, "changed", got, "changed during this session; this version replaces the one in the system prompt")
	absent(t, "changed", got, "v1", "a1")

	write(t, filepath.Join(dir, "AGENTS.override.md"), "ov")
	got = block(t, e)
	inOrder(t, "override", got, "ov", "new since the system prompt was written")
	inOrder(t, "override", got, "## "+filepath.Join(dir, "AGENTS.md")+" no longer applies")

	os.Remove(filepath.Join(dir, "AGENTS.override.md"))
	os.Remove(filepath.Join(dir, "AGENTS.md"))
	got = block(t, e)
	inOrder(t, "deleted", got, filepath.Join(dir, "AGENTS.md")+" no longer applies")
}

func TestFreezeIgnoresChanges(t *testing.T) {
	dir := tree(t, map[string]string{".git/": "", "AGENTS.md": "v1", "a/AGENTS.md": "a1"})
	p := preset(t, "chakra")
	p.Changes = ChangesFreeze
	e := newExt(t, p, "", dir)
	prompt(e)
	call(t, e, map[string]any{"path": "a"})
	write(t, filepath.Join(dir, "AGENTS.md"), "v2")
	write(t, filepath.Join(dir, "a/AGENTS.md"), "a2")
	call(t, e, map[string]any{"path": "a/x.go"}) // a second visit doesn't re-read
	got := block(t, e)
	inOrder(t, "frozen", got, "a1")
	absent(t, "frozen", got, "v2", "a2")
}

func TestBlockSharesTheCapWithThePrompt(t *testing.T) {
	dir := tree(t, map[string]string{".git/": "", "AGENTS.md": strings.Repeat("r", 90), "a/AGENTS.md": strings.Repeat("s", 30)})
	p := preset(t, "chakra")
	p.MaxBytes = 100
	e := newExt(t, p, "", dir)
	prompt(e)
	call(t, e, map[string]any{"path": "a"})
	got := block(t, e)
	if !strings.Contains(got, strings.Repeat("s", 10)+"\n\n[cut here") || strings.Contains(got, strings.Repeat("s", 11)) {
		t.Errorf("block didn't get the remaining 10 bytes:\n%s", got)
	}
}

func TestBadProfilesAreRefused(t *testing.T) {
	if _, err := Preset("cursor"); err == nil {
		t.Error("unknown preset accepted")
	}
	for _, mut := range []func(*Profile){
		func(p *Profile) { p.Names, p.Override = nil, "" },
		func(p *Profile) { p.Walk = "up" },
		func(p *Profile) { p.Subdirs = "eager" },
		func(p *Profile) { p.Changes = "live" },
	} {
		p := preset(t, "chakra")
		mut(&p)
		if _, err := New(Config{Roots: []string{t.TempDir()}, Profile: p}); err == nil {
			t.Errorf("bad profile %+v accepted", p)
		}
	}
	if _, err := New(Config{Profile: preset(t, "chakra")}); err == nil {
		t.Error("no roots accepted")
	}
}

func TestPresetReturnsACopy(t *testing.T) {
	p := preset(t, "codex")
	p.Names[0] = "changed"
	if Presets["codex"].Names[0] != "AGENTS.md" {
		t.Error("changing a preset's copy changed Presets")
	}
}

// TestAnnouncesWhatTheModelIsFollowing checks the lines a user sees: what
// loaded at startup, a subdirectory file once found, and each change once.
func TestAnnouncesWhatTheModelIsFollowing(t *testing.T) {
	dir := tree(t, map[string]string{".git/": "", "AGENTS.md": "v1", "a/AGENTS.md": "a1"})
	e := newExt(t, preset(t, "chakra"), "", dir)
	var lines []string
	e.SetAnnounce(func(s string) { lines = append(lines, s) })
	expect := func(want ...string) {
		t.Helper()
		if strings.Join(lines, "\n") != strings.Join(want, "\n") {
			t.Errorf("announced %q, want %q", lines, want)
		}
		lines = nil
	}

	prompt(e)
	prompt(e)
	expect("Instruction files loaded: AGENTS.md.")
	call(t, e, map[string]any{"path": "a/x.go"})
	expect("Instruction files found in a: " + filepath.Join("a", "AGENTS.md") + ".")
	block(t, e)
	block(t, e)
	expect()

	write(t, filepath.Join(dir, "AGENTS.md"), "v2")
	write(t, filepath.Join(dir, "a/AGENTS.override.md"), "ov")
	block(t, e)
	block(t, e)
	expect("Instruction files changed during this session: AGENTS.md.",
		"Instruction files added during this session: "+filepath.Join("a", "AGENTS.override.md")+".",
		"Instruction files replaced or deleted during this session: "+filepath.Join("a", "AGENTS.md")+".")

	empty := newExt(t, preset(t, "chakra"), "", t.TempDir())
	empty.SetAnnounce(func(s string) { lines = append(lines, s) })
	prompt(empty)
	expect("No instruction files found.")
}
