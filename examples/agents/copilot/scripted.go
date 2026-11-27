package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Scripted is a stand-in model for CI. It reads the board before it acts
// and does what the user asks, and it has one gap for each step to close:
//
//   - With no page tools it can only tell the user to drag the card.
//   - It knows what the user just did only from an event note in its
//     context, so without page events it guesses or asks.
//   - It never checks a card in Done on its own, so only the trigger
//     catches a card moved there with open items.
//   - It plans from the board it read, so a change the user makes after
//     that is lost unless a mid-turn note tells it.
//   - It doesn't re-read the board just before an edit, so only the
//     stale-edit guard stops it overwriting a change made in that gap.
//
// So each step passes more cases in CI, with no model. Only a live run says
// what a step does for a real one.
func Scripted() agent.Provider { return scripted{} }

type scripted struct{}

func (s scripted) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return agent.NewStubProvider(s.reply(req)).Stream(ctx, req)
}

func (s scripted) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return agent.NewStubProvider(s.reply(req)).Generate(ctx, req)
}

var (
	boardLine   = regexp.MustCompile(`(?m)^(C\d+) \| ([^|]+) \| ([^|]+) \| checklist (\d+)/(\d+)`)
	movedNote   = regexp.MustCompile(`The user moved (C\d+) \(.*?\) from .*? to (To do|Doing|Done)\.`)
	renamedNote = regexp.MustCompile(`The user renamed (C\d+) from`)
	userNote    = regexp.MustCompile(`The user (?:moved|renamed) (C\d+)`)
	moveAsk     = regexp.MustCompile(`Move "(.+)" to (To do|Doing|Done)`)
	addAsk      = regexp.MustCompile(`Add a card "(.+)" to (To do|Doing|Done)`)
	everyAsk    = regexp.MustCompile(`Move every card in (To do|Doing|Done) to (To do|Doing|Done)`)
	renameAsk   = regexp.MustCompile(`Rename (C\d+) to "(.+)"`)
)

type row struct {
	id, column, title string
	done, all         int
}

func parseBoard(text string) []row {
	var out []row
	for _, m := range boardLine.FindAllStringSubmatch(text, -1) {
		d, _ := strconv.Atoi(m[4])
		a, _ := strconv.Atoi(m[5])
		out = append(out, row{id: m[1], column: strings.TrimSpace(m[2]), title: strings.TrimSpace(m[3]), done: d, all: a})
	}
	return out
}

func (s scripted) reply(req agent.ProviderRequest) agent.StubTurn {
	// The turn starts at the last user message, or at the trigger's
	// instructions when a trigger started it.
	start, trigger := -1, false
	// midTurn holds the notes that arrived after the turn started, which
	// only the host's critical events send.
	var notes, midTurn []string
	for i, m := range req.Messages {
		switch {
		case m.Role == agent.RoleUser:
			start, trigger, midTurn = i, false, nil
		case m.Role == agent.RoleSystem && strings.Contains(m.Text, "A card just landed in Done"):
			start, trigger, midTurn = i, true, nil
		case m.Role == agent.RoleSystem:
			notes = append(notes, m.Text)
			if start >= 0 {
				midTurn = append(midTurn, m.Text)
			}
		}
	}
	if !slices.ContainsFunc(req.Tools, func(t core.ToolDef) bool { return t.Name == "read_board" }) {
		return agent.StubTurn{Text: "I can't change the board from here, but you can drag the card yourself."}
	}
	var asked string
	if start >= 0 {
		asked = req.Messages[start].Text
	}
	var boardText, lastTool string
	read, edited := false, false
	for _, m := range req.Messages[start+1:] {
		for _, tc := range m.ToolCalls {
			read = read || tc.Name == "read_board"
			edited = edited || tc.Name != "read_board"
		}
		if m.Role == agent.RoleTool {
			lastTool = m.Text
			if strings.HasPrefix(m.Text, "board version") {
				boardText = m.Text
			}
		}
	}
	if edited {
		if strings.Contains(lastTool, "changed since you last read") {
			return agent.StubTurn{Text: "You changed that card just now, so I left it as you have it. Shall I make my change anyway?"}
		}
		return agent.StubTurn{Text: "Done."}
	}
	if !read {
		return call("read_board", nil)
	}
	board := parseBoard(boardText)
	byID := func(id string) (row, bool) {
		i := slices.IndexFunc(board, func(r row) bool { return r.id == id })
		if i < 0 {
			return row{}, false
		}
		return board[i], true
	}
	// lastMovedToDone is the card the latest event note says the user put
	// in Done.
	lastMovedToDone := ""
	lastRenamed := ""
	for _, n := range notes {
		for _, m := range movedNote.FindAllStringSubmatch(n, -1) {
			if m[2] == "Done" {
				lastMovedToDone = m[1]
			}
		}
		if m := renamedNote.FindStringSubmatch(n); m != nil {
			lastRenamed = m[1]
		}
	}
	switch {
	case trigger:
		r, ok := byID(lastMovedToDone)
		if !ok {
			return agent.StubTurn{Text: "I couldn't tell which card landed in Done."}
		}
		if r.done < r.all {
			return calls(
				tool("move_card", map[string]string{"card": r.id, "column": "Doing"}),
				tool("highlight", map[string]string{"card": r.id}))
		}
		return agent.StubTurn{Text: fmt.Sprintf("%s's checklist is complete, so it stays in Done.", r.id)}
	case everyAsk.MatchString(asked):
		m := everyAsk.FindStringSubmatch(asked)
		var told []string
		for _, n := range midTurn {
			if mm := userNote.FindStringSubmatch(n); mm != nil {
				told = append(told, mm[1])
			}
		}
		var cs []agent.ToolCall
		for _, r := range board {
			if r.column == m[1] && !slices.Contains(told, r.id) {
				cs = append(cs, tool("move_card", map[string]string{"card": r.id, "column": m[2]}))
			}
		}
		if len(cs) == 0 {
			return agent.StubTurn{Text: "There's nothing in " + m[1] + "."}
		}
		return calls(cs...)
	case moveAsk.MatchString(asked):
		m := moveAsk.FindStringSubmatch(asked)
		for _, r := range board {
			if strings.EqualFold(r.title, m[1]) {
				return call("move_card", map[string]string{"card": r.id, "column": m[2]})
			}
		}
		return agent.StubTurn{Text: "I can't find that card."}
	case addAsk.MatchString(asked):
		m := addAsk.FindStringSubmatch(asked)
		return call("add_card", map[string]string{"title": m[1], "column": m[2]})
	case renameAsk.MatchString(asked):
		m := renameAsk.FindStringSubmatch(asked)
		return call("rename_card", map[string]string{"card": m[1], "title": m[2]})
	case strings.Contains(asked, "just finished"):
		id := lastMovedToDone
		if id == "" {
			for _, r := range board {
				if r.column == "Done" {
					id = r.id
					break
				}
			}
		}
		return call("highlight", map[string]string{"card": id})
	case strings.Contains(asked, "just renamed"):
		if lastRenamed == "" {
			return agent.StubTurn{Text: "Which card do you mean?"}
		}
		return call("move_card", map[string]string{"card": lastRenamed, "column": "Doing"})
	}
	return agent.StubTurn{Text: "OK."}
}

var callID atomic.Int64

func tool(name string, args map[string]string) agent.ToolCall {
	raw := []byte("{}")
	if args != nil {
		raw, _ = json.Marshal(args)
	}
	return agent.ToolCall{ID: fmt.Sprintf("c%d", callID.Add(1)), Name: name, Args: core.NewRawJSON(raw)}
}

func call(name string, args map[string]string) agent.StubTurn {
	return calls(tool(name, args))
}

func calls(cs ...agent.ToolCall) agent.StubTurn { return agent.StubTurn{ToolCalls: cs} }
