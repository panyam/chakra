package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
	skills "github.com/panyam/mcpkit/ext/skills"
)

func filesEntry(name, uri string, files ...string) skills.SkillEntry {
	e := skillEntry(name, uri)
	for i := 0; i+1 < len(files); i += 2 {
		e.Resources.Files = append(e.Resources.Files, skills.SkillResource{URI: files[i], Digest: files[i+1]})
	}
	return e
}

func loadCall(name string) agent.ToolCallInfo {
	raw, _ := json.Marshal(map[string]string{"name": name})
	return agent.ToolCallInfo{Call: agent.ToolCall{Name: "load_skill", Args: core.NewRawJSON(raw)}}
}

func appWithCatalog(cat ...catalogSkill) *App {
	a := &App{skills: newSkillSet()}
	a.skills.order = []string{"s"}
	a.skills.catalog["s"] = cat
	return a
}

// #50: SEP-2640 binds a remembered approval to the entry's resources, every
// uri and digest. A rotated file must give a different key, so the gate asks
// again, and an unchanged skill keeps its key, so it doesn't.
func TestLoadSkillApprovalIsBoundToTheSkillsFiles(t *testing.T) {
	before := appWithCatalog(catalogSkill{serverID: "s", entry: filesEntry("notes", "skill://notes/SKILL.md", "skill://notes/SKILL.md", "sha256:aaa")})
	same := appWithCatalog(catalogSkill{serverID: "s", entry: filesEntry("notes", "skill://notes/SKILL.md", "skill://notes/SKILL.md", "sha256:aaa")})
	rotated := appWithCatalog(catalogSkill{serverID: "s", entry: filesEntry("notes", "skill://notes/SKILL.md", "skill://notes/SKILL.md", "sha256:bbb")})
	added := appWithCatalog(catalogSkill{serverID: "s", entry: filesEntry("notes", "skill://notes/SKILL.md", "skill://notes/SKILL.md", "sha256:aaa", "skill://notes/extra.md", "sha256:ccc")})

	k0, ok := before.rememberKey(loadCall("notes"))
	if !ok {
		t.Fatal("a skill with a file list should be rememberable")
	}
	if k, _ := same.rememberKey(loadCall("notes")); k != k0 {
		t.Error("the same files gave a different key, so an unchanged skill would be asked about again")
	}
	if k, _ := rotated.rememberKey(loadCall("notes")); k == k0 {
		t.Error("a rotated digest kept the key, so the old approval would cover new content")
	}
	if k, _ := added.rememberKey(loadCall("notes")); k == k0 {
		t.Error("an added file kept the key")
	}
}

func TestDynamicOrUnknownSkillIsNeverRemembered(t *testing.T) {
	dyn := filesEntry("live", "skill://live/SKILL.md")
	dyn.Resources.Dynamic = true
	a := appWithCatalog(catalogSkill{serverID: "s", entry: dyn})
	if _, ok := a.rememberKey(loadCall("live")); ok {
		t.Error("a dynamic skill can't be bound to content, so it must not be remembered")
	}
	if _, ok := a.rememberKey(loadCall("nope")); ok {
		t.Error("a load_skill naming no known skill must not be remembered")
	}
	if k, ok := a.rememberKey(agent.ToolCallInfo{Call: agent.ToolCall{Name: "echo"}}); !ok || k != "echo" {
		t.Errorf("other tools keep the tool-name key, got %q %v", k, ok)
	}
}

// #50 item 3: the listing's frontmatter is checked against the SKILL.md that
// is served, through mcpkit's ReadFromEntry. A listing whose description
// disagrees with the file must refuse the load.
func TestLoadSkillRefusesAFrontmatterMismatch(t *testing.T) {
	ts := startSkillsServer(t)
	cfg := testConfig(ts.URL)
	app, err := NewApp(cfg, &strings.Builder{}, strings.NewReader(""), WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	var cat []catalogSkill
	for deadline := time.Now().Add(5 * time.Second); len(cat) == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		cat = app.allCatalogSkills()
	}
	if len(cat) == 0 {
		t.Fatal("no catalog skills from the test server")
	}
	good, err := app.sources.Call(context.Background(), "load_skill", map[string]any{"name": cat[0].entry.Name()})
	if err != nil || good.IsError {
		t.Fatalf("an honest listing should load: %v %+v", err, good)
	}

	// Swap the listing's description, as a server whose skills/list
	// disagrees with the file it serves would.
	bad := cat[0]
	fm := map[string]any{}
	for k, v := range bad.entry.Frontmatter {
		fm[k] = v
	}
	fm["description"] = "something the file does not say"
	bad.entry.Frontmatter = fm
	app.skills.add(bad.serverID, "", []catalogSkill{bad})

	res, err := app.sources.Call(context.Background(), "load_skill", map[string]any{"name": bad.entry.Name()})
	if err == nil && (res == nil || !res.IsError) {
		t.Fatalf("a frontmatter mismatch loaded anyway: %+v", res)
	}
}
