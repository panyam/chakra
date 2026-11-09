package surfaces

import (
	"context"
	"slices"
	"testing"

	"github.com/panyam/chakra/host"
)

func workspaceSlots(t *testing.T, file string) (*host.SlotsExtension, []host.Extension) {
	t.Helper()
	f, err := LoadWorkspaceFile(writeWorkspaceFile(t, file))
	if err != nil {
		t.Fatal(err)
	}
	cfg := WorkspaceConfig{Roots: []string{t.TempDir()}, CheckpointStore: t.TempDir()}
	if err := f.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	exts, err := WorkspaceExtensions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range exts {
		if sx, ok := e.(*host.SlotsExtension); ok {
			return sx, exts
		}
	}
	return nil, exts
}

// A workspace with the plan and a facts slot gets a plan slot too. The model
// writes facts with write_slot, and only update_plan writes the plan.
func TestWorkspaceSlotsAddAPlanSlot(t *testing.T) {
	sx, exts := workspaceSlots(t, `{"plan": true, "slots": [{"name": "facts", "mode": "append", "maxBytes": 3000}]}`)
	if sx == nil {
		t.Fatal("no slots extension")
	}
	var names []string
	for _, s := range sx.Slots().Specs() {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"plan", "facts"}) {
		t.Errorf("slots = %v, want plan then facts", names)
	}
	src, err := sx.Tools()
	if err != nil || src == nil {
		t.Fatalf("Tools = %v, %v", src, err)
	}
	defs, _ := src.Tools(context.Background())
	if len(defs) != 1 || !slices.Equal(defs[0].InputSchema.(map[string]any)["properties"].(map[string]any)["slot"].(map[string]any)["enum"].([]string), []string{"facts"}) {
		t.Errorf("write_slot = %+v, want it over facts only", defs)
	}
	var plans int
	for _, e := range exts {
		if _, ok := e.(*host.PlanExtension); ok {
			plans++
		}
	}
	if plans != 1 {
		t.Fatalf("%d plan extensions, want 1", plans)
	}
	if _, err := sx.Slots().Write(context.Background(), "plan", "1. read"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePlanWithoutSlotsIsUnchanged(t *testing.T) {
	sx, exts := workspaceSlots(t, `{"plan": true}`)
	if sx != nil {
		t.Fatal("a plan alone added slots")
	}
	if !slices.ContainsFunc(exts, func(e host.Extension) bool { _, ok := e.(*host.PlanExtension); return ok }) {
		t.Fatal("no plan extension")
	}
}

func TestWorkspaceSlotsRejectABadSpec(t *testing.T) {
	f, err := LoadWorkspaceFile(writeWorkspaceFile(t, `{"slots": [{"name": "facts", "mode": "merge"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := WorkspaceConfig{Roots: []string{t.TempDir()}, CheckpointStore: t.TempDir()}
	if err := f.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkspaceExtensions(cfg); err == nil {
		t.Fatal("an unknown slot mode built")
	}
}

