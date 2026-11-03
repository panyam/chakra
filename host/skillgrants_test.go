package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
	skills "github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

func TestParseSkillGrants(t *testing.T) {
	grants, ignored := parseSkillGrants("Bash(git tag *), Bash(go test:*) Read WebFetch Read(src/**) Bash(rm * -rf) list_files")
	var got []string
	for _, g := range grants {
		got = append(got, g.String())
	}
	want := []string{"shell: git tag *", "shell: go test *", "read_file", "list_files"}
	if !slices.Equal(got, want) {
		t.Errorf("grants = %q, want %q", got, want)
	}
	if !slices.Equal(ignored, []string{"WebFetch", "Read(src/**)", "Bash(rm * -rf)"}) {
		t.Errorf("ignored = %q", ignored)
	}
}

func TestShellGrantMatchesTheArgvPrefix(t *testing.T) {
	g, _ := bashGrant("git tag *")
	exact, _ := bashGrant("git status")
	call := func(argv ...string) agent.ToolCall {
		raw, _ := json.Marshal(map[string]any{"command": argv})
		return agent.ToolCall{Name: "shell", Args: core.NewRawJSON(raw)}
	}
	for _, c := range []struct {
		g    skillGrant
		call agent.ToolCall
		want bool
	}{
		{g, call("git", "tag", "v1"), true},
		{g, call("/usr/bin/git", "tag", "v1"), true},
		{g, call("git", "push"), false},
		{g, call("bash", "-lc", "git tag v1 && rm -rf /"), false},
		{exact, call("git", "status"), true},
		{exact, call("git", "status", "-s"), false},
		{g, agent.ToolCall{Name: "exec_command", Args: core.NewRawJSON([]byte(`{"command":["git","tag","v1"]}`))}, false},
	} {
		if got := c.g.matches(c.call); got != c.want {
			t.Errorf("%s matches %s = %v, want %v", c.g, c.call.Args.Raw(), got, c.want)
		}
	}
}

// shellExt offers a "shell" tool that records the argv of every call that ran.
type shellExt struct {
	BaseExtension
	src *agent.FuncSource
}

func (e shellExt) Name() string                     { return "test-shell" }
func (e shellExt) Tools() (agent.ToolSource, error) { return e.src, nil }
func newShellExt(t *testing.T, ran *[]string) shellExt {
	t.Helper()
	var mu sync.Mutex
	src := agent.NewFuncSource()
	err := agent.AddFunc(src, "shell", "runs a command", func(_ context.Context, in struct {
		Command []string `json:"command"`
	}) (string, error) {
		mu.Lock()
		*ran = append(*ran, strings.Join(in.Command, " "))
		mu.Unlock()
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return shellExt{src: src}
}

func callTurn(id, name string, args any) agent.StubTurn {
	raw, _ := json.Marshal(args)
	return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: id, Name: name, Args: core.NewRawJSON(raw)}}}
}

func shellTurn(id string, argv ...string) agent.StubTurn {
	return callTurn(id, "shell", map[string]any{"command": argv})
}

// grantHarness is an App in ask mode with a shell tool, answering every ask
// with answer and recording what it was asked.
type grantHarness struct {
	app  *App
	ran  []string
	mu   sync.Mutex
	asks []string
}

func (h *grantHarness) asked() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.asks...)
}

func newGrantHarness(t *testing.T, cfg *Config, turns []agent.StubTurn, opts ...AppOption) *grantHarness {
	t.Helper()
	h := &grantHarness{}
	if cfg.Approval == nil {
		cfg.Approval = &ApprovalConfig{Mode: "ask"}
	}
	if cfg.Model.Model == "" {
		cfg.Model = ModelConfig{BaseURL: "http://unused", Model: "stub"}
	}
	ui := func(_ context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		h.mu.Lock()
		h.asks = append(h.asks, req.Message)
		h.mu.Unlock()
		return core.ElicitationResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}
	opts = append(opts, WithProvider(agent.NewStubProvider(turns...)), WithElicitationUI(ui), WithExtension(newShellExt(t, &h.ran)))
	app, err := NewApp(cfg, io.Discard, strings.NewReader(""), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	h.app = app
	return h
}

const releaseSkill = "---\nname: release\ndescription: Cut a release.\nallowed-tools: Bash(git tag *) WebFetch\n---\nTag it.\n"

// Loading a project skill asks once, and the ask lists the grants. The granted
// command then runs unasked for the rest of the turn, and the next turn asks
// again.
func TestApprovedSkillGrantsLastTheTurn(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "release", releaseSkill)
	h := newGrantHarness(t, &Config{}, []agent.StubTurn{
		callTurn("l1", "load_skill", map[string]string{"name": "release"}),
		shellTurn("s1", "git", "tag", "v1"),
		shellTurn("s2", "git", "push"),
		{Text: "tagged"},
		shellTurn("s3", "git", "tag", "v2"),
		{Text: "tagged again"},
	}, WithSkillDirs(SkillDir{Path: dir, Label: "project"}))

	if err := h.app.RunTurn(context.Background(), "release it"); err != nil {
		t.Fatal(err)
	}
	asks := h.asked()
	if len(asks) != 2 {
		t.Fatalf("asks = %q, want the load and the ungranted git push", asks)
	}
	if !strings.Contains(asks[0], "load_skill") || !strings.Contains(asks[0], "shell: git tag *") || !strings.Contains(asks[0], "Not granted, since chakra has no matching tool: WebFetch") {
		t.Errorf("load ask = %q, want the grants and the ignored entry listed", asks[0])
	}
	if !strings.Contains(asks[1], "git") || !strings.Contains(asks[1], "push") {
		t.Errorf("second ask = %q, want the git push", asks[1])
	}

	if err := h.app.RunTurn(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	if asks := h.asked(); len(asks) != 3 || !strings.Contains(asks[2], "v2") {
		t.Errorf("asks after turn 2 = %q, want the granted command asked about again", asks)
	}
	if !slices.Equal(h.ran, []string{"git tag v1", "git push", "git tag v2"}) {
		t.Errorf("ran = %q", h.ran)
	}
}

// Under read-only-auto a plain load_skill runs unasked, so a project skill
// that grants tools has to ask, or its grants would apply unseen.
func TestProjectSkillWithGrantsAsksUnderReadOnlyAuto(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "release", releaseSkill)
	writeSkill(t, dir, "notes", skillMD("notes", "Plain.", "no grants"))
	h := newGrantHarness(t, &Config{Approval: &ApprovalConfig{Mode: "read-only-auto"}}, nil, WithSkillDirs(SkillDir{Path: dir, Label: "project"}))
	if class, ok := h.app.classifyApproval(context.Background(), loadCall("release")); !ok || class.Disposition != agent.DispositionDestructive {
		t.Errorf("release load classified %+v %v, want destructive so it asks", class, ok)
	}
	if _, ok := h.app.classifyApproval(context.Background(), loadCall("notes")); ok {
		t.Error("a skill with no grants was classified, which would change how its load is asked about")
	}
}

// A skill from the user's own folder grants without the extra ask.
func TestOwnSkillGrantsWithoutAnAsk(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "release", releaseSkill)
	h := newGrantHarness(t, &Config{Approval: &ApprovalConfig{Mode: "ask", Rules: map[string]string{"load_skill": "allow"}}}, []agent.StubTurn{
		callTurn("l1", "load_skill", map[string]string{"name": "release"}),
		shellTurn("s1", "git", "tag", "v1"),
		{Text: "done"},
	}, WithSkillDirs(SkillDir{Path: dir, Label: "mine", Own: true}))
	if err := h.app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if asks := h.asked(); len(asks) != 0 {
		t.Errorf("asks = %q, want none for the user's own skill", asks)
	}
	if !slices.Equal(h.ran, []string{"git tag v1"}) {
		t.Errorf("ran = %q", h.ran)
	}
}

// A project skill loaded without anyone being asked (load_skill allowed by
// rule) grants nothing.
func TestProjectSkillLoadedUnaskedGrantsNothing(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "release", releaseSkill)
	h := newGrantHarness(t, &Config{Approval: &ApprovalConfig{Mode: "ask", Rules: map[string]string{"load_skill": "allow"}}}, []agent.StubTurn{
		callTurn("l1", "load_skill", map[string]string{"name": "release"}),
		shellTurn("s1", "git", "tag", "v1"),
		{Text: "done"},
	}, WithSkillDirs(SkillDir{Path: dir, Label: "project"}))
	if err := h.app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if asks := h.asked(); len(asks) != 1 || !strings.Contains(asks[0], "git") {
		t.Errorf("asks = %q, want only the git tag, since nobody approved the grants", asks)
	}
}

// A deny rule beats a grant.
func TestDenyRuleBeatsAGrant(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "release", releaseSkill)
	h := newGrantHarness(t, &Config{Approval: &ApprovalConfig{Mode: "ask", Rules: map[string]string{"shell": "deny"}}}, []agent.StubTurn{
		callTurn("l1", "load_skill", map[string]string{"name": "release"}),
		shellTurn("s1", "git", "tag", "v1"),
		{Text: "done"},
	}, WithSkillDirs(SkillDir{Path: dir, Label: "project"}))
	if err := h.app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(h.ran) != 0 {
		t.Errorf("ran = %q, want the denied shell refused despite the grant", h.ran)
	}
}

// An edited skill asks again, and its old approval grants nothing.
func TestChangedSkillAsksForItsGrantsAgain(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "release", releaseSkill)
	h := newGrantHarness(t, &Config{Approval: &ApprovalConfig{Mode: "ask", Remember: true}}, []agent.StubTurn{
		callTurn("l1", "load_skill", map[string]string{"name": "release"}),
		{Text: "loaded"},
		callTurn("l2", "load_skill", map[string]string{"name": "release"}),
		{Text: "loaded again"},
	}, WithSkillDirs(SkillDir{Path: dir, Label: "project"}))
	if err := h.app.RunTurn(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dir, "release", strings.Replace(releaseSkill, "Tag it.", "Tag it, then push.", 1))
	h.app.loadSkillDir(h.app.skillDirs[0])
	if err := h.app.RunTurn(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	if asks := h.asked(); len(asks) != 2 {
		t.Errorf("asks = %q, want the edited skill asked about again despite remember", asks)
	}
}

// A server's skill grants like a project skill: only after an ask.
func TestServerSkillGrantsLikeAProjectSkill(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "release", releaseSkill)
	srv := testutil.NewTestServer()
	p, err := skills.NewProvider(skills.WithDirectory(dir))
	if err != nil {
		t.Fatal(err)
	}
	p.RegisterWith(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	cfg := testConfig(ts.URL)
	cfg.Approval = &ApprovalConfig{Mode: "read-only-auto"}
	h := newGrantHarness(t, cfg, []agent.StubTurn{
		callTurn("l1", "load_skill", map[string]string{"name": "release"}),
		shellTurn("s1", "git", "tag", "v1"),
		{Text: "done"},
	})
	if err := h.app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	asks := h.asked()
	if len(asks) != 1 || !strings.Contains(asks[0], "shell: git tag *") {
		t.Errorf("asks = %q, want one load ask listing the grant", asks)
	}
	if !slices.Equal(h.ran, []string{"git tag v1"}) {
		t.Errorf("ran = %q, want the granted command run unasked", h.ran)
	}
}
