package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

// The notebook surface (--ui notebook) is the opt-in alt-screen alternative to
// the default inline TUI. It gives up the native terminal scroll and
// copy/paste of the inline surface. In return it gets two things only a managed
// alt-screen can offer. The viewport has its own scroll, which fixes the
// inline-mode scrollback limitation, and the transcript is made of collapsible
// cells. Like tui.go it is presentation only. Every action routes through the
// App (Dispatch / RunTurn / Commands).

// nbCellMsg appends a finished cell (a boundary segment: an assistant turn, a
// command result, an info line) to the notebook.
type nbCellMsg nbCell

// nbLiveMsg carries the growing in-flight turn (the not-yet-finished assistant
// cell) so it renders live at the bottom.
type nbLiveMsg string

// liveFlushMsg is the debounce tick that re-renders the viewport for the
// accumulated live text. Streaming sends one nbLiveMsg per token, and each one
// re-renders the whole transcript. Doing that per token starves keystroke
// render frames, so the model coalesces them behind this tick. This fixed the
// "alternate keystrokes swallowed while streaming" report.
type liveFlushMsg struct{}

// liveFlushInterval caps live re-renders during streaming at about 25 fps.
// That is smooth to read, and slow enough that typing is still responsive.
const liveFlushInterval = 40 * time.Millisecond

// nbCell is one collapsible block in the transcript. rendered holds the
// glamour-formatted body for assistant cells (issue 1063 B2). body keeps the
// raw text, so the collapsed snippet and fold logic read plainly. Empty
// rendered means "display body verbatim" (tool / info / user cells).
type nbCell struct {
	label     string // "you" / "assistant" / "command" / "info" / "error"
	body      string
	rendered  string
	collapsed bool
}

// nbObserver is the host.Observer for notebook mode. It reuses the terminal
// renderer by writing into a buffer, so cell bodies format identically to the
// other surfaces. Then it splits the stream into cells at HostEvent
// boundaries. The streaming turn (HostRunnerEvent) accumulates and renders
// live. A non-runner event closes it into a labeled cell.
type nbObserver struct {
	mu   sync.Mutex
	buf  *bytes.Buffer
	term host.Observer
	prog *tea.Program

	md       *mdRenderer
	renderMD func(string) string // == md.render; swapped in tests

	// openTools/toolLabel track the current tool cell. A turn is split so each
	// tool call (or a parallel batch) folds on its own, and the text before and
	// after it become separate assistant cells. openTools counts in-flight
	// calls, so concurrent tool-begins group into one cell.
	openTools int
	toolLabel string
}

func newNBObserver(colorEnabled bool) *nbObserver {
	buf := &bytes.Buffer{}
	md := newMDRenderer(colorEnabled)
	return &nbObserver{buf: buf, term: host.NewTerminalRendererColor(buf, colorEnabled), md: md, renderMD: md.render}
}

// setWidth fans the terminal width to the markdown renderer so assistant cells
// word-wrap to the current width (called from tea.WindowSizeMsg).
func (s *nbObserver) setWidth(w int) { s.md.setWidth(w) }

// cell builds a boundary cell, rendering assistant prose through glamour once
// (issue 1063 B2). Non-assistant cells (tools, commands, info) pass through.
// Used for the text-before-a-tool assistant cell, which carries no footer.
func (s *nbObserver) cell(label, body string) nbCellMsg {
	c := nbCellMsg{label: label, body: strings.TrimRight(body, "\n")}
	if label == "assistant" {
		c.rendered = s.renderMD(c.body)
	}
	return c
}

// assistantCell builds the turn-final assistant cell. It renders the
// accumulated prose through glamour (issue 1063 B2) and appends the dim turn
// footer verbatim after it. Glamour must never see the ANSI footer.
func (s *nbObserver) assistantCell(prose, footer string) nbCellMsg {
	prose = strings.TrimRight(prose, "\n")
	footer = strings.TrimRight(footer, "\n")
	body, rendered := prose, s.renderMD(prose)
	if footer != "" {
		if body != "" {
			body += "\n"
			rendered += "\n"
		}
		body += footer
		rendered += footer
	}
	return nbCellMsg{label: "assistant", body: body, rendered: rendered}
}

func (s *nbObserver) On(ev host.HostEvent) {
	msgs := s.render(ev)
	s.mu.Lock()
	prog := s.prog
	s.mu.Unlock()
	if prog == nil {
		return
	}
	for _, m := range msgs {
		prog.Send(m)
	}
}

// render folds one HostEvent into tea messages for the model. It commits cells
// at boundaries and sends live updates in between. It is separate from On, so
// tests can assert the cell split without a running program.
func (s *nbObserver) render(ev host.HostEvent) []tea.Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	var msgs []tea.Msg
	seg := func() string { return strings.TrimRight(s.buf.String(), "\n") }

	if ev.Kind == host.HostSubAgentEvent {
		// Nested sub-agent activity accumulates into the currently open cell.
		// During a sub-agent call that is the ⚙ delegation tool cell. So the
		// tree-gutter lines of the sub-agent nest inside that cell instead of
		// spawning separate info cells (issue 1063 B4).
		s.term.On(ev)
		return append(msgs, nbLiveMsg(seg()))
	}

	if ev.Kind == host.HostRunnerEvent {
		re := ev.RunnerEvent
		// The first tool-begin of a batch cuts the accumulated text into its
		// own assistant cell. Then the tool renders into a fresh buffer, and a
		// matching tool-end flushes that buffer as a ⚙ cell.
		if re.Kind == agent.EventToolBegin {
			if s.openTools == 0 {
				if txt := seg(); txt != "" {
					msgs = append(msgs, s.cell("assistant", txt))
				}
				s.buf.Reset()
				s.toolLabel = "⚙"
				if re.ToolCall != nil {
					s.toolLabel = "⚙ " + re.ToolCall.Name
				}
			} else {
				s.toolLabel = "⚙ tools"
			}
			s.openTools++
		}

		// Reasoning renders as its own verbatim cell. The terminal renderer dims
		// it with ANSI. If that text were folded into the glamoured assistant
		// prose, glamour would garble the escape codes into literal "[2m" text.
		// Glamour must never see ANSI. So cut the prose so far at begin, and
		// close a verbatim "thinking" cell at end. A step line (EventStep) comes
		// after the step's text and is dim ANSI as well. It gets the same cut,
		// then closes as its own verbatim cell. That cell is collapsed, so only
		// its header shows the line.
		if re.Kind == agent.EventThinkingBegin || re.Kind == agent.EventStep {
			if txt := seg(); txt != "" {
				msgs = append(msgs, s.cell("assistant", txt))
			}
			s.buf.Reset()
		}

		s.term.On(ev) // render this event into buf

		switch re.Kind {
		case agent.EventThinkingEnd:
			msgs = append(msgs, s.cell("thinking", seg())) // verbatim: cell() only glamours "assistant"
			s.buf.Reset()
		case agent.EventStep:
			c := s.cell("step", seg())
			c.collapsed = true
			msgs = append(msgs, c)
			s.buf.Reset()
		case agent.EventToolEnd, agent.EventToolError, agent.EventToolDenied, agent.EventToolCancelled, agent.EventToolUnavailable:
			if s.openTools > 0 {
				s.openTools--
			}
			if s.openTools == 0 {
				msgs = append(msgs, s.cell(s.toolLabel, seg()))
				s.buf.Reset()
			} else {
				msgs = append(msgs, nbLiveMsg(seg()))
			}
		default:
			msgs = append(msgs, nbLiveMsg(seg()))
		}
	} else {
		// non-runner event = a discrete boundary (turn done, command, info)
		before := s.buf.Len()
		s.term.On(ev)
		if nbLabelFor(ev.Kind) == "assistant" {
			// The assistant's prose accumulated before this event, and the
			// event's own output is the dim turn footer. Split them so glamour
			// renders only the prose, never the ANSI footer.
			full := s.buf.String()
			msgs = append(msgs, s.assistantCell(full[:before], full[before:]))
		} else {
			msgs = append(msgs, s.cell(nbLabelFor(ev.Kind), seg()))
		}
		s.buf.Reset()
		if ev.Kind == host.HostTurnDone && ev.Result != nil {
			msgs = append(msgs, usageMsg{in: ev.Result.Usage.InputTokens, out: ev.Result.Usage.OutputTokens})
		}
	}
	return msgs
}

// nbLabelFor maps a boundary event kind to a cell label. The streaming turn
// closes on HostTurnDone, so that is the "assistant" cell. A failure gets its
// own error cell. Commands and the rest are informational.
func nbLabelFor(k host.HostEventKind) string {
	switch k {
	case host.HostTurnDone:
		return "assistant"
	case host.HostTurnFailed:
		return "error"
	case host.HostCommandResult:
		return "command"
	default:
		return "info"
	}
}

// notebookModel is the alt-screen bubbletea model. It shows a scrollable
// viewport of cells above an input, and it has two modes. Insert mode
// (default) types into the input. Nav mode (Esc) moves a selection cursor over
// cells and folds them.
type notebookModel struct {
	app     *host.App
	surface *nbObserver

	vp    viewport.Model
	ta    textarea.Model
	cells []nbCell
	live  string // the in-flight turn, always shown expanded at the bottom

	keys      appKeys
	help      help.Model
	modalHost      // an open interactive dialog (/mcp, /sessions) as the focused layer
	showAll   bool // ? toggled the full-help view

	nav     bool // false = insert, true = nav
	raw     bool // ctrl+o: show cells as raw markdown, not glamour-rendered (1083)
	sel     int  // selected cell index in nav mode
	running bool
	// sendSteer, when set, replaces App.Submit for input typed during a
	// turn; tests use it.
	sendSteer     func(line string)
	atBottom      bool // whether the viewport is scrolled to the end (for auto-follow)
	liveScheduled bool // a liveFlushMsg tick is pending (coalesces per-token renders)

	history []string
	histIdx int

	maxLines      int      // input auto-grows up to this many rows (0 = default)
	usage         usageMsg // last turn's tokens, for the status line
	window        int      // context window for the "N% left" gauge (0 = off)
	session       string   // cached RunID for the status line (refreshed off the render path)
	width, height int
	ready         bool
}

// defaultNotebookMaxLines caps the auto-growing prompt when --notebook-max-lines
// is unset.
const defaultNotebookMaxLines = 20

func newNotebookModel(app *host.App, surface *nbObserver, maxLines, window int) notebookModel {
	if maxLines <= 0 {
		maxLines = defaultNotebookMaxLines
	}
	m := notebookModel{app: app, surface: surface, ta: newPromptArea(), keys: newAppKeys(), help: help.New(), atBottom: true, maxLines: maxLines, window: window}
	m.histIdx = 0
	if app != nil {
		// Safe here: construction runs before the first turn, so turnMu is free.
		m.session = app.RunID()
	}
	return m
}

func (m notebookModel) Init() tea.Cmd { return textarea.Blink }

// relayout recomputes the split between the input and the viewport. The input
// auto-grows with its line count. It is 1 line when empty, grows up to
// maxLines, and shrinks back. The viewport takes the rest. So a long
// multi-line draft expands the prompt, and a short one leaves the transcript
// tall.
func (m *notebookModel) relayout() {
	if m.width == 0 {
		return
	}
	ih := m.ta.LineCount()
	if ih < 1 {
		ih = 1
	}
	if ih > m.maxLines {
		ih = m.maxLines
	}
	m.ta.SetWidth(m.width)
	m.ta.SetHeight(ih)
	vpH := m.height - ih - 3 // 2-line status + separator
	if vpH < 1 {
		vpH = 1
	}
	if !m.ready {
		m.vp = viewport.New(m.width, vpH)
		m.ready = true
	} else {
		m.vp.Width = m.width
		m.vp.Height = vpH
	}
}

func (m notebookModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.surface != nil {
			m.surface.setWidth(msg.Width)
		}
		m.relayout()
		m.refresh()
		return m, nil

	case nbLiveMsg:
		// Coalesce per-token updates. Record the latest live text, but debounce the
		// whole-transcript re-render behind a single pending tick, so streaming
		// does not starve keystroke render frames.
		m.live = string(msg)
		if m.liveScheduled {
			return m, nil
		}
		m.liveScheduled = true
		return m, tea.Tick(liveFlushInterval, func(time.Time) tea.Msg { return liveFlushMsg{} })

	case liveFlushMsg:
		m.liveScheduled = false
		m.refresh()
		return m, nil

	case nbCellMsg:
		if msg.body != "" {
			m.cells = append(m.cells, nbCell(msg))
		}
		m.live = ""
		m.refresh()
		return m, nil

	case turnDoneMsg:
		m.running = false
		// The turn (or dispatched command) has fully returned. So turnMu is
		// free, and a session-changing command like /sessions has finished.
		if m.app != nil {
			m.session = m.app.RunID()
		}
		return m, nil

	case usageMsg:
		m.usage = msg
		return m, nil

	case openOverlayMsg:
		m.push(msg.ov, m.width) // nests above the current overlay, if any
		m.ta.Blur()
		return m, nil

	case closeOverlaysMsg:
		if m.active() {
			m.clear()
			m.ta.Focus()
		}
		return m, nil

	case tea.MouseMsg:
		m.vp, _ = m.vp.Update(msg)
		m.atBottom = m.vp.AtBottom()
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		// An open dialog owns the keyboard. Esc pops one level, back to the
		// parent overlay or the prompt. An action dispatches a line, and its
		// result nests a new overlay or closes the stack.
		if m.active() {
			out := m.top().handleKey(msg)
			switch {
			case out.Dismiss:
				m.pop()
				if !m.active() {
					m.ta.Focus()
				}
			case out.Line != "":
				return m.submit(out.Line)
			}
			return m, nil
		}
		// ctrl+o toggles the whole transcript between rich and raw markdown, in
		// place (issue 1083). It works in both INS and NAV modes.
		if key.Matches(msg, m.keys.Raw) {
			m.raw = !m.raw
			m.refresh()
			return m, nil
		}
		if m.nav {
			return m.updateNav(msg)
		}
		// Give the input its full height budget before the edit. Then the
		// textarea's internal reposition scrolls only when content really
		// exceeds maxLines. Without this, the old, smaller height is in effect
		// during Update. A just-inserted newline briefly over-scrolls and hides
		// line 1, even though the grown box has room. relayout then shrinks the
		// box back to the actual line count.
		m.ta.SetHeight(m.maxLines)
		nm, cmd := m.updateInsert(msg)
		n := nm.(notebookModel)
		n.relayout() // the input may have grown or shrunk a line
		n.refresh()
		return n, cmd
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

// updateNav handles keys while the selection cursor is active: move the
// selection, fold/unfold, jump to ends, or return to insert mode.
func (m notebookModel) updateNav(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Help):
		m.showAll = !m.showAll
	case key.Matches(msg, m.keys.Insert):
		m.nav = false
	case key.Matches(msg, m.keys.Select):
		if s := msg.String(); (s == "up" || s == "k") && m.sel > 0 {
			m.sel--
		} else if (s == "down" || s == "j") && m.sel < len(m.cells)-1 {
			m.sel++
		}
	case key.Matches(msg, m.keys.Ends):
		if msg.String() == "g" {
			m.sel = 0
		} else {
			m.sel = len(m.cells) - 1
		}
	case key.Matches(msg, m.keys.Fold):
		if m.sel >= 0 && m.sel < len(m.cells) {
			m.cells[m.sel].collapsed = !m.cells[m.sel].collapsed
		}
	case key.Matches(msg, m.keys.Scroll):
		if msg.String() == "pgup" {
			m.vp.HalfPageUp()
		} else {
			m.vp.HalfPageDown()
		}
	}
	m.refresh()
	return m, nil
}

// updateInsert handles keys while typing: submit, complete, history, scroll the
// viewport, or enter nav mode; anything else edits the input.
func (m notebookModel) updateInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// `?` toggles full help only on an empty prompt, so it is still typeable
	// mid-message. (In NAV mode there is no typing, so `?` always toggles.)
	if key.Matches(msg, m.keys.Help) && m.ta.Value() == "" {
		m.showAll = !m.showAll
		m.refresh()
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		if len(m.cells) > 0 {
			m.nav = true
			m.sel = len(m.cells) - 1
			m.refresh()
		}
		return m, nil
	case tea.KeyEnter:
		if m.running {
			return m.steerTurn()
		}
		line := strings.TrimSpace(m.ta.Value())
		if line == "" {
			return m, nil
		}
		m.ta.Reset()
		m.history = append(m.history, line)
		m.histIdx = len(m.history)
		return m.submit(line)
	case tea.KeyTab:
		tabComplete(&m.ta, m.app)
		return m, nil
	case tea.KeyPgUp, tea.KeyPgDown:
		m.vp, _ = m.vp.Update(msg)
		m.atBottom = m.vp.AtBottom()
		return m, nil
	case tea.KeyUp:
		// Move within the prompt lines first. Up-arrow history recall and
		// transcript scroll start only at the top line.
		if m.ta.Line() > 0 {
			break
		}
		if m.recallHistory(-1) {
			return m, nil
		}
		m.vp.ScrollUp(1)
		m.atBottom = m.vp.AtBottom()
		return m, nil
	case tea.KeyDown:
		if m.ta.Line() < m.ta.LineCount()-1 {
			break
		}
		if m.recallHistory(1) {
			return m, nil
		}
		m.vp.ScrollDown(1)
		m.atBottom = m.vp.AtBottom()
		return m, nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m notebookModel) View() string {
	if !m.ready {
		return "starting…"
	}
	// An open dialog renders between the transcript and the input.
	if m.active() {
		return m.vp.View() + "\n" + m.view(m.ta.View())
	}
	return m.vp.View() + "\n" + m.statusBar() + "\n" + m.ta.View()
}

func (m notebookModel) statusBar() string {
	var hint string
	if m.showAll {
		hint = m.help.FullHelpView(m.keys.fullHelp())
	} else if m.nav {
		hint = "NAV  " + m.help.ShortHelpView(m.keys.navBar())
	} else {
		hint = "INS  " + m.help.ShortHelpView(m.keys.insertBar())
	}
	if m.raw {
		hint = "RAW  " + hint
	}
	if m.running {
		hint = "working…  " + hint
	}
	// Two-line status: the persistent model/session/context line, then keys.
	return lipgloss.NewStyle().Faint(true).Render(statusLine(m.app, m.session, m.usage, m.window) + "\n" + hint)
}

// refresh rebuilds the viewport content from the cells (+ the live turn) and,
// when the view was already at the bottom, follows the new content down.
func (m *notebookModel) refresh() {
	if !m.ready {
		return
	}
	follow := m.atBottom
	m.vp.SetContent(m.renderCells())
	if follow {
		m.vp.GotoBottom()
		m.atBottom = true
	}
}

// renderCells lays out every cell. Each cell gets a header with a fold marker
// and label, and an indented body unless it is collapsed. In nav mode it
// highlights the selected cell. Then it appends the in-flight turn.
func (m notebookModel) renderCells() string {
	var b strings.Builder
	rule := m.hrule()
	for i, c := range m.cells {
		if i > 0 {
			b.WriteString(rule)
			b.WriteString("\n")
		}
		marker := "▾"
		if c.collapsed {
			marker = "▸"
		}
		// A leading cursor column marks the nav selection with a glyph, not
		// color/attribute alone. NO_COLOR and a dumb terminal strip Reverse,
		// and the glyph keeps the selected cell visible there (issue 1063 E3).
		cursor := "  "
		if m.nav && i == m.sel {
			cursor = "▌ "
		}
		header := cursor + marker + " " + c.label
		if c.collapsed {
			header += " · " + snippet(firstLine(c.body), 70)
		}
		if m.nav && i == m.sel {
			header = lipgloss.NewStyle().Reverse(true).Render(header)
		}
		b.WriteString(header)
		b.WriteString("\n")
		if !c.collapsed && c.body != "" {
			display := c.body
			// ctrl+o (m.raw) shows raw markdown for copy fidelity. Otherwise the
			// cell shows its glamour-rendered form when it has one (issue 1083).
			if c.rendered != "" && !m.raw {
				display = c.rendered
			}
			b.WriteString(indentBlock(wrapCell(display, m.contentWidth())))
			b.WriteString("\n")
		}
	}
	if m.live != "" {
		if len(m.cells) > 0 {
			b.WriteString(rule)
			b.WriteString("\n")
		}
		b.WriteString("▾ assistant\n")
		b.WriteString(indentBlock(wrapCell(m.live, m.contentWidth())))
	}
	return b.String()
}

// contentWidth is the viewport width for the transcript. It falls back to the
// last window width, then to a floor. Both the hrule and cell wrapping measure
// against this one width.
func (m notebookModel) contentWidth() int {
	w := m.vp.Width
	if w <= 0 {
		w = m.width
	}
	if w <= 0 {
		w = 40
	}
	return w
}

// hrule is a faint full-width horizontal delimiter drawn between cells.
func (m notebookModel) hrule() string {
	return lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", m.contentWidth()))
}

// wrapCell soft-wraps a cell body to the viewport width before it is indented.
// The viewport does not wrap on its own. Without this, a wide line is clipped
// at the right edge, such as a long error, unwrapped tool output, or raw JSON.
// ansi.Wrap preserves styling, is grapheme-width aware, and hard-breaks tokens
// longer than the width (JSON with no spaces). indentBlock adds a two-space
// indent, so the width is reduced by two.
func wrapCell(s string, width int) string {
	w := width - 2
	if w < 1 {
		w = 1
	}
	return ansi.Wrap(s, w, "")
}

func indentBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// steerTurn sends the typed line into the running turn (App.Submit), as the
// inline TUI does, and shows it as its own cell. Slash commands wait for the
// turn to end.
func (m notebookModel) steerTurn() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.ta.Value())
	if line == "" || strings.HasPrefix(line, "/") {
		return m, nil
	}
	m.ta.Reset()
	m.history = append(m.history, line)
	m.histIdx = len(m.history)
	m.cells = append(m.cells, nbCell{label: "you · added to the running turn", body: line})
	m.refresh()
	send := m.sendSteer
	if send == nil {
		app := m.app
		send = func(line string) { go func() { _, _ = app.Submit(context.Background(), line, host.TurnOptions{}) }() }
	}
	send(line)
	return m, nil
}

// submit adds the user's line as a cell and runs it (command or turn) on a
// goroutine, mirroring the inline TUI. /keys is a terminal-only cheatsheet.
func (m notebookModel) submit(line string) (tea.Model, tea.Cmd) {
	if line == "/keys" {
		m.cells = append(m.cells, nbCell{label: "info", body: renderKeyHelp()})
		m.refresh()
		return m, nil
	}
	m.cells = append(m.cells, nbCell{label: "you", body: line})
	m.refresh()
	m.running = true
	app, surface := m.app, m.surface
	ctx := context.Background()
	if strings.HasPrefix(line, "/") {
		go func() {
			res, err := app.Dispatch(ctx, line)
			switch {
			case errors.Is(err, host.ErrUnknownCommand):
				surface.On(host.HostEvent{Kind: host.HostTurnFailed, Err: "unknown command " + line})
			case err != nil:
				surface.On(host.HostEvent{Kind: host.HostTurnFailed, Err: err.Error()})
			case overlayFor(res) != nil:
				surface.prog.Send(openOverlayMsg{ov: overlayFor(res)})
			default:
				surface.prog.Send(closeOverlaysMsg{})
				surface.On(host.HostEvent{Kind: host.HostCommandResult, Command: res})
				if res.Quit {
					surface.prog.Quit()
					return
				}
			}
			surface.prog.Send(turnDoneMsg{})
		}()
	} else {
		go func() {
			_ = app.RunTurn(ctx, line)
			surface.prog.Send(turnDoneMsg{})
		}()
	}
	return m, nil
}

// recallHistory mirrors the inline TUI's history recall for the notebook input.
func (m *notebookModel) recallHistory(dir int) bool {
	if len(m.history) == 0 || strings.Contains(m.ta.Value(), "\n") {
		return false
	}
	idx := m.histIdx + dir
	if idx < 0 || idx > len(m.history) {
		return false
	}
	m.histIdx = idx
	if idx == len(m.history) {
		m.ta.Reset()
	} else {
		m.ta.SetValue(m.history[idx])
		m.ta.CursorEnd()
	}
	return true
}

// runNotebook starts the alt-screen notebook program with mouse support.
// maxLines caps the auto-growing prompt.
func runNotebook(app *host.App, surface *nbObserver, maxLines, window int) error {
	prog := tea.NewProgram(newNotebookModel(app, surface, maxLines, window), tea.WithAltScreen(), tea.WithMouseCellMotion())
	surface.prog = prog
	_, err := prog.Run()
	return err
}
