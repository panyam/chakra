package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mapOver builds a repo map over the stub and returns it with the stub's log
// of document methods.
func mapOver(t *testing.T, s stubScript) (string, []string) {
	t.Helper()
	root := workspace(t, map[string]string{
		"a.go": "package p\n\nfunc Alpha() { Beta() }\n",
		"b.go": "package p\n\nfunc Beta() {}\n",
	})
	ext, err := New(Config{Roots: []string{root}, Servers: []ServerSpec{stubSpec(t, s)}, RepoMap: &RepoMapConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ext.Close() })
	deadline := time.Now().Add(10 * time.Second)
	for ext.repoMap.get() == "" {
		if time.Now().After(deadline) {
			t.Fatal("the map never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, _ := os.ReadFile(filepath.Join(root, stubDocLogFile))
	return ext.repoMap.get(), strings.Fields(strings.ReplaceAll(string(raw), "\n", " \n "))
}

func docLines(log []string) []string {
	var out []string
	for i := 0; i+1 < len(log); i += 2 {
		out = append(out, log[i]+" "+log[i+1])
	}
	return out
}

// #117: opening every file the map indexes made gopls type-check and diagnose
// all of them for the rest of the session, so the map took 20s and Close
// waited behind that work. A server that answers for a closed file is asked
// without opening it.
func TestRepoMapDoesNotOpenFilesTheServerCanAnswerFor(t *testing.T) {
	m, log := mapOver(t, stubScript{Symbols: map[string][]documentSymbol{
		"a.go": {{Name: "Alpha"}}, "b.go": {{Name: "Beta"}},
	}})
	for _, line := range docLines(log) {
		if strings.HasPrefix(line, "textDocument/didOpen") {
			t.Errorf("the map opened a file: %s", line)
		}
	}
	if !strings.Contains(m, "Alpha") || !strings.Contains(m, "Beta") {
		t.Errorf("map is missing a symbol:\n%s", m)
	}
}

// A server that only answers for open documents still gets mapped, and every
// file opened for it is closed again.
func TestRepoMapOpensAndClosesForAServerThatNeedsIt(t *testing.T) {
	m, log := mapOver(t, stubScript{SymbolsOnlyWhenOpen: true, Symbols: map[string][]documentSymbol{
		"a.go": {{Name: "Alpha"}}, "b.go": {{Name: "Beta"}},
	}})
	if !strings.Contains(m, "Alpha") || !strings.Contains(m, "Beta") {
		t.Errorf("map is missing a symbol:\n%s", m)
	}
	open := map[string]bool{}
	for _, line := range docLines(log) {
		method, file, _ := strings.Cut(line, " ")
		switch method {
		case "textDocument/didOpen":
			open[file] = true
		case "textDocument/didClose":
			delete(open, file)
		}
	}
	if len(open) != 0 {
		t.Errorf("the map left documents open: %v\nlog:\n%s", open, strings.Join(docLines(log), "\n"))
	}
}
