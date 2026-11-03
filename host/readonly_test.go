package host

import (
	"context"
	"testing"

	"github.com/panyam/chakra"
)

// TestHostToolsDeclareReadOnly pins which host-local tools declare
// readOnlyHint (#99). Under reversible-auto the listing tools stop
// prompting, and the tools that change subscriptions, triggers or tasks still
// ask. load_skill is left unannotated on purpose. In catalog mode the approval
// gate on it keeps an untrusted skill body out of context (see the README's
// skills trust model).
func TestHostToolsDeclareReadOnly(t *testing.T) {
	app := &App{skills: newSkillSet()}
	multi := agent.NewMultiSource()
	if err := app.registerMetaTools(multi); err != nil {
		t.Fatal(err)
	}
	if err := app.registerLoadSkill(multi); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"load_skill":         false,
		"list_subscriptions": true,
		"list_triggers":      true,
		"list_tasks":         true,
		"subscribe_events":   false,
		"unsubscribe":        false,
		"create_trigger":     false,
		"remove_trigger":     false,
		"cancel_task":        false,
	}
	defs, err := multi.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range defs {
		ro, _ := d.Annotations["readOnlyHint"].(bool)
		got[d.Name] = ro
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
