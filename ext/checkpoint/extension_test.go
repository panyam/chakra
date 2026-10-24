package checkpoint

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

func pathArg(args map[string]any) []string {
	p, _ := args["path"].(string)
	if p == "" {
		return nil
	}
	return []string{p}
}

func newExt(t *testing.T) (*Extension, string) {
	t.Helper()
	work := t.TempDir()
	e, err := New(Config{
		Root:   filepath.Join(t.TempDir(), "cp"),
		Writes: []WriteSpec{{Tool: "write_file", Paths: pathArg}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, work
}

func call(name string, args map[string]any) agent.ToolCall {
	raw, _ := json.Marshal(args)
	return agent.ToolCall{Name: name, Args: core.NewRawJSON(raw)}
}

// dispatch runs one call through the extension's middleware, with next
// performing the write the tool would have done.
func dispatch(t *testing.T, e *Extension, info agent.ToolCallInfo, effect func()) error {
	t.Helper()
	mw := e.Middleware()[0]
	_, err := mw(context.Background(), info, func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
		if effect != nil {
			effect()
		}
		return &core.ToolResult{}, nil
	})
	return err
}

func TestCaptureAndUndoRestoresFile(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})}
	if err := dispatch(t, e, info, func() { write(t, f, "clobbered") }); err != nil {
		t.Fatal(err)
	}

	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "original" {
		t.Fatalf("undo gave %q, want %q", got, "original")
	}
	if !strings.Contains(res.Message, "1 file(s) restored") {
		t.Fatalf("undo report = %q", res.Message)
	}
}

// TestSubAgentWritesDoNotCheckpoint is the reason RunScope (#1259) exists. A
// checkpoint per nested frame is both wasteful and wrong: the useful restore
// point is the turn.
func TestSubAgentWritesDoNotCheckpoint(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := agent.ToolCallInfo{
		Call:  call("write_file", map[string]any{"path": f}),
		Scope: agent.RunScope{Path: agent.AgentPath{}.Child("researcher")},
	}
	if err := dispatch(t, e, info, func() { write(t, f, "child wrote this") }); err != nil {
		t.Fatal(err)
	}

	list, err := e.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("a sub-agent write created %d checkpoint(s), want 0", len(list))
	}
}

// TestReadOnlyTurnCreatesNoCheckpoint pins laziness: a turn that touches no
// files costs nothing on disk.
func TestReadOnlyTurnCreatesNoCheckpoint(t *testing.T) {
	e, _ := newExt(t)
	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := agent.ToolCallInfo{Call: call("list_files", nil), ReadOnly: true}
	if err := dispatch(t, e, info, nil); err != nil {
		t.Fatal(err)
	}
	list, err := e.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("a read-only turn created %d checkpoint(s), want 0", len(list))
	}
}

// TestUndoReportsUnreversedCalls is the gap-reporting requirement. Without it
// /undo says "1 file restored" while a created issue goes unmentioned, which
// is a safety net with an unreported hole.
func TestUndoReportsUnreversedCalls(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
		func() { write(t, f, "edited") }); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("create_issue", map[string]any{"title": "bug"})}, nil); err != nil {
		t.Fatal(err)
	}

	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Message, "1 file(s) restored") {
		t.Fatalf("report lost the restore count: %q", res.Message)
	}
	if !strings.Contains(res.Message, "create_issue") {
		t.Fatalf("report did not name the unreversed call: %q", res.Message)
	}
	if !strings.Contains(res.Message, "NOT undone") {
		t.Fatalf("report did not say the call was not undone: %q", res.Message)
	}
}

// TestReadOnlyAndDeniedCallsAreNotReported pins the other side: a list that
// includes calls with nothing to undo trains the user to ignore it.
func TestReadOnlyAndDeniedCallsAreNotReported(t *testing.T) {
	e, _ := newExt(t)
	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("search", nil), ReadOnly: true}, nil); err != nil {
		t.Fatal(err)
	}

	mw := e.Middleware()[0]
	_, err := mw(context.Background(), agent.ToolCallInfo{Call: call("deploy", nil)},
		func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
			return nil, agent.DenyTool("declined by user")
		})
	if err == nil {
		t.Fatal("expected the denial to propagate")
	}

	e.mu.Lock()
	got := len(e.unreversed)
	e.mu.Unlock()
	if got != 0 {
		t.Fatalf("recorded %d unreversed calls, want 0 (read-only and denied are neither)", got)
	}
}

// TestTurnStartScopesTheGapList pins that /undo reports the turn it is undoing
// rather than everything the session ever did.
func TestTurnStartScopesTheGapList(t *testing.T) {
	e, _ := newExt(t)
	ctx := context.Background()
	if err := e.TurnStart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("create_issue", nil)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.TurnStart(ctx); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	got := len(e.unreversed)
	e.mu.Unlock()
	if got != 0 {
		t.Fatalf("a new turn kept %d stale gap(s)", got)
	}
}

// TestOneCheckpointPerTurn pins that several writes in a turn share a restore
// point rather than each making their own.
func TestOneCheckpointPerTurn(t *testing.T) {
	e, work := newExt(t)
	a, b := filepath.Join(work, "a.txt"), filepath.Join(work, "b.txt")
	write(t, a, "a1")
	write(t, b, "b1")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{a, b} {
		if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
			func() { write(t, f, "edited") }); err != nil {
			t.Fatal(err)
		}
	}

	list, err := e.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("one turn made %d checkpoints, want 1", len(list))
	}
	if list[0].Files != 2 {
		t.Fatalf("checkpoint holds %d files, want 2", list[0].Files)
	}
}

func TestUndoCreatedFileDeletesIt(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "new.txt")

	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
		func() { write(t, f, "created") }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.runUndo(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("undo left the created file: %v", err)
	}
}

func TestUndoWithNoCheckpointsIsNotAnError(t *testing.T) {
	e, _ := newExt(t)
	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatalf("undo with nothing to undo should not error: %v", err)
	}
	if !strings.Contains(res.Message, "nothing to undo") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestUndoUnknownCheckpointErrors(t *testing.T) {
	e, _ := newExt(t)
	if _, err := e.runUndo(context.Background(), "turn-999"); err == nil {
		t.Fatal("undo of an unknown checkpoint should error")
	}
}

func TestCheckpointsListsNewestFirst(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	write(t, f, "x")
	ctx := context.Background()
	for range 3 {
		if err := e.TurnStart(ctx); err != nil {
			t.Fatal(err)
		}
		if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})}, nil); err != nil {
			t.Fatal(err)
		}
	}
	res, err := e.runList(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"turn-1", "turn-2", "turn-3"} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("listing missing %s: %q", want, res.Message)
		}
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty Root should error")
	}
	if _, err := New(Config{Root: t.TempDir(), Writes: []WriteSpec{{Tool: "x"}}}); err == nil {
		t.Fatal("a WriteSpec without Paths should error")
	}
}

// TestARestartDoesNotReopenAnEarlierSessionsCheckpoint is the restart half of
// issue 69. The turn counter restarts with the process while the store
// persists, so turn 1 of a second host used to reopen the first host's turn-1
// manifest and merge into it: /undo then also restored the earlier session's
// files, and the merged checkpoint kept the older timestamp, so /undo picked a
// different checkpoint as the newest.
func TestARestartDoesNotReopenAnEarlierSessionsCheckpoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cp")
	work := t.TempDir()
	host := func() *Extension {
		e, err := New(Config{Root: root, Writes: []WriteSpec{{Tool: "write_file", Paths: pathArg}}})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	writeIn := func(e *Extension, f, content string) {
		if err := e.TurnStart(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
			func() { write(t, f, content) }); err != nil {
			t.Fatal(err)
		}
	}

	earlier := filepath.Join(work, "earlier.txt")
	write(t, earlier, "kept")
	writeIn(host(), earlier, "first session's edit")

	later := filepath.Join(work, "later.txt")
	second := host()
	writeIn(second, later, "second session's file")

	if _, err := second.runUndo(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(later); !os.IsNotExist(err) {
		t.Errorf("/undo in the second session left later.txt behind (stat err %v)", err)
	}
	if got := read(t, earlier); got != "first session's edit" {
		t.Errorf("/undo in the second session rewrote the first session's file to %q", got)
	}
}

// TestRepeatedUndoStepsBackTurnByTurn is issue 75. Repeating a bare /undo
// should walk the turn log backwards: the first restores the start of the
// newest turn, the second the start of the one before it. A selection that
// only ever asks the store for its newest checkpoint restores turn-3 twice and
// never reaches turn-2, and because Restore is idempotent the second call
// reports success for a turn it did not change.
func TestRepeatedUndoStepsBackTurnByTurn(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	ctx := context.Background()

	// Three turns, each editing the same file, so every checkpoint captured a
	// distinct start-of-turn state.
	write(t, f, "v0")
	for _, content := range []string{"v1", "v2", "v3"} {
		if err := e.TurnStart(ctx); err != nil {
			t.Fatal(err)
		}
		if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
			func() { write(t, f, content) }); err != nil {
			t.Fatal(err)
		}
	}

	// The file is now v3. Undoing turn 3 should put it back to v2.
	first, err := e.runUndo(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "v2" {
		t.Fatalf("first /undo gave %q, want %q (report: %q)", got, "v2", first.Message)
	}

	// A second /undo must step back another turn, to the start of turn 2,
	// which is v1. Restoring turn 3 again leaves the file at v2.
	second, err := e.runUndo(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "v1" {
		t.Fatalf("second /undo gave %q, want %q: it re-restored the newest checkpoint instead of stepping back a turn (report: %q)", got, "v1", second.Message)
	}
}

func TestUndoPastTheOldestSaysSo(t *testing.T) {
	e, work := newExt(t)
	f := filepath.Join(work, "a.txt")
	ctx := context.Background()
	write(t, f, "v0")
	if err := e.TurnStart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
		func() { write(t, f, "v1") }); err != nil {
		t.Fatal(err)
	}

	if _, err := e.runUndo(ctx, ""); err != nil {
		t.Fatal(err)
	}
	write(t, f, "edited after undo")
	res, err := e.runUndo(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Message, "nothing more to undo") {
		t.Errorf("second /undo with one checkpoint said %q, want it to say nothing is left", res.Message)
	}
	if got := read(t, f); got != "edited after undo" {
		t.Errorf("second /undo rewrote the file to %q; it should leave it alone", got)
	}
}

// TestANewTurnAfterUndoIsUndoneFirst: after stepping back, a turn that writes
// again is the newest thing to undo. Turn 4 touches only b.txt, so continuing
// the walk from before it would restore turn 3's a.txt and leave b.txt edited.
func TestANewTurnAfterUndoIsUndoneFirst(t *testing.T) {
	e, work := newExt(t)
	a, b := filepath.Join(work, "a.txt"), filepath.Join(work, "b.txt")
	ctx := context.Background()
	write(t, a, "a0")
	write(t, b, "b0")
	turn := func(f, content string) {
		t.Helper()
		if err := e.TurnStart(ctx); err != nil {
			t.Fatal(err)
		}
		if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
			func() { write(t, f, content) }); err != nil {
			t.Fatal(err)
		}
	}
	turn(a, "a1")
	turn(a, "a2")
	turn(a, "a3")
	if _, err := e.runUndo(ctx, ""); err != nil {
		t.Fatal(err)
	}

	turn(b, "b1")
	if _, err := e.runUndo(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := read(t, b); got != "b0" {
		t.Errorf("/undo after a new turn left b.txt at %q, want %q: it skipped the newest turn", got, "b0")
	}
	if got := read(t, a); got != "a2" {
		t.Errorf("a.txt is %q, want %q: only turn 4 should have been undone", got, "a2")
	}
}
