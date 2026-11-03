package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/client"
)

// overlayAction is one action offered on the selected overlay row. Key is the
// single key that triggers it. Enter also triggers the primary action, which
// has an empty Key. Label names the action in the footer. When the action
// fires, the surface dispatches Line as an App command. An empty Line marks
// the action unavailable, and the footer renders it dimmed. serversOverlay
// uses that empty Line for tools on a server that isn't ready, and for login
// where no interactive login is possible.
type overlayAction struct {
	Key   string
	Label string
	Line  string
}

// overlayItem is one selectable row: a label, a dimmed detail (state, message
// count), and the actions available on it.
type overlayItem struct {
	Label   string
	Detail  string
	Actions []overlayAction
}

// overlayOutcome tells the host surface the result of handleKey. The overlay
// either dismisses, dispatches a command Line and closes, or does neither. In
// that last case it consumed the key and is still open. The surface is the
// dispatcher, so App.Dispatch is still data-only. The overlay yields a command
// string and does not call the host.
type overlayOutcome struct {
	Dismiss bool
	Line    string
}

// focusLayer is a modal component that temporarily owns the keyboard above a
// surface's base view. It consumes keys and renders itself. The overlay is the
// only implementation today. With the seam, both TUI surfaces share the
// open/route/dismiss glue instead of each special-casing *overlayModel. A
// fuller model would route the base view and overlay uniformly as peer layers.
// That model is the focus-management design-track item (issue 1063 C4).
type focusLayer interface {
	handleKey(tea.KeyMsg) overlayOutcome
	setWidth(int)
	View() string
}

// modalHost is the shared state for a surface's stack of modal layers. Both TUI
// models embed one. So "is a modal open, route keys to the top one, push a
// nested one, pop back to the parent" is defined once here rather than
// branched per surface. It is a stack rather than a single layer, so nested
// overlays get back-navigation. Opening the tools view from the servers
// overlay pushes. Esc then pops back to the servers overlay instead of
// dropping to the input (issue 1063 C4). bubbletea copies the model on each
// Update, and the layers are pointers, so they are safe across those copies.
// The surface owns the input and still handles blurring and focusing it.
type modalHost struct {
	stack []focusLayer
}

// active reports whether any modal layer currently owns the keyboard.
func (h *modalHost) active() bool { return len(h.stack) > 0 }

// top is the focused layer. Keys route to it and it renders. Only call when
// active.
func (h *modalHost) top() focusLayer { return h.stack[len(h.stack)-1] }

// push opens l as a nested modal above the current one (or the first modal when
// the stack is empty), sized to the surface width. The caller blurs its input.
func (h *modalHost) push(l focusLayer, width int) {
	if l == nil {
		return
	}
	l.setWidth(width)
	h.stack = append(h.stack, l)
}

// pop closes the top modal, revealing its parent (or the input when the stack
// empties). A no-op on an empty stack.
func (h *modalHost) pop() {
	if n := len(h.stack); n > 0 {
		h.stack = h.stack[:n-1]
	}
}

// clear closes the whole stack at once. That is the "an action completed,
// return to the prompt" path (e.g. resuming a session).
func (h *modalHost) clear() { h.stack = nil }

// setWidth reflows every layer on resize, not just the top, so a parent revealed
// by a later pop is already sized. A no-op when the stack is empty.
func (h *modalHost) setWidth(w int) {
	for _, l := range h.stack {
		l.setWidth(w)
	}
}

// view composes the top modal above the surface's base region. The caller
// passes the base in, since it differs per surface. The inline TUI passes the
// input alone, and the notebook passes the viewport plus input.
func (h *modalHost) view(base string) string {
	return h.top().View() + "\n" + base
}

// overlayModel is the reusable interactive dialog in the bottom region of a TUI
// surface (issue 1095). It is a titled, selectable list with per-row keyed
// actions, Enter for the primary action and Esc to dismiss. It is
// surface-agnostic. Both the inline TUI and the notebook embed one and route
// keys to handleKey while it is open. It is also content-agnostic. /mcp, the
// /sessions picker, and future dialogs build one from a CmdResult. Rendering
// is here in the surface, and the host is still data-only.
type overlayModel struct {
	title   string
	items   []overlayItem
	cursor  int
	top     int // first visible row (scroll offset)
	width   int
	maxRows int // visible-row cap; the list scrolls past it
}

// newOverlay builds an overlay with a bounded visible window (long lists
// scroll). An empty item list is valid. The overlay renders a placeholder and
// only Esc acts.
func newOverlay(title string, items []overlayItem) *overlayModel {
	return &overlayModel{title: title, items: items, maxRows: 10}
}

// setWidth fans the surface width to the overlay so its box reflows on resize.
func (o *overlayModel) setWidth(w int) { o.width = w }

// move shifts the selection by d (clamped), keeping the cursor inside the
// visible window by scrolling top as needed.
func (o *overlayModel) move(d int) {
	if len(o.items) == 0 {
		return
	}
	o.cursor += d
	if o.cursor < 0 {
		o.cursor = 0
	}
	if o.cursor >= len(o.items) {
		o.cursor = len(o.items) - 1
	}
	if o.cursor < o.top {
		o.top = o.cursor
	}
	if o.cursor >= o.top+o.maxRows {
		o.top = o.cursor - o.maxRows + 1
	}
}

// primary returns the selected row's Enter action (the one with Key == "").
func (o *overlayModel) primary() (overlayAction, bool) {
	if len(o.items) == 0 {
		return overlayAction{}, false
	}
	for _, a := range o.items[o.cursor].Actions {
		if a.Key == "" {
			return a, true
		}
	}
	return overlayAction{}, false
}

// handleKey advances the overlay for one keypress and reports the outcome. Esc
// dismisses. Up/Down (or k/j, Ctrl+P/N) move. Enter fires the primary action.
// Any other key fires a secondary action on the selected row when it matches
// that action's Key. An action with an empty Line is unavailable and does
// nothing.
func (o *overlayModel) handleKey(msg tea.KeyMsg) overlayOutcome {
	switch msg.String() {
	case "esc":
		return overlayOutcome{Dismiss: true}
	case "up", "k", "ctrl+p":
		o.move(-1)
	case "down", "j", "ctrl+n":
		o.move(1)
	case "enter":
		if a, ok := o.primary(); ok && a.Line != "" {
			return overlayOutcome{Line: a.Line}
		}
	default:
		if len(o.items) > 0 {
			for _, a := range o.items[o.cursor].Actions {
				if a.Key != "" && a.Key == msg.String() && a.Line != "" {
					return overlayOutcome{Line: a.Line}
				}
			}
		}
	}
	return overlayOutcome{}
}

// View renders the overlay as a bordered panel. The panel holds the title, the
// visible window of rows, and a dim key hint. The selected row is highlighted,
// and a footer lists its available actions. The height is dynamic, and the box
// grows with the visible rows.
func (o *overlayModel) View() string {
	dim := lipgloss.NewStyle().Faint(true)
	sel := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	var b strings.Builder
	b.WriteString(sel.Render(o.title))
	b.WriteString("\n")

	if len(o.items) == 0 {
		b.WriteString(dim.Render("  (nothing to show)"))
		b.WriteString("\n")
	} else {
		end := o.top + o.maxRows
		if end > len(o.items) {
			end = len(o.items)
		}
		if o.top > 0 {
			b.WriteString(dim.Render("  ↑ more"))
			b.WriteString("\n")
		}
		for i := o.top; i < end; i++ {
			it := o.items[i]
			row := fmt.Sprintf("%-24s %s", it.Label, dim.Render(it.Detail))
			if i == o.cursor {
				b.WriteString(sel.Render("▸ " + row))
			} else {
				b.WriteString("  " + row)
			}
			b.WriteString("\n")
		}
		if end < len(o.items) {
			b.WriteString(dim.Render("  ↓ more"))
			b.WriteString("\n")
		}
	}

	b.WriteString(dim.Render(o.footer()))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Render(b.String())
}

// footer lists the keys. It shows the selected row's actions and the
// always-present navigation and dismiss hints. Unavailable actions (empty
// Line) are shown too, so the user sees that the capability exists. The
// whole-footer style dims them.
func (o *overlayModel) footer() string {
	var acts []string
	if len(o.items) > 0 {
		for _, a := range o.items[o.cursor].Actions {
			k := a.Key
			if k == "" {
				k = "enter"
			}
			label := a.Label
			if a.Line == "" {
				label += " (n/a)"
			}
			acts = append(acts, k+" "+label)
		}
	}
	acts = append(acts, "↑↓ move", "esc close")
	return strings.Join(acts, " · ")
}

// serversOverlay builds the interactive /mcp (alias /servers) view from a
// CmdServers result. It has one row per MCP server, with its connection state
// as the detail, and a reconnect action on any failed or needs-login server.
// The tools action is enabled on a ready server. The login action is enabled
// on a needs-login server when an interactive login is possible.
func serversOverlay(res host.CmdResult) *overlayModel {
	items := make([]overlayItem, 0, len(res.Servers))
	for _, s := range res.Servers {
		detail := s.State.String()
		if s.Required {
			detail += " · required"
		}
		if s.Err != nil {
			detail += " · " + s.Err.Error()
		}
		var actions []overlayAction
		if canReconnect(s.State) {
			actions = append(actions, overlayAction{Label: "reconnect", Line: "/servers reconnect " + s.ID})
		} else {
			actions = append(actions, overlayAction{Label: "reconnect"}) // n/a for ready/connecting
		}
		// Tools are listable only once the server is ready (its catalog is
		// registered); otherwise the action is shown disabled.
		tools := overlayAction{Key: "t", Label: "tools"}
		if s.State == client.StateReady {
			tools.Line = "/servers tools " + s.ID
		}
		actions = append(actions, tools)
		// Login is offered on a needs-login server only when an interactive
		// (oauth) auth type is configured for it; otherwise the action is
		// disabled (there is nothing to trigger).
		if s.State == client.StateNeedsLogin {
			login := overlayAction{Key: "l", Label: "login"}
			if s.CanLogin {
				login.Line = "/servers login " + s.ID
			}
			actions = append(actions, login)
		}
		items = append(items, overlayItem{Label: s.ID, Detail: detail, Actions: actions})
	}
	return newOverlay("MCP servers", items)
}

// canReconnect reports whether a reconnect action is meaningful for a state. It
// is for a failed (backoff) or needs-login (parked) server, not a ready or
// in-flight one.
func canReconnect(s client.ConnState) bool {
	return s == client.StateFailed || s == client.StateNeedsLogin
}

// sessionsOverlay builds the interactive /sessions picker from a CmdSessions
// result. It has one row per persisted run, with message count and lineage as
// the detail. The active run is marked, and Enter resumes a run. This picker
// proves the widget is reusable (issue 1095). Resume works today, so the
// picker is fully functional.
func sessionsOverlay(res host.CmdResult) *overlayModel {
	items := make([]overlayItem, 0, len(res.Sessions))
	active := -1
	for i, s := range res.Sessions {
		detail := fmt.Sprintf("%d msg", s.MessageCount)
		if s.ID == res.RunID {
			detail += " · active"
			active = i
		}
		if s.ParentID != "" {
			detail += fmt.Sprintf(" · forked from %s @%d", s.ParentID, s.ForkPoint)
		}
		items = append(items, overlayItem{
			Label:   s.ID,
			Detail:  detail,
			Actions: []overlayAction{{Label: "resume", Line: "/resume " + s.ID}},
		})
	}
	o := newOverlay("sessions", items)
	if active >= 0 {
		o.cursor = active
		o.move(0) // clamp the scroll window onto the active row
	}
	return o
}

// toolsOverlay builds the read-only per-server tool list. The tools action of
// the /mcp overlay opens it (issue 1117). It has one row per tool (name +
// description) and no actions. Opening it from the servers overlay pushes it
// onto the modal stack, and Esc pops back to the servers overlay (issue 1063
// C4).
func toolsOverlay(res host.CmdResult) *overlayModel {
	items := make([]overlayItem, 0, len(res.Tools))
	for _, t := range res.Tools {
		items = append(items, overlayItem{Label: t.Name, Detail: t.Description})
	}
	return newOverlay("tools · "+res.ServerID, items)
}

// overlayFor converts a command result into an overlay that presents it. It
// returns nil when the result is not an interactive-picker shape. It
// centralizes the surface's "which CmdKinds open an overlay" decision, so both
// TUI surfaces share it.
func overlayFor(res host.CmdResult) *overlayModel {
	switch res.Kind {
	case host.CmdServers:
		return serversOverlay(res)
	case host.CmdSessions:
		return sessionsOverlay(res)
	case host.CmdServerTools:
		return toolsOverlay(res)
	default:
		return nil
	}
}
