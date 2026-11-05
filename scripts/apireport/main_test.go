package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestModuleAPIMatchesGolden(t *testing.T) {
	got, err := moduleAPI("testdata/mod")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/mod.golden")
	if err != nil {
		t.Fatal(err)
	}
	if g := strings.Join(got, "\n") + "\n"; g != string(want) {
		t.Errorf("report differs from testdata/mod.golden:\n%s", g)
	}
}

func TestModuleAPILeavesOutWhatIsNotImportable(t *testing.T) {
	got, err := moduleAPI("testdata/mod")
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(got, "\n")
	for _, absent := range []string{"TestOnly", "Hidden()", "Main()", "Nested()", "Exported()", "func unexported", "kindC", " hidden"} {
		if strings.Contains(all, absent) {
			t.Errorf("report lists %s:\n%s", absent, all)
		}
	}
}

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module example.com/m\n\ngo 1.24\n"
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestNewExportedFuncChangesTheReport(t *testing.T) {
	before, err := moduleAPI(writeModule(t, map[string]string{"m.go": "package m\n\nfunc A() {}\n"}))
	if err != nil {
		t.Fatal(err)
	}
	after, err := moduleAPI(writeModule(t, map[string]string{"m.go": "package m\n\nfunc A() {}\n\nfunc B(n int) error { return nil }\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(before, after) || !slices.Contains(after, "pkg example.com/m, func B(int) error") {
		t.Errorf("before %q, after %q", before, after)
	}
}

func TestDocsAndParamNamesDontChangeTheReport(t *testing.T) {
	a, err := moduleAPI(writeModule(t, map[string]string{"m.go": "package m\n\n// A does one thing.\nfunc A(n int) {}\n"}))
	if err != nil {
		t.Fatal(err)
	}
	b, err := moduleAPI(writeModule(t, map[string]string{"m.go": "package m\n\n// A does another thing now.\nfunc A(count int) {}\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a, b) {
		t.Errorf("a doc or parameter rename changed the report: %q vs %q", a, b)
	}
}
