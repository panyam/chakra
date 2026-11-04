package main

import (
	"sync"
	"testing"
	"time"

	"github.com/panyam/chakra/host"
)

type resizeCall struct {
	source, id string
	cols, rows int
}

type fakeResizer struct {
	mu    sync.Mutex
	calls []resizeCall
}

func (f *fakeResizer) ResizeProcess(source, id string, cols, rows int) error {
	f.mu.Lock()
	f.calls = append(f.calls, resizeCall{source, id, cols, rows})
	f.mu.Unlock()
	return nil
}

func (f *fakeResizer) got() []resizeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]resizeCall(nil), f.calls...)
}

func out(id string, tty bool) host.HostEvent {
	return host.HostEvent{Kind: host.HostProcessOutput, ProcessOutput: &host.ProcessOutput{Source: "exec", ID: id, TTY: tty, Text: "x"}}
}

func TestTTYSizerResizesEachTTYProcessOnceToTheTerminalLessTheTag(t *testing.T) {
	f := &fakeResizer{}
	s := newTTYSizer()
	s.size = func() (int, int, bool) { return 100, 40, true }
	s.On(out("p0", true)) // before attach: ignored
	s.attach(f)
	s.On(out("p1", true))
	s.On(out("p1", true))
	s.On(out("p2", false))
	deadline := time.Now().Add(2 * time.Second)
	for len(f.got()) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	want := []resizeCall{{"exec", "p1", 100 - len("[p1] "), 40}}
	if got := f.got(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("calls = %+v, want %+v", got, want)
	}
}

func TestTTYSizerDoesNothingWithoutATerminal(t *testing.T) {
	f := &fakeResizer{}
	s := newTTYSizer()
	s.size = func() (int, int, bool) { return 0, 0, false }
	s.attach(f)
	s.On(out("p1", true))
	time.Sleep(50 * time.Millisecond)
	if got := f.got(); len(got) != 0 {
		t.Errorf("calls = %+v, want none without a terminal", got)
	}
}
