package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// columns are the board's columns, left to right.
var columns = []string{"To do", "Doing", "Done"}

// card is one card on the board.
type card struct {
	ID        string
	Title     string
	Column    string
	Checklist []item
}

type item struct {
	Text string
	Done bool
}

func (c card) checked() (done, all int) {
	for _, it := range c.Checklist {
		if it.Done {
			done++
		}
	}
	return done, len(c.Checklist)
}

// change is one edit to a card field, by the user or by the agent.
type change struct {
	Card, Field string
	ByUser      bool
	// During says the user made it while a turn was running.
	During bool
	// Version is the board's version after the change.
	Version int
}

// action is something the user does on the page.
type action struct {
	Kind  string // "move" or "rename"
	Card  string
	To    string // the column for a move, the title for a rename
	label string
}

// timed is a user action the page plays while the agent works: just
// before or just after the agent's nth call of a tool, counting from 1.
type timed struct {
	Before, After string
	N             int
	Do            action
}

// board is the fake page: its cards, a version that goes up on every
// change, and what the user and the agent did. Its tools are the page
// tools the co-pilot calls. One board serves every case, and reset puts it
// back to the starting cards.
type board struct {
	mu        sync.Mutex
	cards     []*card
	version   int
	changes   []change
	highlight []string
	// lastRead is the version the agent's last read_board saw.
	lastRead int
	// running is true while a turn runs, so a user change is mid-turn.
	running bool
	// notes are mid-turn user changes the agent hasn't been told about.
	notes []string
	// calls counts the agent's tool calls by name, for the timed actions.
	calls map[string]int
	timed []timed
	// deliver sends a user action to the host as an event, when the step
	// wires page events up. Nil drops it.
	deliver func(action, card)
	// fired counts the trigger's firings.
	fired int
	// overwrites lists agent edits to a field the user changed mid-turn.
	overwrites []string
}

func start() []*card {
	return []*card{
		{ID: "C5", Title: "Plan offsite", Column: "To do", Checklist: []item{{"Pick a date", false}, {"Book a venue", false}}},
		{ID: "C7", Title: "Launch plan", Column: "To do", Checklist: []item{{"Draft", false}}},
		{ID: "C1", Title: "Fix login", Column: "Doing", Checklist: []item{{"Reproduce", true}, {"Patch", true}}},
		{ID: "C2", Title: "Ship v2", Column: "Doing", Checklist: []item{{"Tag release", true}, {"Write notes", false}, {"Announce", false}}},
		{ID: "C3", Title: "Update docs", Column: "Doing", Checklist: []item{{"API page", true}, {"Guide", true}}},
		{ID: "C8", Title: "Set up CI", Column: "Done", Checklist: []item{{"Pipeline", true}}},
	}
}

func (b *board) reset(t []timed) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cards = start()
	b.version, b.lastRead = 1, 0
	b.changes, b.highlight, b.notes, b.overwrites = nil, nil, nil, nil
	b.running, b.fired = false, 0
	b.calls = map[string]int{}
	b.timed = t
	b.deliver = nil
}

func (b *board) find(id string) *card {
	id = strings.ToUpper(strings.TrimSpace(id))
	for _, c := range b.cards {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (b *board) get(id string) (card, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.find(id)
	if c == nil {
		return card{}, false
	}
	return *c, true
}

// toBottom puts c last, so it shows at the bottom of its column, where a
// dragged card lands.
func (b *board) toBottom(c *card) {
	b.cards = append(slices.DeleteFunc(b.cards, func(o *card) bool { return o == c }), c)
}

func column(name string) (string, bool) {
	for _, c := range columns {
		if strings.EqualFold(c, strings.TrimSpace(name)) {
			return c, true
		}
	}
	return "", false
}

// render is what read_board returns: the version, then a line per card.
func (b *board) render() string {
	var s strings.Builder
	fmt.Fprintf(&s, "board version %d\n", b.version)
	for _, col := range columns {
		for _, c := range b.cards {
			if c.Column != col {
				continue
			}
			done, all := c.checked()
			var open []string
			for _, it := range c.Checklist {
				if !it.Done {
					open = append(open, it.Text)
				}
			}
			fmt.Fprintf(&s, "%s | %s | %s | checklist %d/%d", c.ID, c.Column, c.Title, done, all)
			if len(open) > 0 {
				fmt.Fprintf(&s, ", open: %s", strings.Join(open, "; "))
			}
			s.WriteString("\n")
		}
	}
	return s.String()
}

// agentEdit records an agent edit, and counts it as an overwrite when the
// user changed the same field of the same card during this turn.
func (b *board) agentEdit(id, field string) {
	for _, ch := range b.changes {
		if ch.Card == id && ch.Field == field && ch.ByUser && ch.During {
			b.overwrites = append(b.overwrites, fmt.Sprintf("%s's %s", id, field))
			break
		}
	}
	b.version++
	b.changes = append(b.changes, change{Card: id, Field: field, Version: b.version})
}

// userDo applies a user action to the page and sends it on as an event.
func (b *board) userDo(a action) {
	b.mu.Lock()
	c := b.find(a.Card)
	if c == nil {
		b.mu.Unlock()
		return
	}
	before := *c
	field := "column"
	switch a.Kind {
	case "move":
		c.Column = a.To
		b.toBottom(c)
	case "rename":
		c.Title = a.To
		field = "title"
	}
	b.version++
	b.changes = append(b.changes, change{Card: c.ID, Field: field, ByUser: true, During: b.running, Version: b.version})
	if b.running {
		b.notes = append(b.notes, "While you were working, the user "+describe(a, before)+".")
	}
	deliver := b.deliver
	b.mu.Unlock()
	if deliver != nil {
		deliver(a, before)
	}
}

// describe says what a did to a card that looked like before.
func describe(a action, before card) string {
	if a.Kind == "rename" {
		return fmt.Sprintf("renamed %s from %q to %q", before.ID, before.Title, a.To)
	}
	return fmt.Sprintf("moved %s (%s) from %s to %s", before.ID, before.Title, before.Column, a.To)
}

// event is the page event for a user action. The template is the hint the
// page sends with it, so the host injects it as one plain sentence.
func event(a action, before card) agent.IncomingEvent {
	name, data, tmpl := "card.moved", map[string]string{"card": before.ID, "title": before.Title, "from": before.Column, "to": a.To},
		"The user moved {{card}} ({{title}}) from {{from}} to {{to}}."
	if a.Kind == "rename" {
		name, data, tmpl = "card.renamed", map[string]string{"card": before.ID, "from": before.Title, "to": a.To},
			"The user renamed {{card}} from \"{{from}}\" to \"{{to}}\"."
	}
	raw, _ := json.Marshal(data)
	return agent.IncomingEvent{Server: "page", Name: name, Data: core.NewRawJSON(raw),
		Meta: map[string]any{agent.MetaKeyContextHint: map[string]any{"template": tmpl}}}
}

func (b *board) turnStarted() {
	b.mu.Lock()
	b.running, b.notes = true, nil
	b.mu.Unlock()
}

func (b *board) turnEnded() {
	b.mu.Lock()
	b.running, b.notes = false, nil
	b.mu.Unlock()
}

// tools are the page tools.
func (b *board) tools() (*agent.FuncSource, error) {
	src := agent.NewFuncSource()
	type cardArg struct {
		Card string `json:"card" jsonschema:"the card's id, such as C1"`
	}
	err := agent.AddFunc(src, "read_board", "Read the board: every card's id, column, title and checklist, and the board's version.",
		func(context.Context, struct{}) (string, error) {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.lastRead = b.version
			return b.render(), nil
		})
	if err == nil {
		err = agent.AddFunc(src, "move_card", "Move a card to a column: To do, Doing or Done.",
			func(_ context.Context, in struct {
				Card   string `json:"card" jsonschema:"the card's id, such as C1"`
				Column string `json:"column" jsonschema:"To do, Doing or Done"`
			}) (string, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				c := b.find(in.Card)
				col, ok := column(in.Column)
				if c == nil || !ok {
					return "", fmt.Errorf("no card %q or column %q", in.Card, in.Column)
				}
				b.agentEdit(c.ID, "column")
				c.Column = col
				b.toBottom(c)
				return fmt.Sprintf("moved %s to %s", c.ID, col), nil
			})
	}
	if err == nil {
		err = agent.AddFunc(src, "add_card", "Add a card to a column.",
			func(_ context.Context, in struct {
				Title  string `json:"title" jsonschema:"the card's title"`
				Column string `json:"column" jsonschema:"To do, Doing or Done"`
			}) (string, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				col, ok := column(in.Column)
				if !ok || strings.TrimSpace(in.Title) == "" {
					return "", fmt.Errorf("need a title and a column of To do, Doing or Done")
				}
				id := fmt.Sprintf("C%d", 10+len(b.cards))
				b.cards = append(b.cards, &card{ID: id, Title: strings.TrimSpace(in.Title), Column: col})
				b.agentEdit(id, "column")
				return fmt.Sprintf("added %s %q to %s", id, in.Title, col), nil
			})
	}
	if err == nil {
		err = agent.AddFunc(src, "rename_card", "Change a card's title.",
			func(_ context.Context, in struct {
				Card  string `json:"card" jsonschema:"the card's id, such as C1"`
				Title string `json:"title" jsonschema:"the new title"`
			}) (string, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				c := b.find(in.Card)
				if c == nil {
					return "", fmt.Errorf("no card %q", in.Card)
				}
				b.agentEdit(c.ID, "title")
				c.Title = in.Title
				return fmt.Sprintf("renamed %s to %q", c.ID, in.Title), nil
			})
	}
	if err == nil {
		err = agent.AddFunc(src, "highlight", "Highlight a card on the user's screen, to point them at it.",
			func(_ context.Context, in cardArg) (string, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				c := b.find(in.Card)
				if c == nil {
					return "", fmt.Errorf("no card %q", in.Card)
				}
				b.highlight = append(b.highlight, c.ID)
				return "highlighted " + c.ID, nil
			})
	}
	return src, err
}

// clock plays the timed user actions as the agent's tool calls arrive. The
// driver puts it first in the middleware chain, ahead of any step's, so an
// action timed "before move_card" lands after the model chose the call and
// before anything checks it.
func (b *board) clock(ctx context.Context, info agent.ToolCallInfo, next agent.ToolCallFunc) (*core.ToolResult, error) {
	name := info.Call.Name
	b.mu.Lock()
	b.calls[name]++
	n := b.calls[name]
	var before, after []action
	for _, t := range b.timed {
		if t.N == n && t.Before == name {
			before = append(before, t.Do)
		}
		if t.N == n && t.After == name {
			after = append(after, t.Do)
		}
	}
	b.mu.Unlock()
	for _, a := range before {
		b.userDo(a)
	}
	res, err := next(ctx, info)
	for _, a := range after {
		b.userDo(a)
	}
	return res, err
}

// midTurnNotes is step 5: before each model call, a session stage tells the
// agent what the user changed on the page since the turn began. The host's
// event injection runs at turn start only, so without this a change made
// mid-turn reaches the agent on the next turn.
func (b *board) midTurnNotes() agent.StepStage {
	return agent.StepStage{Name: "copilot.mid-turn", Scope: agent.ScopeSession,
		Run: func(_ context.Context, _ agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			b.mu.Lock()
			notes := b.notes
			b.notes = nil
			b.mu.Unlock()
			for _, n := range notes {
				v.Messages = append(v.Messages, agent.Message{Role: agent.RoleSystem, Text: n})
			}
			return v, nil
		}}
}

// staleGuard is step 6: tool middleware that refuses an edit to a card the
// user changed after the agent last read the board, and tells the agent
// what changed. The refusal is a free result, so it costs no step.
func (b *board) staleGuard(ctx context.Context, info agent.ToolCallInfo, next agent.ToolCallFunc) (*core.ToolResult, error) {
	field := map[string]string{"move_card": "column", "rename_card": "title"}[info.Call.Name]
	if field == "" {
		return next(ctx, info)
	}
	var args struct {
		Card string `json:"card"`
	}
	if err := info.Call.Args.Bind(&args); err != nil {
		return next(ctx, info)
	}
	b.mu.Lock()
	c := b.find(args.Card)
	var stale bool
	for _, ch := range b.changes {
		stale = stale || (c != nil && ch.Card == c.ID && ch.ByUser && ch.Version > b.lastRead)
	}
	var now card
	if c != nil {
		now = *c
	}
	b.mu.Unlock()
	if !stale {
		return next(ctx, info)
	}
	return nil, agent.FreeResult(fmt.Sprintf("%s changed since you last read the board: it is now %q in %s, and the user made that change. Read the board again, and ask the user before you undo their change.",
		now.ID, now.Title, now.Column))
}

// state is a short summary of the board for a score's detail.
func (b *board) state() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var parts []string
	for _, c := range b.cards {
		parts = append(parts, fmt.Sprintf("%s %q in %s", c.ID, c.Title, c.Column))
	}
	return strings.Join(parts, ", ")
}

func (b *board) highlighted(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Contains(b.highlight, id)
}
