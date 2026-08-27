package agent

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// scriptedSource is a ToolSource whose Call returns a fixed result, for
// driving OffloadingSource without a real MCP server.
type scriptedSource struct {
	def    core.ToolDef
	result core.ToolResult
	err    error
}

func (s *scriptedSource) Tools(context.Context) ([]core.ToolDef, error) {
	return []core.ToolDef{s.def}, nil
}
func (s *scriptedSource) Call(context.Context, string, map[string]any) (*core.ToolResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	r := s.result
	return &r, nil
}

func newOffloader(t *testing.T, result core.ToolResult, cfg OffloadConfig) (*OffloadingSource, *InMemoryToolResultStore) {
	t.Helper()
	store := NewInMemoryToolResultStore()
	src := &scriptedSource{def: core.ToolDef{Name: "dump"}, result: result}
	return NewOffloadingSource(src, store, cfg), store
}

func callDump(t *testing.T, o *OffloadingSource) *core.ToolResult {
	t.Helper()
	res, err := o.Call(context.Background(), "dump", nil)
	if err != nil {
		t.Fatalf("Call(dump): %v", err)
	}
	return res
}

func TestOffloadingSource_SmallResultInlineUnchanged(t *testing.T) {
	o, store := newOffloader(t, textResult("short"), OffloadConfig{Threshold: 100})
	res := callDump(t, o)
	if toolResultText(res) != "short" {
		t.Fatalf("small result was altered: %q", toolResultText(res))
	}
	// nothing stored
	if resp, _ := store.GetToolResult(context.Background(), GetToolResultRequest{Ref: "res:anything"}); resp.Found {
		t.Fatal("small result should not have been offloaded")
	}
}

func TestOffloadingSource_LargeResultStubbedAndStored(t *testing.T) {
	big := strings.Repeat("x", 5000)
	o, store := newOffloader(t, textResult(big), OffloadConfig{Threshold: 4096, PreviewLen: 20})
	res := callDump(t, o)
	stub := toolResultText(res)

	if strings.Contains(stub, big) {
		t.Fatal("stub still contains the full payload")
	}
	if !strings.Contains(stub, "5000B") || !strings.Contains(stub, ReadToolResultName) {
		t.Fatalf("stub missing size or retrieval instruction: %q", stub)
	}
	// preview is bounded
	if strings.Count(stub, "x") > 20+2 {
		t.Fatalf("preview exceeded PreviewLen: %q", stub)
	}

	// the ref in the stub resolves to the full result
	ref := extractRef(t, stub)
	resp, err := store.GetToolResult(context.Background(), GetToolResultRequest{Ref: ref})
	if err != nil || !resp.Found {
		t.Fatalf("stored ref %q not found: %v", ref, err)
	}
	if toolResultText(&resp.Result) != big {
		t.Fatal("stored result does not match the original payload")
	}
}

// TestOffloadingSource_StubIsFaithful pins the log-fidelity property: the
// bytes fed back to the model (the stub) are exactly what a persisted
// RoleTool message would carry, so resume replays what the model saw.
func TestOffloadingSource_StubIsFaithful(t *testing.T) {
	o, _ := newOffloader(t, textResult(strings.Repeat("y", 5000)), OffloadConfig{})
	res := callDump(t, o)
	if res.IsError {
		t.Fatal("stub marked IsError")
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("stub is not a single text content: %+v", res.Content)
	}
	if res.StructuredContent != nil {
		t.Fatal("stub leaked structured content (should live in the blob only)")
	}
}

func TestOffloadingSource_ErrorResultStaysInline(t *testing.T) {
	big := strings.Repeat("z", 5000)
	o, store := newOffloader(t, core.ToolResult{IsError: true, Content: []core.Content{{Type: "text", Text: big}}}, OffloadConfig{Threshold: 100})
	res := callDump(t, o)
	if !res.IsError || toolResultText(res) != big {
		t.Fatal("error result was offloaded; errors must stay inline")
	}
	_ = store
}

func TestOffloadingSource_PerToolPinnedInline(t *testing.T) {
	big := strings.Repeat("q", 5000)
	o, _ := newOffloader(t, textResult(big), OffloadConfig{
		Threshold:        100,
		PerToolThreshold: map[string]int{"dump": 0},
	})
	if toolResultText(callDump(t, o)) != big {
		t.Fatal("per-tool pin (threshold 0) should keep the result inline")
	}
}

func TestOffloadingSource_ReadWindowAndGrep(t *testing.T) {
	lines := []string{"alpha 1", "beta 2", "alpha 3", "gamma 4"}
	payload := strings.Join(lines, "\n") + strings.Repeat(" tail", 2000)
	o, _ := newOffloader(t, textResult(payload), OffloadConfig{Threshold: 100, PreviewLen: 10})
	stub := toolResultText(callDump(t, o))
	ref := extractRef(t, stub)

	// grep
	grep, err := o.Call(context.Background(), ReadToolResultName, map[string]any{"ref": ref, "pattern": "^alpha"})
	if err != nil {
		t.Fatalf("read grep: %v", err)
	}
	gt := toolResultText(grep)
	if !strings.Contains(gt, "alpha 1") || !strings.Contains(gt, "alpha 3") || strings.Contains(gt, "beta") {
		t.Fatalf("grep returned wrong lines: %q", gt)
	}

	// window
	win, err := o.Call(context.Background(), ReadToolResultName, map[string]any{"ref": ref, "offset": float64(0), "limit": float64(5)})
	if err != nil {
		t.Fatalf("read window: %v", err)
	}
	wt := toolResultText(win)
	if !strings.HasPrefix(wt, "alpha") || !strings.Contains(wt, "more chars") {
		t.Fatalf("window did not return a bounded prefix with continuation: %q", wt)
	}
}

func TestOffloadingSource_ReadUnknownRefGraceful(t *testing.T) {
	o, _ := newOffloader(t, textResult("x"), OffloadConfig{})
	res, err := o.Call(context.Background(), ReadToolResultName, map[string]any{"ref": "res:gone"})
	if err != nil {
		t.Fatalf("read unknown ref errored instead of degrading: %v", err)
	}
	if res.IsError {
		t.Fatal("unknown ref should be a graceful non-error answer")
	}
	if !strings.Contains(toolResultText(res), "no longer available") {
		t.Fatalf("unexpected graceful message: %q", toolResultText(res))
	}
}

func TestOffloadingSource_ToolsIncludesReadTool(t *testing.T) {
	o, _ := newOffloader(t, textResult("x"), OffloadConfig{})
	defs, err := o.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	if !contains(names, "dump") || !contains(names, ReadToolResultName) {
		t.Fatalf("Tools() = %v, want both dump and %s", names, ReadToolResultName)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// extractRef pulls the "res:xxxx" token out of a stub.
func extractRef(t *testing.T, stub string) string {
	t.Helper()
	i := strings.Index(stub, "res:")
	if i < 0 {
		t.Fatalf("no ref in stub: %q", stub)
	}
	end := i + 4
	for end < len(stub) && isHexish(stub[end]) {
		end++
	}
	return stub[i:end]
}

func isHexish(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')
}

// The two fixtures under testdata/ are real captured `go test` output from
// this module, one run with -v and one without, each carrying a single
// genuine failure. They exist because the position of a failure's detail
// relative to the line a caller can grep for is not a design choice we
// made, it is what the tool emits, and it differs between the two modes.
const (
	verboseFixture = "testdata/gotest_verbose_fail.txt"
	plainFixture   = "testdata/gotest_plain_fail.txt"
)

// TestReadToolResult_GrepContextRecoversFailureDetail is the regression for
// chakra#41. Grepping "--- FAIL" finds which test failed and never why: in
// -v output the assertion is printed ABOVE that marker, and without -v it is
// printed BELOW. Context has to reach both, which is why it is two-sided.
func TestReadToolResult_GrepContextRecoversFailureDetail(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		detail  string // the assertion text, on a line the pattern cannot match
		side    string
	}{
		{"verbose, detail above the marker", verboseFixture, `filter let "secret_tool" through`, "above"},
		{"plain, detail below the marker", plainFixture, `recall returned "alpha", want "beta"`, "below"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			o, _ := newOffloader(t, textResult(string(raw)), OffloadConfig{Threshold: 1})
			ref := extractRef(t, toolResultText(callDump(t, o)))

			grep := func(ctxLines int) string {
				args := map[string]any{"ref": ref, "pattern": "--- FAIL"}
				if ctxLines > 0 {
					args["context"] = float64(ctxLines)
				}
				res, err := o.Call(context.Background(), ReadToolResultName, args)
				if err != nil {
					t.Fatalf("read: %v", err)
				}
				return toolResultText(res)
			}

			// Without context the caller learns the test name and nothing
			// actionable. This half is the bug, and it must keep failing
			// if someone "helpfully" makes context default to non-zero.
			bare := grep(0)
			if !strings.Contains(bare, "--- FAIL") {
				t.Fatalf("grep did not even find the marker: %q", bare)
			}
			if strings.Contains(bare, tc.detail) {
				t.Fatalf("context=0 should not reach the detail %s the marker, got %q", tc.side, bare)
			}

			// With context it reaches the assertion, whichever side it is on.
			withCtx := grep(2)
			if !strings.Contains(withCtx, tc.detail) {
				t.Fatalf("context=2 should have reached the detail %s the marker, got %q", tc.side, withCtx)
			}
		})
	}
}

func TestGrepLines_MergesOverlappingAndSeparatesDistantBlocks(t *testing.T) {
	lines := []string{
		"0 hit", "1", "2 hit", // two hits close enough that ctx=1 overlaps
		"3", "4", "5", "6", "7", "8", "9",
		"10 hit", // far away: its own block
	}
	got := grepLines(lines, regexp.MustCompile("hit"), 1, 0, "res:x", "hit")

	// The first two hits merge into one contiguous run, so no separator
	// appears between them and line 1 is not emitted twice.
	if strings.Count(got, "1\n") != 1 {
		t.Fatalf("overlapping windows should merge, not repeat lines: %q", got)
	}
	if n := strings.Count(got, "\n--\n"); n != 1 {
		t.Fatalf("want exactly one separator between the two distant blocks, got %d: %q", n, got)
	}
	// The gap itself must not be included.
	if strings.Contains(got, "5") || strings.Contains(got, "7") {
		t.Fatalf("lines outside every context window leaked in: %q", got)
	}
}

func TestGrepLines_BoundedByLimit(t *testing.T) {
	var lines []string
	for i := range 500 {
		lines = append(lines, fmt.Sprintf("line %d hit", i))
	}
	got := grepLines(lines, regexp.MustCompile("hit"), 0, 10, "res:x", "hit")

	body, _, _ := strings.Cut(got, "… ")
	if n := len(strings.Split(strings.TrimSpace(body), "\n")); n > 10 {
		t.Fatalf("limit=10 returned %d lines: %q", n, body)
	}
	if !strings.Contains(got, "not shown") {
		t.Fatalf("truncation must be stated, got %q", got)
	}

	// The default bound applies when the caller sets no limit, so a loose
	// pattern cannot re-inline the whole payload offloading just removed.
	unbounded := grepLines(lines, regexp.MustCompile("hit"), 0, 0, "res:x", "hit")
	if n := strings.Count(unbounded, "\n"); n > DefaultGrepMaxLines+1 {
		t.Fatalf("unbounded grep returned %d lines, want <= %d", n, DefaultGrepMaxLines)
	}
}

func TestGrepLines_ContextClampsAtEdges(t *testing.T) {
	lines := []string{"first hit", "middle", "last hit"}
	got := grepLines(lines, regexp.MustCompile("hit"), 50, 0, "res:x", "hit")
	if !strings.Contains(got, "first hit") || !strings.Contains(got, "last hit") {
		t.Fatalf("context past both ends should clamp, not drop: %q", got)
	}
}

func TestGrepLines_NoMatchIsGraceful(t *testing.T) {
	got := grepLines([]string{"a", "b"}, regexp.MustCompile("zzz"), 3, 0, "res:x", "zzz")
	if !strings.Contains(got, "no lines") {
		t.Fatalf("want the graceful no-match answer, got %q", got)
	}
}
