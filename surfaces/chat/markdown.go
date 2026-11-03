package main

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

// mdRenderer renders a finished markdown block to terminal ANSI via glamour,
// for the "Glamour once at commit" path (issue 1063 B2). Glamour mangles a
// half-written fenced code block and reflows on every call. So the renderer
// runs only on a committed block, never on partial streaming deltas. Streaming
// output is raw, and the render happens once when the block closes.
//
// The --no-color flag, NO_COLOR, and a dumb terminal resolve to one color
// decision. When color is off, rendering is disabled and text passes through
// raw. That matches the plain-mode semantics of the host package's terminal
// renderer. The plain REPL never reaches this path at all. The underlying
// glamour TermRenderer is cached and rebuilt only when the target width
// changes.
type mdRenderer struct {
	mu       sync.Mutex
	disabled bool
	width    int
	tr       *glamour.TermRenderer
}

// newMDRenderer builds the commit-time markdown renderer. colorEnabled=false
// disables glamour styling so committed blocks pass through as raw markdown.
func newMDRenderer(colorEnabled bool) *mdRenderer {
	return &mdRenderer{disabled: !colorEnabled}
}

// setWidth updates the word-wrap width used for subsequent renders (fanned out
// from the surface's tea.WindowSizeMsg). A changed width invalidates the cached
// renderer so the next render rebuilds at the new width.
func (r *mdRenderer) setWidth(w int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w != r.width {
		r.width = w
		r.tr = nil
	}
}

// render turns a complete markdown block into styled terminal output. It
// returns the input unchanged when disabled, when the block is empty, or when
// glamour fails to build/render. The raw text is always a safe fallback, so a
// rendering error never drops the assistant's words.
func (r *mdRenderer) render(md string) string {
	if md == "" {
		return md
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled {
		return md
	}
	if r.tr == nil {
		opts := []glamour.TermRendererOption{glamour.WithAutoStyle()}
		if r.width > 0 {
			opts = append(opts, glamour.WithWordWrap(r.width))
		}
		tr, err := glamour.NewTermRenderer(opts...)
		if err != nil {
			return md
		}
		r.tr = tr
	}
	out, err := r.tr.Render(md)
	if err != nil {
		return md
	}
	// glamour brackets output with blank lines and a trailing newline; trim so a
	// committed block sits flush with its neighbours in the transcript.
	return strings.Trim(out, "\n")
}
