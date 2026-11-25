package codefix

import (
	"strings"
	"testing"
)

// Every task's bug fails go test, and undoing it passes, so each case can be
// fixed and none passes by accident.
func TestEachTaskFailsUntilFixed(t *testing.T) {
	for _, task := range tasks {
		dir, err := task.workspace(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if out, ok := runTests(dir); ok {
			t.Errorf("%s: go test passes with the bug in:\n%s", task.name, out)
		}
		if err := task.fix(dir); err != nil {
			t.Fatal(err)
		}
		if out, ok := runTests(dir); !ok {
			t.Errorf("%s: go test fails after the fix:\n%s", task.name, out)
		}
		if same, err := testsUnchanged(dir); err != nil || !same {
			t.Errorf("%s: the tests changed: %v", task.name, err)
		}
	}
}

// The panic task's test output names the line that panics, which is what
// running the tests tells a model that reading alone doesn't.
func TestNilCartPanicNamesTotal(t *testing.T) {
	dir, err := tasks[2].workspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, _ := runTests(dir)
	if !strings.Contains(out, "cart.(*Cart).Total") {
		t.Errorf("the panic doesn't name Total:\n%s", out)
	}
}
