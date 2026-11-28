package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/examples/agents/copilot"
	"github.com/panyam/mcpkit/core"
)

// stubModel stands in for a real model under -stub, so the browser test in
// surfaces/web/web/e2e runs without one. It answers only the done-check
// trigger's turn. The turn reads the board, finds a card in Done with open
// items, moves it back to Doing and highlights it, then names the open
// items. A typed message gets a fixed reply.
type stubModel struct{}

func (stubModel) turn(req agent.ProviderRequest) agent.StubTurn {
	start := -1
	for i, m := range req.Messages {
		if m.Role == agent.RoleUser || m.Role == agent.RoleSystem && m.Text == copilot.DoneCheck {
			start = i
		}
	}
	if start < 0 || req.Messages[start].Role == agent.RoleUser {
		return agent.StubTurn{Text: "I only check cards that land in Done."}
	}
	var board string
	moved := false
	for _, m := range req.Messages[start+1:] {
		if m.Role != agent.RoleTool {
			continue
		}
		if strings.HasPrefix(m.Text, "board version") {
			board = m.Text
		}
		if strings.HasPrefix(m.Text, "highlighted") {
			moved = true
		}
	}
	if board == "" {
		return call("r1", "read_board", `{}`)
	}
	card, open := doneWithOpenItems(board)
	switch {
	case card == "":
		return agent.StubTurn{Text: "Every card in Done is finished."}
	case !moved:
		return agent.StubTurn{ToolCalls: []agent.ToolCall{
			toolCall("m1", "move_card", fmt.Sprintf(`{"card":%q,"column":"Doing"}`, card)),
			toolCall("h1", "highlight", fmt.Sprintf(`{"card":%q}`, card)),
		}}
	default:
		return agent.StubTurn{Text: fmt.Sprintf("I moved %s back to Doing. Still open: %s.", card, open)}
	}
}

// doneWithOpenItems reads read_board's text. It returns the first card in
// Done with open items, and those items.
func doneWithOpenItems(board string) (card, open string) {
	for _, line := range strings.Split(board, "\n") {
		f := strings.Split(line, " | ")
		if len(f) == 4 && f[1] == "Done" {
			if _, items, ok := strings.Cut(f[3], ", open: "); ok {
				return f[0], items
			}
		}
	}
	return "", ""
}

func toolCall(id, name, args string) agent.ToolCall {
	return agent.ToolCall{ID: id, Name: name, Args: core.NewRawJSON(json.RawMessage(args))}
}

func call(id, name, args string) agent.StubTurn {
	return agent.StubTurn{ToolCalls: []agent.ToolCall{toolCall(id, name, args)}}
}

func (s stubModel) Stream(ctx context.Context, req agent.ProviderRequest) (agent.Stream, error) {
	return agent.NewStubProvider(s.turn(req)).Stream(ctx, req)
}

func (s stubModel) Generate(ctx context.Context, req agent.ProviderRequest) (*agent.ProviderResponse, error) {
	return agent.NewStubProvider(s.turn(req)).Generate(ctx, req)
}
