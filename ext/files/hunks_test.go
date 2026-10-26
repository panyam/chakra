package files

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

const threeLines = "alpha\nbeta\ngamma\n"

func threeHunkCall(t *testing.T) agent.ToolCallInfo {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"path":        "f.txt",
		"expect_hash": Hash(threeLines),
		"edits": []map[string]string{
			{"old": "alpha", "new": "ALPHA"},
			{"old": "beta", "new": "BETA"},
			{"old": "gamma", "new": "GAMMA"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent.ToolCallInfo{Call: agent.ToolCall{ID: "c1", Name: "edit_file", Args: core.NewRawJSON(args)}}
}

// answering returns a UI that records the request it was shown and answers
// with content.
func answering(action string, content map[string]any, seen *core.ElicitationRequest) agent.ElicitationUI {
	return func(_ context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		*seen = req
		return core.ElicitationResult{Action: action, Content: content}, nil
	}
}

func TestApplyingASubsetOfHunksKeepsTheirPlaces(t *testing.T) {
	got, err := Edit{
		ExpectHash: Hash(threeLines),
		Hunks:      []Hunk{{Old: "alpha", New: "ALPHA"}, {Old: "gamma", New: "GAMMA"}},
	}.Apply(threeLines)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ALPHA\nbeta\nGAMMA\n" {
		t.Errorf("got %q", got)
	}
}

func TestChooseHunksAsksOncePerHunk(t *testing.T) {
	var seen core.ElicitationRequest
	if _, err := chooseHunks(context.Background(), threeHunkCall(t), answering("accept", nil, &seen)); err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Type    string `json:"type"`
			Default bool   `json:"default"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(seen.RequestedSchema, &schema); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"change_1", "change_2", "change_3"} {
		p, ok := schema.Properties[k]
		if !ok || p.Type != "boolean" || !p.Default {
			t.Errorf("field %s = %+v (present %v), want a boolean ticked by default", k, p, ok)
		}
	}
	for _, want := range []string{"Change 2:", "- beta", "+ BETA"} {
		if !strings.Contains(seen.Message, want) {
			t.Errorf("message is missing %q:\n%s", want, seen.Message)
		}
	}
}

func TestChooseHunksNarrowsToTheTickedOnes(t *testing.T) {
	var seen core.ElicitationRequest
	c, err := chooseHunks(context.Background(), threeHunkCall(t),
		answering("accept", map[string]any{"change_1": true, "change_2": false, "change_3": true}, &seen))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Claimed || !c.Approved {
		t.Fatalf("choice = %+v, want a claimed approval", c)
	}
	var args struct {
		Path       string              `json:"path"`
		ExpectHash string              `json:"expect_hash"`
		Edits      []map[string]string `json:"edits"`
	}
	if err := json.Unmarshal(c.Args.Raw(), &args); err != nil {
		t.Fatal(err)
	}
	if len(args.Edits) != 2 || args.Edits[0]["old"] != "alpha" || args.Edits[1]["old"] != "gamma" {
		t.Errorf("edits = %v, want alpha and gamma", args.Edits)
	}
	if args.Path != "f.txt" || args.ExpectHash != Hash(threeLines) {
		t.Errorf("other arguments changed: %+v", args)
	}
	if !strings.Contains(c.Note, "change 2 of the 3") {
		t.Errorf("note = %q, want it to name the declined change", c.Note)
	}
}

func TestChooseHunksAnswers(t *testing.T) {
	var seen core.ElicitationRequest
	cases := []struct {
		name     string
		action   string
		content  map[string]any
		approved bool
		narrowed bool
	}{
		{"accept with no fields is the whole edit", "accept", nil, true, false},
		{"all ticked is the whole edit", "accept", map[string]any{"change_1": true, "change_2": true, "change_3": true}, true, false},
		{"none ticked declines", "accept", map[string]any{"change_1": false, "change_2": false, "change_3": false}, false, false},
		{"decline declines", "decline", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := chooseHunks(context.Background(), threeHunkCall(t), answering(tc.action, tc.content, &seen))
			if err != nil {
				t.Fatal(err)
			}
			if !c.Claimed || c.Approved != tc.approved || (len(c.Args.Raw()) > 0) != tc.narrowed {
				t.Errorf("choice = %+v", c)
			}
		})
	}
}

func TestChooseHunksLeavesOtherCallsToThePlainAsk(t *testing.T) {
	ui := func(context.Context, core.ElicitationRequest) (core.ElicitationResult, error) {
		t.Fatal("presented a form for a call it should not claim")
		return core.ElicitationResult{}, nil
	}
	for _, info := range []agent.ToolCallInfo{
		{Call: agent.ToolCall{Name: "write_file", Args: core.NewRawJSON([]byte(`{"path":"f.txt","content":"x"}`))}},
		{Call: agent.ToolCall{Name: "edit_file", Args: core.NewRawJSON([]byte(`{"path":"f.txt","edits":[{"old":"a","new":"b"}]}`))}},
		{Call: agent.ToolCall{Name: "edit_file", Args: core.NewRawJSON([]byte(`not json`))}},
	} {
		c, err := chooseHunks(context.Background(), info, ui)
		if err != nil || c.Claimed {
			t.Errorf("%s %s: claimed=%v err=%v", info.Call.Name, info.Call.Args.Raw(), c.Claimed, err)
		}
	}
}

// TestPerHunkApprovalEndToEnd runs a real edit_file through the real gate:
// the user unticks change 2, the file gets changes 1 and 3, and the model is
// told which one was left out.
func TestPerHunkApprovalEndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(threeLines), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := NewSource(Config{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	var seen core.ElicitationRequest
	ui := answering("accept", map[string]any{"change_2": false}, &seen)
	gate := agent.NewTieredApproval(
		agent.WithAsk(func(context.Context, agent.ToolCallInfo) (bool, error) {
			t.Fatal("the plain ask ran as well; the user would be asked twice")
			return false, nil
		}),
		agent.WithChooser(func(ctx context.Context, info agent.ToolCallInfo) (agent.Choice, error) {
			return chooseHunks(ctx, info, ui)
		}))
	dispatch := func(ctx context.Context, info agent.ToolCallInfo) (*core.ToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(info.Call.Args.Raw(), &args); err != nil {
			return nil, err
		}
		return src.Call(ctx, info.Call.Name, args)
	}
	res, err := gate.WrapToolCall(context.Background(), threeHunkCall(t), dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("edit failed: %+v", res.Content)
	}
	got, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ALPHA\nbeta\nGAMMA\n" {
		t.Errorf("file = %q, want changes 1 and 3 only", got)
	}
	last := res.Content[len(res.Content)-1].Text
	if !strings.Contains(last, "declined change 2") {
		t.Errorf("last result block = %q, want the note naming change 2", last)
	}
}
