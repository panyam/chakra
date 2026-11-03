package host

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
	skills "github.com/panyam/mcpkit/ext/skills"
)

func writeSkill(t *testing.T, dir, name, manifest string) {
	t.Helper()
	p := filepath.Join(dir, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillMD(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body + "\n"
}

type skillEvents struct {
	mu     sync.Mutex
	events []HostEvent
}

func (l *skillEvents) On(ev HostEvent) {
	switch ev.Kind {
	case HostSkillsLoaded, HostSkillSkipped, HostSessionWarn:
		l.mu.Lock()
		l.events = append(l.events, ev)
		l.mu.Unlock()
	}
}

func (l *skillEvents) of(kind HostEventKind) []HostEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []HostEvent
	for _, ev := range l.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func skillApp(t *testing.T, dirs ...SkillDir) (*App, *skillEvents) {
	t.Helper()
	ts := startTestServer(t)
	log := &skillEvents{}
	app, err := NewApp(testConfig(ts.URL), io.Discard, strings.NewReader(""),
		WithProvider(agent.NewStubProvider()), WithObserver(log), WithSkillDirs(dirs...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app, log
}

func loadSkill(t *testing.T, app *App, args map[string]any) string {
	t.Helper()
	res, err := app.sources.Call(context.Background(), "load_skill", args)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestSkillsFromAFolderJoinTheCatalog(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "go-testing", skillMD("go-testing", "How to run and read Go tests here.", "Run go test ./... from the root."))
	writeSkill(t, dir, "release", "---\nname: release\ndescription: Cut a release.\nallowed-tools: Bash(git tag *)\n---\nTag it.\n")
	if err := os.MkdirAll(filepath.Join(dir, "not-a-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	app, log := skillApp(t, SkillDir{Path: dir, Label: "project"})

	loaded := log.of(HostSkillsLoaded)
	if len(loaded) != 1 || loaded[0].ServerID != "project" || loaded[0].Loaded != 2 {
		t.Fatalf("skills-loaded events = %+v", loaded)
	}
	section := app.skillsSection(context.Background())
	for _, want := range []string{"repository's skills folder", "(source \"project\")", "- go-testing: How to run", "- release: Cut a release. (asks to use: Bash(git tag *); not granted"} {
		if !strings.Contains(section, want) {
			t.Errorf("catalog lacks %q:\n%s", want, section)
		}
	}
	body := loadSkill(t, app, map[string]any{"name": "go-testing"})
	if !strings.Contains(body, "Run go test ./... from the root.") || !strings.Contains(body, "repository content") {
		t.Errorf("load_skill returned %q", body)
	}
}

func TestMalformedSkillIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "good", skillMD("good", "Fine.", "ok"))
	writeSkill(t, dir, "no-desc", "---\nname: no-desc\n---\nbody\n")
	writeSkill(t, dir, "wrong-name", skillMD("other-name", "Mismatch.", "x"))
	writeSkill(t, dir, "broken", "---\nname: [unclosed\n---\n")
	_, log := skillApp(t, SkillDir{Path: dir, Label: "project"})

	if skipped := log.of(HostSkillSkipped); len(skipped) != 3 {
		t.Fatalf("skipped events = %+v, want the three bad skills", skipped)
	}
	if loaded := log.of(HostSkillsLoaded); len(loaded) != 1 || loaded[0].Loaded != 1 || loaded[0].Skipped != 3 {
		t.Errorf("skills-loaded = %+v, want 1 loaded and 3 skipped", loaded)
	}
}

func TestEditedSkillIsAskedAboutAgain(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "go-testing", skillMD("go-testing", "Tests.", "v1"))
	app, _ := skillApp(t, SkillDir{Path: dir, Label: "project"})
	call := agent.ToolCallInfo{Call: agent.ToolCall{Name: "load_skill", Args: core.NewRawJSON([]byte(`{"name":"go-testing"}`))}}
	before, ok := app.rememberKey(call)
	if !ok {
		t.Fatal("a local skill's approval should be rememberable")
	}

	writeSkill(t, dir, "go-testing", skillMD("go-testing", "Tests.", "v2"))
	if got := loadSkill(t, app, map[string]any{"name": "go-testing"}); !strings.Contains(got, "changed at its source") {
		t.Fatalf("a changed skill was served: %q", got)
	}
	app.loadSkillDir(app.skillDirs[0]) // what the refresh does, synchronously
	after, _ := app.rememberKey(call)
	if before == after {
		t.Error("the approval key didn't change with the skill's content, so the edit wouldn't be asked about")
	}
	if got := loadSkill(t, app, map[string]any{"name": "go-testing"}); !strings.Contains(got, "v2") {
		t.Errorf("after the re-list, load_skill = %q", got)
	}
}

func TestSkillNameSharedWithAServerIsQualified(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "go-testing", skillMD("go-testing", "Local.", "local body"))
	app, _ := skillApp(t, SkillDir{Path: dir, Label: "project"})
	server := skills.SkillEntry{URI: "skill://go-testing/SKILL.md", Frontmatter: map[string]any{"name": "go-testing", "description": "Served."}}
	collisions, _ := app.skills.add("test", "", []catalogSkill{{serverID: "test", entry: server}})
	if len(collisions) != 1 || !strings.Contains(collisions[0], "project") {
		t.Errorf("collisions = %q", collisions)
	}
	if got := loadSkill(t, app, map[string]any{"name": "go-testing"}); !strings.Contains(got, "more than one source") {
		t.Errorf("an ambiguous name was resolved silently: %q", got)
	}
	if got := loadSkill(t, app, map[string]any{"name": "go-testing", "server": "project"}); !strings.Contains(got, "local body") {
		t.Errorf("server=project = %q", got)
	}
}

func TestProjectAndOwnFoldersAndAMissingOne(t *testing.T) {
	project, own := t.TempDir(), t.TempDir()
	writeSkill(t, project, "build", skillMD("build", "Build it.", "b"))
	writeSkill(t, own, "notes", skillMD("notes", "My notes.", "n"))
	app, log := skillApp(t,
		SkillDir{Path: project, Label: "project"},
		SkillDir{Path: own, Label: "user", Own: true},
		SkillDir{Path: filepath.Join(project, "nope"), Label: "absent"},
	)
	section := app.skillsSection(context.Background())
	if !strings.Contains(section, "repository's skills folder") || !strings.Contains(section, "your own skills folder") {
		t.Errorf("each folder should say whose it is:\n%s", section)
	}
	if strings.Index(section, "- build:") > strings.Index(section, "- notes:") {
		t.Error("folders should list in the order given")
	}
	if got := loadSkill(t, app, map[string]any{"name": "notes"}); !strings.Contains(got, "your skills folder") {
		t.Errorf("own skill body = %q", got)
	}
	for _, ev := range log.of(HostSkillsLoaded) {
		if ev.ServerID == "absent" {
			t.Error("a missing folder announced skills")
		}
	}
	if warns := log.of(HostSessionWarn); len(warns) != 0 {
		t.Errorf("warnings: %+v", warns)
	}
}

func TestSkillFolderLabelCantShadowAServer(t *testing.T) {
	ts := startTestServer(t)
	_, err := NewApp(testConfig(ts.URL), io.Discard, strings.NewReader(""), WithProvider(agent.NewStubProvider()), WithSkillDirs(SkillDir{Path: t.TempDir(), Label: "test"}))
	if err == nil || !strings.Contains(err.Error(), "already uses") {
		t.Errorf("err = %v", err)
	}
}
