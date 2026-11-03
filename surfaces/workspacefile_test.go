package surfaces

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/panyam/chakra/ext/agentsmd"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/host"
)

func writeWorkspaceFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "workspace.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadWorkspaceFileApply(t *testing.T) {
	p := writeWorkspaceFile(t, `{
		"repoMap": true,
		"languageServers": [{"command": ["gopls"], "extensions": [".go"], "languageId": "go"}],
		"exec": {
			"sandbox": "unconfined",
			"timeout": "90s",
			"maxOutput": 1024,
			"writePaths": [],
			"commands": [{
				"name": "test",
				"argv": ["go", "test"],
				"description": "Run the Go tests.",
				"args": {"max": 3, "match": "\\./.*", "paths": true},
				"readOnly": true,
				"timeout": "5m"
			}]
		}
	}`)
	f, err := LoadWorkspaceFile(p)
	if err != nil {
		t.Fatalf("LoadWorkspaceFile: %v", err)
	}
	cfg := WorkspaceConfig{Roots: []string{"/flag/root"}}
	if err := f.Apply(&cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(cfg.Roots) != 1 || cfg.Roots[0] != "/flag/root" {
		t.Errorf("Apply must leave the flag-owned Roots alone, got %v", cfg.Roots)
	}
	if !cfg.RepoMap {
		t.Error("repoMap not applied")
	}
	if len(cfg.LanguageServers) != 1 || cfg.LanguageServers[0].Command[0] != "gopls" ||
		cfg.LanguageServers[0].Extensions[0] != ".go" || cfg.LanguageServers[0].LanguageID != "go" {
		t.Errorf("languageServers not applied: %+v", cfg.LanguageServers)
	}

	ec := cfg.Exec
	if ec == nil {
		t.Fatal("exec not applied")
	}
	if ec.Sandbox == nil || ec.Sandbox.Name() != "unconfined" {
		t.Errorf("sandbox \"unconfined\" should select exec.Unconfined, got %v", ec.Sandbox)
	}
	if ec.Timeout != 90*time.Second || ec.MaxOutput != 1024 {
		t.Errorf("timeout/maxOutput = %v/%d", ec.Timeout, ec.MaxOutput)
	}
	if ec.WritePaths == nil || len(ec.WritePaths) != 0 {
		t.Errorf("an explicit [] writePaths must stay empty-non-nil (none), got %#v", ec.WritePaths)
	}
	if ec.DenyRead != nil {
		t.Errorf("an absent denyRead must stay nil (package default), got %#v", ec.DenyRead)
	}
	if len(ec.Commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(ec.Commands))
	}
	c := ec.Commands[0]
	if c.Name != "test" || strings.Join(c.Argv, " ") != "go test" || !c.ReadOnly || c.Reversible || c.Timeout != 5*time.Minute {
		t.Errorf("command not applied: %+v", c)
	}
	if c.Args == nil || c.Args.Max != 3 || c.Args.Match != `\./.*` || !c.Args.Paths {
		t.Errorf("args not applied: %+v", c.Args)
	}
}

func TestLoadWorkspaceFileRejectsUnknownField(t *testing.T) {
	p := writeWorkspaceFile(t, `{"exec": {"commands": [{"name": "rm", "argv": ["rm"], "description": "x", "readOnyl": true}]}}`)
	if _, err := LoadWorkspaceFile(p); err == nil || !strings.Contains(err.Error(), "readOnyl") {
		t.Fatalf("a misspelt readOnly must be an error naming the field, got %v", err)
	}
}

func TestWorkspaceFileSandbox(t *testing.T) {
	for _, tc := range []struct {
		sandbox string
		want    string // "" for the platform default (nil Sandbox)
		wantErr bool
	}{
		{sandbox: "", want: ""},
		{sandbox: "platform", want: ""},
		{sandbox: "unconfined", want: "unconfined"},
		{sandbox: "none", wantErr: true},
	} {
		f := &WorkspaceFile{Exec: &ExecFile{Sandbox: tc.sandbox}}
		var cfg WorkspaceConfig
		err := f.Apply(&cfg)
		if tc.wantErr {
			if err == nil {
				t.Errorf("sandbox %q: want an error", tc.sandbox)
			}
			continue
		}
		if err != nil {
			t.Errorf("sandbox %q: %v", tc.sandbox, err)
			continue
		}
		got := ""
		if cfg.Exec.Sandbox != nil {
			got = cfg.Exec.Sandbox.Name()
		}
		if got != tc.want {
			t.Errorf("sandbox %q selected %q, want %q", tc.sandbox, got, tc.want)
		}
	}
}

func TestWorkspaceFileBadDuration(t *testing.T) {
	f := &WorkspaceFile{Exec: &ExecFile{Commands: []CommandFile{{Name: "t", Timeout: "soon"}}}}
	var cfg WorkspaceConfig
	if err := f.Apply(&cfg); err == nil || !strings.Contains(err.Error(), "commands[t].timeout") {
		t.Fatalf("want an error naming the field, got %v", err)
	}
}

// TestDogfoodWorkspaceFileBuilds keeps the mission exercise's checked-in
// config from rotting: it must decode and build into a working tool set.
//
// Two substitutions keep it runnable in CI. Language servers are dropped
// because lsp.New spawns them and gopls is not on the runner. Off darwin the
// sandbox becomes Unconfined, because Linux has no backend yet (#32) and the
// platform default would refuse. Both are construction concerns. The file
// exists to declare the allowlist, and the allowlist is built as written.
func TestDogfoodWorkspaceFileBuilds(t *testing.T) {
	f, err := LoadWorkspaceFile(filepath.Join("..", "examples", "dogfood", "workspace.json"))
	if err != nil {
		t.Fatalf("LoadWorkspaceFile: %v", err)
	}
	cfg := WorkspaceConfig{Roots: []string{t.TempDir()}, NoCheckpoint: true}
	if err := f.Apply(&cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cfg.Exec == nil {
		t.Fatal("the dogfood file must configure exec: the mission exercise runs the tests")
	}
	cfg.LanguageServers = nil
	cfg.RepoMap = false
	if runtime.GOOS != "darwin" {
		cfg.Exec.Sandbox = exec.Unconfined()
	}

	exts, err := WorkspaceExtensions(cfg)
	if err != nil {
		t.Fatalf("WorkspaceExtensions: %v", err)
	}
	var out bytes.Buffer
	app, err := host.NewApp(&host.Config{Model: host.ModelConfig{BaseURL: "http://127.0.0.1:1", Model: "test"}},
		&out, strings.NewReader(""), host.WithProvider(stubProvider{}), host.WithExtension(exts...))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	if err := app.Tools(context.Background()); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	for _, want := range []string{"run_test", "run_test_one", "run_test_all", "run_vet", "run_fmt_check", "run_build"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dogfood tool %q missing; listing was:\n%s", want, out.String())
		}
	}
}

// TestDogfoodFilesLoad keeps the mission exercise's files loadable: a field
// typo in either fails here rather than at the start of a dogfood run. It
// also pins the step budget, since run 1 (issue 70) died on the agent's
// default of 8 halfway through writing a test, and that offloading is on.
func TestDogfoodFilesLoad(t *testing.T) {
	cfg, err := host.LoadConfig("../examples/dogfood/config.json")
	if err != nil {
		t.Fatalf("dogfood config: %v", err)
	}
	if cfg.MaxSteps < 30 {
		t.Errorf("dogfood maxSteps = %d; a read-edit-test-fix turn needs room, want at least 30", cfg.MaxSteps)
	}
	if cfg.Offload == nil {
		t.Error("dogfood must offload: check 3 needs the whole test transcript in the store, where read_tool_result can grep it (#59)")
	}
	var ws WorkspaceConfig
	if err := ws.LoadFile("../examples/dogfood/workspace.json"); err != nil {
		t.Fatalf("dogfood workspace file: %v", err)
	}
}

func TestWorkspaceFileShellRules(t *testing.T) {
	p := writeWorkspaceFile(t, `{"exec": {"sandbox": "unconfined", "shell": {
		"timeout": "30s",
		"rules": [
			{"prefix": ["git", ["status", "diff"]], "decision": "allow", "match": [["git", "diff", "HEAD"]]},
			{"prefix": ["rm", "-rf"], "decision": "prompt", "justification": "recursive delete"}
		]}}}`)
	var cfg WorkspaceConfig
	if err := cfg.LoadFile(p); err != nil {
		t.Fatal(err)
	}
	sh := cfg.Exec.Shell
	if sh == nil || sh.Timeout != 30*time.Second || len(sh.Rules) != 2 {
		t.Fatalf("shell = %+v", sh)
	}
	if got := sh.Rules[0].Prefix; len(got) != 2 || len(got[1]) != 2 || got[1][1] != "diff" {
		t.Errorf("prefix alternatives = %q", got)
	}
	if sh.Rules[1].Decision != exec.Prompt || sh.Rules[1].Justification != "recursive delete" {
		t.Errorf("rule 1 = %+v", sh.Rules[1])
	}

	bad := writeWorkspaceFile(t, `{"exec": {"shell": {"rules": [{"prefix": [3], "decision": "allow"}]}}}`)
	if err := (&WorkspaceConfig{}).LoadFile(bad); err == nil {
		t.Error("a numeric prefix word loaded")
	}
}

func TestWorkspaceFileInstructions(t *testing.T) {
	var cfg WorkspaceConfig
	if err := cfg.LoadFile(writeWorkspaceFile(t, `{"instructions": {"preset": "codex", "maxBytes": 1000, "names": ["AGENTS.md", "TEAM.md"]}}`)); err != nil {
		t.Fatal(err)
	}
	p := cfg.Instructions
	if p == nil || p.MaxBytes != 1000 || len(p.Names) != 2 || p.Names[1] != "TEAM.md" || p.Override != "AGENTS.override.md" || !p.PrimaryOnly {
		t.Errorf("codex preset with overrides = %+v", p)
	}

	cfg = WorkspaceConfig{}
	if err := cfg.LoadFile(writeWorkspaceFile(t, `{"instructions": {"subdirs": "off"}}`)); err != nil {
		t.Fatal(err)
	}
	if p := cfg.Instructions; p == nil || p.Subdirs != agentsmd.SubdirsOff || p.Changes != agentsmd.ChangesNotice {
		t.Errorf("no preset named should start from chakra: %+v", p)
	}

	cfg = WorkspaceConfig{}
	if err := cfg.LoadFile(writeWorkspaceFile(t, `{"instructions": {"off": true}}`)); err != nil || !cfg.NoInstructions {
		t.Errorf("off: NoInstructions=%v err=%v", cfg.NoInstructions, err)
	}

	for _, bad := range []string{
		`{"instructions": {"preset": "cursor"}}`,
		`{"instructions": {"preset": "codex", "maxSize": 10}}`,
	} {
		if err := (&WorkspaceConfig{}).LoadFile(writeWorkspaceFile(t, bad)); err == nil {
			t.Errorf("%s loaded", bad)
		}
	}
}

func TestWorkspaceFileProcesses(t *testing.T) {
	for body, want := range map[string]*exec.ProcessConfig{
		`{"exec": {"sandbox": "unconfined", "shell": {"processes": true}}}`:                         {},
		`{"exec": {"sandbox": "unconfined", "shell": {"processes": false}}}`:                        nil,
		`{"exec": {"sandbox": "unconfined", "shell": {}}}`:                                          nil,
		`{"exec": {"sandbox": "unconfined", "shell": {"processes": {"max": 2, "maxYield": "5s"}}}}`: {Max: 2, MaxYield: 5 * time.Second},
		`{"exec": {"sandbox": "unconfined", "shell": {"processes": {"streamBudget": -1}}}}`:         {StreamBudget: -1},
		`{"exec": {"sandbox": "unconfined", "shell": {"processes": {"detach": true}}}}`:             {AllowDetach: true},
	} {
		var cfg WorkspaceConfig
		if err := cfg.LoadFile(writeWorkspaceFile(t, body)); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		got := cfg.Exec.Shell.Processes
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%s: processes = %+v, want %+v", body, got, want)
		}
	}
	if err := (&WorkspaceConfig{}).LoadFile(writeWorkspaceFile(t, `{"exec": {"shell": {"processes": {"maxx": 2}}}}`)); err == nil {
		t.Error("an unknown processes field loaded")
	}
}

func TestWorkspaceFileNetwork(t *testing.T) {
	var cfg WorkspaceConfig
	if err := cfg.LoadFile(writeWorkspaceFile(t, `{"exec": {"sandbox": "unconfined", "shell": {}, "network": {"allow": ["proxy.golang.org", "*.npmjs.org"], "ask": true}}}`)); err != nil {
		t.Fatal(err)
	}
	n := cfg.Exec.Network
	if n == nil || len(n.Allow) != 2 || n.Allow[1] != "*.npmjs.org" || !n.Ask {
		t.Errorf("network = %+v", n)
	}
	if err := (&WorkspaceConfig{}).LoadFile(writeWorkspaceFile(t, `{"exec": {"shell": {}, "network": {"allowed": ["x"]}}}`)); err == nil {
		t.Error("an unknown network field loaded")
	}
}
