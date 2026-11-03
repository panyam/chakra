package surfaces

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

func dirsOf(ds []host.SkillDir) string {
	var parts []string
	for _, d := range ds {
		s := d.Label + "=" + d.Path
		if d.Own {
			s += " (own)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " | ")
}

func TestWorkspaceSkillDirsDefaultsAndPresets(t *testing.T) {
	a, b := "/w/a", "/w/b"
	cases := []struct {
		cfg  WorkspaceConfig
		want string
	}{
		{WorkspaceConfig{Roots: []string{a}}, ".agents/skills=/w/a/.agents/skills | .claude/skills=/w/a/.claude/skills | ~/.agents/skills=~/.agents/skills (own)"},
		{WorkspaceConfig{Roots: []string{a}, SkillsPreset: "codex"}, ".agents/skills=/w/a/.agents/skills | ~/.agents/skills=~/.agents/skills (own)"},
		{WorkspaceConfig{Roots: []string{a}, SkillsPreset: "claude"}, ".claude/skills=/w/a/.claude/skills | ~/.claude/skills=~/.claude/skills (own)"},
		{WorkspaceConfig{Roots: []string{a, b}, SkillDirs: []string{"skills", "/opt/team-skills"}}, "a/skills=/w/a/skills | b/skills=/w/b/skills | /opt/team-skills=/opt/team-skills"},
		{WorkspaceConfig{Roots: []string{a}, SkillDirs: []string{}}, ""},
		{WorkspaceConfig{}, ""},
	}
	for _, c := range cases {
		got, err := WorkspaceSkillDirs(c.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if dirsOf(got) != c.want {
			t.Errorf("%+v:\n got  %s\n want %s", c.cfg, dirsOf(got), c.want)
		}
	}
	if _, err := WorkspaceSkillDirs(WorkspaceConfig{Roots: []string{a}, SkillsPreset: "cursor"}); err == nil {
		t.Error("an unknown preset was accepted")
	}
}

func TestWorkspaceFileSkills(t *testing.T) {
	var cfg WorkspaceConfig
	if err := cfg.LoadFile(writeWorkspaceFile(t, `{"skills": {"preset": "claude", "dirs": ["tools/skills"]}}`)); err != nil {
		t.Fatal(err)
	}
	if cfg.SkillsPreset != "claude" || len(cfg.SkillDirs) != 1 || cfg.SkillDirs[0] != "tools/skills" {
		t.Errorf("cfg = %+v", cfg)
	}
	cfg = WorkspaceConfig{}
	if err := cfg.LoadFile(writeWorkspaceFile(t, `{"skills": {"dirs": []}}`)); err != nil {
		t.Fatal(err)
	}
	if cfg.SkillDirs == nil || len(cfg.SkillDirs) != 0 {
		t.Errorf("an empty dirs list should mean none, got %#v", cfg.SkillDirs)
	}
	for _, bad := range []string{`{"skills": {"preset": "cursor"}}`, `{"skills": {"folders": ["x"]}}`} {
		if err := (&WorkspaceConfig{}).LoadFile(writeWorkspaceFile(t, bad)); err == nil {
			t.Errorf("%s loaded", bad)
		}
	}
}

type loadedLog struct {
	mu sync.Mutex
	n  map[string]int
}

func (l *loadedLog) On(ev host.HostEvent) {
	if ev.Kind == host.HostSkillsLoaded {
		l.mu.Lock()
		l.n[ev.ServerID] += ev.Loaded
		l.mu.Unlock()
	}
}

// TestWorkspaceSkillsReachAnApp is the agentchat --workspace path: a skill in
// the workspace's .agents/skills joins the catalog of an App built with
// WorkspaceSkillDirs.
func TestWorkspaceSkillsReachAnApp(t *testing.T) {
	ws := t.TempDir()
	p := filepath.Join(ws, ".agents", "skills", "go-testing", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: go-testing\ndescription: Run the tests.\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs, err := WorkspaceSkillDirs(WorkspaceConfig{Roots: []string{ws}, SkillDirs: []string{".agents/skills"}})
	if err != nil {
		t.Fatal(err)
	}
	log := &loadedLog{n: map[string]int{}}
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"}, Instructions: "test"}
	var out bytes.Buffer
	app, err := host.NewApp(cfg, &out, strings.NewReader(""), host.WithProvider(agent.NewStubProvider()), host.WithSkillDirs(dirs...), host.WithObserver(log))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.n[".agents/skills"] != 1 {
		t.Errorf("skills loaded by source = %v", log.n)
	}
}
