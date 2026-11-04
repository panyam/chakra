//go:build unix

package exec

import (
	osexec "os/exec"
	"strings"
	"testing"
	"time"
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
