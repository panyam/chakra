//go:build unix

package exec

import (
	osexec "os/exec"
	"strings"
	"testing"
	"time"

	"github.com/panyam/chakra/host"
)

// done must mean the output is all in. Here the output path is slow, so the
// copier is still reading when the process exits. Closing the terminal at
// exit would cut off the end (#218, from #219).
func TestTTYDoneWaitsForTheCopier(t *testing.T) {
	argv := helperArgv(t, "burst")
	p := &proc{cmd: osexec.Command(argv[0], argv[1:]...), tty: true, out: &tailBuffer{max: 1 << 20}, done: make(chan struct{})}
	p.out.tee = func([]byte) { time.Sleep(2 * time.Millisecond) }
	if err := startTTY(p, Unconfined(), Policy{}); err != nil {
		t.Fatal(err)
	}
	go p.wait()
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		t.Fatal("process never finished")
	}
	if text, _, _ := p.out.since(0); !strings.Contains(text, "END OF OUTPUT") {
		t.Errorf("done before the copier drained: got %d bytes ending %q", len(text), text[max(0, len(text)-40):])
	}
}

// A background child that keeps the terminal open doesn't hold done past
// drainWait.
func TestTTYDrainWaitIsBounded(t *testing.T) {
	p := &proc{cmd: osexec.Command("sh", "-c", "sleep 30 & echo started"), tty: true, out: &tailBuffer{max: 1 << 20}, done: make(chan struct{})}
	if err := startTTY(p, Unconfined(), Policy{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killProcessGroup(p.cmd) })
	start := time.Now()
	go p.wait()
	select {
	case <-p.done:
	case <-time.After(drainWait + 5*time.Second):
		t.Fatal("done waited on the background child")
	}
	if took := time.Since(start); took > drainWait+2*time.Second {
		t.Errorf("done took %s, want about drainWait (%s)", took, drainWait)
	}
	if text, _, _ := p.out.since(0); !strings.Contains(text, "started") {
		t.Errorf("output = %q", text)
	}
}

// A surface can resize a tty and type into it (#218, folded from #249). The
// process sees the new size, and later events report it.
func TestSurfaceResizesAndTypesIntoATTY(t *testing.T) {
	src, sink := streamingSource(t, ProcessConfig{})
	e := &Extension{src: src}
	_, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "winsize-on-input"), "tty": true, "yield_ms": 200})
	id, _ := sc["session_id"].(string)
	if id == "" {
		t.Fatalf("no session_id in %v", sc)
	}
	if err := e.ResizeProcess(id, 90, 20); err != nil {
		t.Fatal(err)
	}
	if err := e.WriteProcess(id, "go\n"); err != nil {
		t.Fatal(err)
	}
	evs := sink.waitFor(t, 5*time.Second, func(evs []host.ProcessOutput) bool {
		return exited(evs) && strings.Contains(sink.text(), "winsize=")
	})
	if !strings.Contains(sink.text(), "winsize=90x20") {
		t.Errorf("process saw %q, want winsize=90x20", sink.text())
	}
	if last := evs[len(evs)-1]; last.Cols != 90 || last.Rows != 20 {
		t.Errorf("last event size %dx%d, want 90x20", last.Cols, last.Rows)
	}
	if err := e.WriteProcess(id, "x"); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Errorf("writing to an exited process: err = %v", err)
	}
	if err := e.ResizeProcess("p404", 80, 24); err == nil {
		t.Error("resizing an unknown process succeeded")
	}
}

func TestResizingAPlainProcessIsRefused(t *testing.T) {
	src, _ := streamingSource(t, ProcessConfig{})
	e := &Extension{src: src}
	_, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "staged", "first", "5s"), "yield_ms": 100})
	id, _ := sc["session_id"].(string)
	if err := e.ResizeProcess(id, 80, 24); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("err = %v, want a not-a-terminal refusal", err)
	}
}
