package surfaces

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra/ext/exec"
)

// canSandbox reports whether sandbox-exec can apply a profile here. It cannot
// inside another sandbox, which is where these tests run when the agent runs
// make test through run_test_all: Seatbelt refuses a second profile with
// "sandbox_apply: Operation not permitted" (#95). The probe denies by default
// because an allow-everything profile still applies when nested.
var canSandbox = sync.OnceValue(func() bool {
	probe := "(version 1)(deny default)(allow process*)(allow file-read*)"
	return osexec.Command("/usr/bin/sandbox-exec", "-p", probe, "/usr/bin/true").Run() == nil
})

// dogfoodExec builds the mission exercise's exec allowlist, as checked in, over
// a fixture module that carries the repo's scripts/test-pkg.sh. It runs under
// the real sandbox on darwin, which is where the exercise runs. Elsewhere, or
// already inside a sandbox, it runs unconfined: these tests check the
// allowlist and test-pkg.sh, and ext/exec's exec_live suite checks the
// profile itself.
func dogfoodExec(t *testing.T) *exec.Source {
	t.Helper()
	root := t.TempDir()
	script, err := os.ReadFile(filepath.Join("..", "scripts", "test-pkg.sh"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"scripts/test-pkg.sh": string(script),
		"go.mod":              "module fixture\n\ngo 1.24\n",
		"serve/serve_test.go": `package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServes(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
}
`,
		"pick/pick_test.go": `package pick

import "testing"

func TestPicked(t *testing.T) {}

func TestNotPicked(t *testing.T) { t.Fatal("ran a test the pattern did not name") }
`,
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	f, err := LoadWorkspaceFile(filepath.Join("..", "examples", "dogfood", "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg WorkspaceConfig
	if err := f.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	ec := *cfg.Exec
	ec.Roots = []string{root}
	switch {
	case runtime.GOOS != "darwin":
		ec.Sandbox = exec.Unconfined()
	case !canSandbox():
		t.Log("already inside a sandbox, so running the allowlist unconfined")
		ec.Sandbox = exec.Unconfined()
	}
	src, err := exec.NewSource(ec)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func runDogfood(t *testing.T, src *exec.Source, tool string, args ...string) (string, bool) {
	t.Helper()
	list := make([]any, len(args))
	for i, a := range args {
		list[i] = a
	}
	res, err := src.Call(context.Background(), tool, map[string]any{"args": list})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res.Content[0].Text, res.IsError
}

// A package whose tests start an httptest server must go green under
// run_test. Dogfood run 3 spent 23 steps on this (#74).
func TestDogfoodRunTestServesOnLoopback(t *testing.T) {
	out, _ := runDogfood(t, dogfoodExec(t), "run_test", "./serve")
	if !strings.Contains(out, "exited 0") {
		t.Fatalf("run_test ./serve must pass:\n%s", out)
	}
}

func TestDogfoodRunTestOneRunsOnlyTheNamedTest(t *testing.T) {
	out, _ := runDogfood(t, dogfoodExec(t), "run_test_one", "./pick:TestPicked")
	if !strings.Contains(out, "exited 0") {
		t.Fatalf("run_test_one must run TestPicked alone:\n%s", out)
	}
}

// go test exits 0 when -run matches nothing, which a model reads as a pass.
func TestDogfoodRunTestOneFailsWhenNothingMatches(t *testing.T) {
	out, _ := runDogfood(t, dogfoodExec(t), "run_test_one", "./pick:TestMisspelled")
	if strings.Contains(out, "exited 0") || !strings.Contains(out, "no test matched") {
		t.Fatalf("a pattern that matches no test must fail and say so:\n%s", out)
	}
}

func TestDogfoodRunTestOneStaysInsideTheWorkspace(t *testing.T) {
	src := dogfoodExec(t)
	for _, arg := range []string{"./pick/..:TestPicked", "../pick:TestPicked", "./pick:Test Picked", "./pick", "/tmp:TestPicked"} {
		if out, isErr := runDogfood(t, src, "run_test_one", arg); !isErr {
			t.Errorf("run_test_one %q must be refused, got:\n%s", arg, out)
		}
	}
}
