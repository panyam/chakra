package agent

import (
	"context"
	"testing"
)

// TestBuiltinToolsDeclareReadOnly pins which agent-owned tools carry
// readOnlyHint (#99). The ones that only read run without a prompt under
// read-only-auto; the ones that write or start work must still ask.
func TestBuiltinToolsDeclareReadOnly(t *testing.T) {
	mem, err := NewMemorySource(NewInMemoryMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	off := NewOffloadingSource(NewFuncSource(), NewInMemoryToolResultStore(), OffloadConfig{})
	want := map[string]bool{
		RecallToolName:      true,
		ListAgentsToolName:  true,
		ReadToolResultName:  true,
		RememberToolName:    false,
		ForgetToolName:      false,
		SpawnAgentToolName:  false,
		AwaitAgentToolName:  false,
		CancelAgentToolName: false,
	}
	got := map[string]bool{}
	for _, src := range []ToolSource{mem, NewSpawnSource(NewAgentPool(nil)), off} {
		defs, err := src.Tools(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range defs {
			ro, _ := d.Annotations["readOnlyHint"].(bool)
			got[d.Name] = ro
		}
	}
	for name, ro := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s not listed", name)
			continue
		}
		if g != ro {
			t.Errorf("%s readOnlyHint = %v, want %v", name, g, ro)
		}
	}
}

func TestReadOnlyLeavesOtherToolsUnannotated(t *testing.T) {
	fs := NewFuncSource()
	type in struct{}
	noop := func(context.Context, in) (string, error) { return "", nil }
	if err := AddFunc(fs, "plain", "", noop); err != nil {
		t.Fatal(err)
	}
	if err := AddFunc(fs, "reader", "", noop, ReadOnly()); err != nil {
		t.Fatal(err)
	}
	defs, _ := fs.Tools(context.Background())
	if defs[0].Annotations != nil {
		t.Fatalf("plain tool got annotations %v, want none", defs[0].Annotations)
	}
	if ro, _ := defs[1].Annotations["readOnlyHint"].(bool); !ro {
		t.Fatalf("reader annotations = %v, want readOnlyHint true", defs[1].Annotations)
	}
}
