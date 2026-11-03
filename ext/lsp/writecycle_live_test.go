//go:build lsp_live

package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveRustWriteCycle exercises the production path rather than the opening
// one. It warms the server, then makes real edits that change content, and
// re-checks them through the middleware exactly as a write would be.
//
// TestServers re-checks a file it had only just opened. That hides two
// problems, and this test catches both. rust-analyzer publishes nothing at all
// for a didChange, so without the didSave that goes with it every edit after
// the first would go unchecked. And the wait for a busy server has to be
// bounded. An earlier version waited for rust-analyzer to fall quiet. It never
// does, so every write took the full timeout.
//
// On a small crate we measured about 130ms for a broken edit to report and
// about 1.9s for a clean one. The clean one is slower because an empty result
// is ambiguous and has to wait for a possible correction.
func TestLiveRustWriteCycle(t *testing.T) {
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	os.MkdirAll(filepath.Join(resolved, "src"), 0o755)
	os.WriteFile(filepath.Join(resolved, "Cargo.toml"), []byte("[package]\nname=\"p\"\nversion=\"0.1.0\"\nedition=\"2021\"\n"), 0o644)
	os.WriteFile(filepath.Join(resolved, "src/main.rs"), []byte("fn get() -> i32 { 1 }\n\nfn main() { let _ = get(); }\n"), 0o644)

	ext, err := New(Config{
		Roots:   []string{resolved},
		Servers: []ServerSpec{{Command: []string{"rust-analyzer"}, Extensions: []string{".rs"}, LanguageID: "rust"}},
		Writes:  []WriteSpec{{Tool: "edit_file", Paths: pathArg}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Close()

	// Warm up. Open the file and let startup settle, as a session would.
	c := ext.pool.clients[0]
	main := filepath.Join(resolved, "src/main.rs")
	if _, err := c.sync(main); err != nil {
		t.Fatal(err)
	}
	time.Sleep(8 * time.Second)
	t.Logf("after warmup: %+v", c.diagnostics(main))

	wants := []string{"nope1", "no problems reported", "nope2"}
	for i, body := range []string{
		"fn get() -> i32 { 1 }\n\nfn main() { let _ = get(); let _ = nope1(); }\n",
		"fn get() -> i32 { 1 }\n\nfn main() { let _ = get(); }\n",
		"fn get() -> i32 { 1 }\n\nfn main() { let _ = get(); let _ = nope2(); }\n",
	} {
		os.WriteFile(filepath.Join(resolved, "src/main.rs"), []byte(body), 0o644)
		start := time.Now()
		res, _ := ext.Middleware()[0](context.Background(), callInfo("edit_file", "src/main.rs"), okResult("edited"))
		elapsed := time.Since(start)
		t.Logf("write %d took %s:\n%s", i, elapsed.Round(10*time.Millisecond), resultText(res))
		if elapsed > DefaultDiagnosticsTimeout-time.Second {
			t.Fatalf("write %d took %s, which is the timeout rather than an answer", i, elapsed)
		}
		if want := wants[i]; !strings.Contains(resultText(res), want) {
			t.Fatalf("write %d: want %q in:\n%s", i, want, resultText(res))
		}
	}
}
