package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

// TestStartupBannerNamesActiveConnection is the regression for issue #61: a
// connections-only config is valid without a `model` block (host.Config.Validate
// allows it; host/config_test.go pins it), and `connections` supersedes Model
// for the chat provider. The banner must therefore name the ACTIVE CONNECTION,
// not cfg.Model.Model — which is empty under such a config and made the banner
// read "model ." with no name at all.
func TestStartupBannerNamesActiveConnection(t *testing.T) {
	cfg := &host.Config{
		// No Model block: connections supply the provider.
		Connections: &host.ConnectionsConfig{
			Active: "cloud",
			Connections: map[string]host.ConnectionConfig{
				"local": {Type: "lmstudio", Model: "qwen2.5-coder-32b-instruct"},
				"cloud": {Type: "openai", Model: "gpt-5", APIKeyEnv: "UNUSED"},
			},
		},
	}
	build := func(host.ConnectionConfig) (agent.Provider, error) { return agent.NewStubProvider(), nil }
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProviderBuilder(build))
	if err != nil {
		t.Fatalf("connections-only config should start: %v", err)
	}
	defer app.Close()

	// The host already resolves this correctly for the status line.
	want := app.ModelLabel()
	if want != "cloud" {
		t.Fatalf("ModelLabel() = %q, want the active connection name %q", want, "cloud")
	}

	// What runChat prints: it must name the active connection, not the empty
	// legacy cfg.Model.Model field.
	got := startupBanner(app, &cfg.Servers)
	if !strings.Contains(got, "model "+want+".") {
		t.Fatalf("banner does not name the active connection %q:\n  got:  %q\n  want: contains \"model %s.\"",
			want, got, want)
	}

	// Guard the exact symptom from the issue: "model ." with an empty name.
	if strings.Contains(got, "model .") {
		t.Fatalf("banner regressed to 'model .' (empty model):\n  got: %q", got)
	}
}

// TestStartupBannerQuickStartKeepsModelName pins the other half of #61: with no
// connections registry the banner must still fall back to the single
// connection's cfg.Model, so the quick-start (--model + --url) README example
// keeps naming its model.
func TestStartupBannerQuickStartKeepsModelName(t *testing.T) {
	cfg := &host.Config{
		Model:   host.ModelConfig{BaseURL: "http://unused", Model: "qwen2.5-7b-instruct"},
		Servers: []host.ServerConfig{{ID: "test", URL: "http://unused/mcp"}},
	}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""),
		host.WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatalf("quick-start config should start: %v", err)
	}
	defer app.Close()

	got := startupBanner(app, &cfg.Servers)
	if !strings.Contains(got, "model qwen2.5-7b-instruct.") {
		t.Fatalf("quick-start banner should name cfg.Model:\n  got: %q", got)
	}
	if !strings.Contains(got, "1 server(s)") {
		t.Fatalf("banner should count servers:\n  got: %q", got)
	}
}

// TestEnvOverridesFlagDefaults pins the viper contract the README documents:
// AGENTCHAT_<FLAG> (dashes as underscores) overrides a flag's default, and an
// explicitly passed flag wins over the env var.
func TestEnvOverridesFlagDefaults(t *testing.T) {
	t.Setenv("AGENTCHAT_MODEL", "env-model")
	t.Setenv("AGENTCHAT_BASE_URL", "http://env:9999/v1")

	root, v := newRoot()
	if err := root.ParseFlags([]string{"--base-url", "http://flag:1111/v1"}); err != nil {
		t.Fatal(err)
	}

	if got := v.GetString("model"); got != "env-model" {
		t.Fatalf("env must override the empty default: model = %q", got)
	}
	if got := v.GetString("base-url"); got != "http://flag:1111/v1" {
		t.Fatalf("explicit flag must beat env: base-url = %q", got)
	}
	if got := v.GetString("instructions"); got == "" {
		t.Fatal("untouched flag must keep its default")
	}
}

// TestBuildConfigRequiresModelOrConfig pins the CLI's quick-start guard
// (moved here from the host config test when App core was extracted).
func TestBuildConfigRequiresModelOrConfig(t *testing.T) {
	if _, err := buildConfig("", false, nil, "b", "", "", "", 0); err == nil {
		t.Fatal("want error when neither config nor url/model given")
	}
	cfg, err := buildConfig("", false, []string{"http://x/mcp"}, "b", "m", "", "sys", 0)
	if err != nil || len(cfg.Servers) != 1 || cfg.Model.Model != "m" {
		t.Fatalf("valid quick-start config: %+v %v", cfg, err)
	}
}

// TestWorkspaceConfigLoadsFile pins that --workspace-config reaches the
// workspace alongside the flags, and that its absence leaves exec off.
func TestWorkspaceConfigLoadsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ws.json")
	if err := os.WriteFile(file, []byte(`{"exec": {"commands": [{"name": "vet", "argv": ["go", "vet"], "description": "Vet."}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	root, v := newRoot()
	if err := root.ParseFlags([]string{"--workspace", "/repo", "--workspace-config", file}); err != nil {
		t.Fatal(err)
	}
	cfg, err := workspaceConfig(v)
	if err != nil {
		t.Fatalf("workspaceConfig: %v", err)
	}
	if len(cfg.Roots) != 1 || cfg.Roots[0] != "/repo" {
		t.Errorf("roots = %v, want the --workspace flag", cfg.Roots)
	}
	if cfg.Exec == nil || len(cfg.Exec.Commands) != 1 || cfg.Exec.Commands[0].Name != "vet" {
		t.Fatalf("exec from --workspace-config did not arrive: %+v", cfg.Exec)
	}

	root, v = newRoot()
	if err := root.ParseFlags([]string{"--workspace", "/repo"}); err != nil {
		t.Fatal(err)
	}
	if cfg, err := workspaceConfig(v); err != nil || cfg.Exec != nil {
		t.Fatalf("no --workspace-config must leave exec off: %+v, %v", cfg.Exec, err)
	}
}
