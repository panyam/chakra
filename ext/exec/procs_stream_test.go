package exec

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/chakra/host"
)

// outputSink records what a Source reports while its processes run.
type outputSink struct {
	mu  sync.Mutex
	evs []host.ProcessOutput
}

func (k *outputSink) report(o host.ProcessOutput) {
	k.mu.Lock()
	k.evs = append(k.evs, o)
	k.mu.Unlock()
}

func (k *outputSink) snapshot() []host.ProcessOutput {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]host.ProcessOutput(nil), k.evs...)
}

func (k *outputSink) text() string {
	var b strings.Builder
	for _, o := range k.snapshot() {
		b.WriteString(o.Text)
	}
	return b.String()
}

// waitFor polls the sink until cond holds or the deadline passes.
func (k *outputSink) waitFor(t *testing.T, d time.Duration, cond func([]host.ProcessOutput) bool) []host.ProcessOutput {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		evs := k.snapshot()
		if cond(evs) {
			return evs
		}
		if time.Now().After(deadline) {
			t.Fatalf("sink never reached the condition; got %+v", evs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func exited(evs []host.ProcessOutput) bool {
	return len(evs) > 0 && evs[len(evs)-1].Exited
}

func streamingSource(t *testing.T, pc ProcessConfig) (*Source, *outputSink) {
	t.Helper()
	src := procSource(t, pc)
	sink := &outputSink{}
	src.setProcessOutput(sink.report)
	return src, sink
}

func TestProcessOutputStreamsBetweenCollections(t *testing.T) {
	src, sink := streamingSource(t, ProcessConfig{})
	out, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "staged", "first", "2s", "second", "30s"), "yield_ms": 1000})
	// A slow runner may not have printed first within the yield. The test only
	// needs second to come later, with nobody collecting it.
	if sc["running"] != true || strings.Contains(out, "\nsecond\n") {
		t.Fatalf("start: %q %v", out, sc)
	}
	id := sc["session_id"].(string)
	evs := sink.waitFor(t, 5*time.Second, func([]host.ProcessOutput) bool { return strings.Contains(sink.text(), "second\n") })
	for _, o := range evs {
		if o.Source != "exec" || o.ID != id || !strings.Contains(o.Command, "staged") {
			t.Fatalf("event not keyed to the process: %+v", o)
		}
		if o.Exited {
			t.Fatalf("exited before kill: %+v", o)
		}
	}
	callProc(t, src, KillProcessTool, map[string]any{"session_id": id})
	sink.waitFor(t, 3*time.Second, exited)
}

func TestProcessOutputReportsTheExit(t *testing.T) {
	src, sink := streamingSource(t, ProcessConfig{})
	callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "fail"), "yield_ms": 3000})
	evs := sink.waitFor(t, 3*time.Second, exited)
	last := evs[len(evs)-1]
	if last.ExitCode != 3 {
		t.Errorf("exit event %+v, want code 3", last)
	}
	if !strings.Contains(sink.text(), "compile error") {
		t.Errorf("streamed %q, missing the output", sink.text())
	}
	if n := countExited(evs); n != 1 {
		t.Errorf("%d exit events, want 1", n)
	}
}

func countExited(evs []host.ProcessOutput) int {
	n := 0
	for _, o := range evs {
		if o.Exited {
			n++
		}
	}
	return n
}

var elapsedRE = regexp.MustCompile(`after [0-9][^\]; ]*`)

// normalized is a tool result with its elapsed times blanked, so two runs of
// the same script compare equal.
func normalized(text string, sc map[string]any) string {
	return fmt.Sprintf("%s|%v|%v|%v|%v", elapsedRE.ReplaceAllString(text, "after T"),
		sc["session_id"], sc["running"], sc["exit_code"], sc["dropped"])
}

func TestProcessStreamingLeavesResultsUnchanged(t *testing.T) {
	run := func(stream bool) []string {
		var src *Source
		if stream {
			src, _ = streamingSource(t, ProcessConfig{})
		} else {
			src = procSource(t, ProcessConfig{})
		}
		var got []string
		out, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "staged", "a", "2s", "b", "100ms", "c"), "yield_ms": 1000})
		got = append(got, normalized(out, sc))
		out, sc = callProc(t, src, WriteStdinTool, map[string]any{"session_id": sc["session_id"], "yield_ms": 3000})
		got = append(got, normalized(out, sc))
		out, sc = callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "spam", "500"), "yield_ms": 3000})
		got = append(got, normalized(out, sc))
		return got
	}
	plain, streamed := run(false), run(true)
	for i := range plain {
		if plain[i] != streamed[i] {
			t.Errorf("result %d changed with streaming on:\nwithout: %q\nwith:    %q", i, plain[i], streamed[i])
		}
	}
}

func TestProcessOutputCoalesces(t *testing.T) {
	src, sink := streamingSource(t, ProcessConfig{})
	callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "spam", "20000"), "yield_ms": 3000})
	evs := sink.waitFor(t, 5*time.Second, exited)
	if len(evs) > 200 {
		t.Errorf("%d events for 20002 writes; output is not being batched", len(evs))
	}
	if !strings.Contains(sink.text(), "LAST LINE") {
		t.Errorf("the end of the output never streamed")
	}
}

func TestProcessOutputStopsAtTheBudget(t *testing.T) {
	src, sink := streamingSource(t, ProcessConfig{StreamBudget: 2000})
	callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "spam", "2000"), "yield_ms": 3000})
	evs := sink.waitFor(t, 5*time.Second, exited)
	if n := len(sink.text()); n > 2000 {
		t.Errorf("streamed %d bytes past a 2000-byte budget", n)
	}
	paused := 0
	for _, o := range evs {
		if o.Paused {
			paused++
		}
	}
	if paused != 1 {
		t.Errorf("%d paused events, want 1: %+v", paused, evs)
	}
	if evs[len(evs)-1].ExitCode != 0 {
		t.Errorf("exit event %+v", evs[len(evs)-1])
	}
}

func TestProcessOutputBudgetCanBeLifted(t *testing.T) {
	src, sink := streamingSource(t, ProcessConfig{StreamBudget: -1})
	callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "spam", "2000"), "yield_ms": 3000})
	evs := sink.waitFor(t, 5*time.Second, exited)
	for _, o := range evs {
		if o.Paused {
			t.Fatalf("paused with the budget off: %+v", o)
		}
	}
	if !strings.Contains(sink.text(), "LAST LINE") {
		t.Errorf("output ended early with the budget off")
	}
}

func TestTTYProcessOutputStreams(t *testing.T) {
	src, sink := streamingSource(t, ProcessConfig{})
	callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "isatty"), "tty": true, "yield_ms": 3000})
	sink.waitFor(t, 3*time.Second, exited)
	if !strings.Contains(sink.text(), "tty=true") {
		t.Errorf("streamed %q from a tty process", sink.text())
	}
}
