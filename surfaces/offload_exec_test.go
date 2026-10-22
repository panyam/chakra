package surfaces

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/ext/exec"
)

// transcript prints about 450 KB shaped like go test -v, with one failure
// halfway down: the case #59 is about, where the 32 KiB view drops it.
const transcript = `i=0
while [ $i -lt 10000 ]; do
	echo "=== RUN   TestFiller$i"
	echo "--- PASS: TestFiller$i (0.00s)"
	i=$((i+1))
	if [ $i = 5000 ]; then
		echo "--- FAIL: TestBanner (0.00s)"
		echo "    main_test.go:79: got x, want y"
	fi
done
exit 1
`

// offloadedExec builds the workspace's exec tools behind an OffloadingSource,
// the way a host with Offload set composes them.
func offloadedExec(t *testing.T, offloading bool) agent.ToolSource {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "transcript.sh"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	exts, err := WorkspaceExtensions(WorkspaceConfig{
		Roots:        []string{root},
		NoCheckpoint: true,
		Offloading:   offloading,
		Exec: &exec.Config{
			Sandbox:  exec.Unconfined(),
			Commands: []exec.CommandSpec{{Name: "test", Argv: []string{"sh", "transcript.sh"}, Description: "Test.", ReadOnly: true}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range exts {
		if e.Name() != "exec" {
			continue
		}
		src, err := e.(interface {
			Tools() (agent.ToolSource, error)
		}).Tools()
		if err != nil {
			t.Fatal(err)
		}
		return agent.NewOffloadingSource(src, agent.NewInMemoryToolResultStore(), agent.OffloadConfig{Threshold: 16 << 10})
	}
	t.Fatal("no exec extension")
	return nil
}

var refRE = regexp.MustCompile(`stored as (\S+)\]`)

// grepFailure runs the transcript and greps the stored result the way the
// prompt tells a model to.
func grepFailure(t *testing.T, src agent.ToolSource) string {
	t.Helper()
	ctx := context.Background()
	res, err := src.Call(ctx, "run_test", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := refRE.FindStringSubmatch(res.Content[0].Text)
	if m == nil {
		t.Fatalf("a long run must come back as a stub:\n%.400s", res.Content[0].Text)
	}
	found, err := src.Call(ctx, agent.ReadToolResultName, map[string]any{"ref": m[1], "pattern": "--- FAIL", "context": 1})
	if err != nil {
		t.Fatal(err)
	}
	return found.Content[0].Text
}

func TestOffloadedExecRunCanBeGreppedForItsFailure(t *testing.T) {
	if got := grepFailure(t, offloadedExec(t, true)); !strings.Contains(got, "main_test.go:79: got x, want y") {
		t.Fatalf("read_tool_result must find the assertion in the middle of the run:\n%s", got)
	}
}

// Without the Offloading wiring, the store only ever sees the 32 KiB view, so
// the same grep finds nothing. This was every offloading host before #59.
func TestExecViewWithoutOffloadingLosesTheMiddle(t *testing.T) {
	if got := grepFailure(t, offloadedExec(t, false)); strings.Contains(got, "main_test.go:79") {
		t.Fatalf("expected the default view to have dropped the assertion; the test no longer shows the #59 case:\n%s", got)
	}
}
