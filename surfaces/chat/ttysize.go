package main

import (
	"os"
	"sync"

	"github.com/panyam/chakra/host"
	"golang.org/x/term"
)

// processResizer is the part of host.App a ttySizer calls.
type processResizer interface {
	ResizeProcess(source, id string, cols, rows int) error
}

// ttySizer gives each tty process agentchat's own terminal size, less the
// "[p1] " tag each output line gets, so a program draws for the width it's
// shown at (#218). It resizes once, on the process's first event. With no
// terminal (a headless exec, or output piped elsewhere) it does nothing.
type ttySizer struct {
	mu   sync.Mutex
	app  processResizer
	seen map[string]bool
	size func() (cols, rows int, ok bool)
}

func newTTYSizer() *ttySizer {
	return &ttySizer{seen: map[string]bool{}, size: stdoutSize}
}

func stdoutSize() (int, int, bool) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	return cols, rows, err == nil && cols > 0 && rows > 0
}

// attach sets the App once it exists. Events before then are ignored.
func (s *ttySizer) attach(app processResizer) {
	s.mu.Lock()
	s.app = app
	s.mu.Unlock()
}

// On implements host.Observer.
func (s *ttySizer) On(ev host.HostEvent) {
	o := ev.ProcessOutput
	if ev.Kind != host.HostProcessOutput || o == nil || !o.TTY || o.Exited {
		return
	}
	key := o.Source + "\x00" + o.ID
	s.mu.Lock()
	app, first := s.app, !s.seen[key]
	s.seen[key] = true
	s.mu.Unlock()
	if app == nil || !first {
		return
	}
	cols, rows, ok := s.size()
	if !ok {
		return
	}
	cols -= len("[" + o.ID + "] ")
	if cols < 20 {
		cols = 20
	}
	// Off the event path: the extension may be emitting this event.
	go func() { _ = app.ResizeProcess(o.Source, o.ID, cols, rows) }()
}
