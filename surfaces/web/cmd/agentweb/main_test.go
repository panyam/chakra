package main

import (
	"os"
	"path/filepath"
	"testing"
)

const twoConnections = `{
  "connections": {
    "active": "first",
    "connections": {
      "first":  { "type": "lmstudio", "model": "model-one" },
      "second": { "type": "lmstudio", "model": "model-two" }
    }
  }
}`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestActiveOverridesTheConfigsConnection(t *testing.T) {
	cfg := writeConfig(t, twoConnections)

	app, err := buildApp(appOptions{configPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if got := app.ModelLabel(); got != "first" {
		t.Errorf("without --active, label = %q, want the config's active (first)", got)
	}

	app, err = buildApp(appOptions{configPath: cfg, active: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if got := app.ModelLabel(); got != "second" {
		t.Errorf("with --active second, label = %q, want second", got)
	}
}

func TestActiveRejectsWhatTheConfigCannotSupply(t *testing.T) {
	if _, err := buildApp(appOptions{configPath: writeConfig(t, twoConnections), active: "third"}); err == nil {
		t.Error("an unknown connection name should be an error, not a silent fallback")
	}
	single := `{"model": {"baseUrl": "http://localhost:1234/v1", "model": "m"}}`
	if _, err := buildApp(appOptions{configPath: writeConfig(t, single), active: "first"}); err == nil {
		t.Error("--active on a config without connections should be an error")
	}
}
