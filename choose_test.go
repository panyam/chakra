package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// runChoice sends info through a gate built with opts and reports the call
// that reached next (nil if none did), the result, and the denial reason.
func runChoice(t *testing.T, info ToolCallInfo, opts ...TieredOption) (*ToolCallInfo, *core.ToolResult, string) {
	t.Helper()
	var ran *ToolCallInfo
	next := func(_ context.Context, got ToolCallInfo) (*core.ToolResult, error) {
		ran = &got
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "ok"}}}, nil
	}
	res, err := NewTieredApproval(opts...).WrapToolCall(context.Background(), info, next)
	if err != nil {
		reason, ok := deniedReason(err)
		if !ok {
			t.Fatalf("unexpected error: %v", err)
		}
		return ran, res, reason
	}
	return ran, res, ""
}

func editCall(args string) ToolCallInfo {
	return ToolCallInfo{Call: ToolCall{ID: "c1", Name: "edit_file", Args: core.NewRawJSON(json.RawMessage(args))}}
}

func choosing(c Choice) ChooseFunc {
	return func(context.Context, ToolCallInfo) (Choice, error) { return c, nil }
}

func TestChooserNarrowsTheCallThatRuns(t *testing.T) {
	ask, asked := countingAsk(true)
	ran, res, reason := runChoice(t, editCall(`{"edits":[1,2,3]}`),
		WithAsk(ask),
		WithChooser(choosing(Choice{Claimed: true, Approved: true, Args: core.NewRawJSON(json.RawMessage(`{"edits":[1,3]}`)), Note: "hunk 2 declined by the user"})))
	if reason != "" || ran == nil {
		t.Fatalf("call did not run: %q", reason)
	}
	if got := string(ran.Call.Args.Raw()); got != `{"edits":[1,3]}` {
		t.Errorf("ran with args %s, want the narrowed ones", got)
	}
	if ran.Call.Name != "edit_file" || ran.Call.ID != "c1" {
		t.Errorf("call identity changed: %+v", ran.Call)
	}
	if *asked != 0 {
		t.Errorf("AskFunc consulted %d times for a claimed call; the user would be asked twice", *asked)
	}
	if n := len(res.Content); n != 2 || res.Content[1].Text != "hunk 2 declined by the user" {
		t.Errorf("result content = %+v, want the tool's output then the note", res.Content)
	}
}

func TestChooserDecliningAllDeniesWithTheNote(t *testing.T) {
	ran, _, reason := runChoice(t, editCall(`{}`),
		WithChooser(choosing(Choice{Claimed: true, Note: "all 3 hunks declined by the user"})))
	if ran != nil {
		t.Fatal("a fully declined call ran")
	}
	if reason != "declined by user: all 3 hunks declined by the user" {
		t.Errorf("reason = %q", reason)
	}
}

func TestUnclaimedCallFallsBackToAsk(t *testing.T) {
	ask, asked := countingAsk(false)
	ran, _, reason := runChoice(t, editCall(`{}`), WithAsk(ask), WithChooser(choosing(Choice{})))
	if ran != nil || reason != "declined by user" || *asked != 1 {
		t.Errorf("ran=%v reason=%q asked=%d, want the AskFunc's refusal", ran != nil, reason, *asked)
	}
}

func TestPartialApprovalIsNotRemembered(t *testing.T) {
	calls := 0
	choose := func(context.Context, ToolCallInfo) (Choice, error) {
		calls++
		return Choice{Claimed: true, Approved: true, Args: core.NewRawJSON(json.RawMessage(`{"edits":[1]}`))}, nil
	}
	p := NewTieredApproval(WithChooser(choose), WithRememberApprovals(true))
	for range 2 {
		if allowed, reason, err := gate(p, editCall(`{"edits":[1,2]}`)); !allowed || err != nil {
			t.Fatalf("allowed=%v reason=%q err=%v", allowed, reason, err)
		}
	}
	if calls != 2 {
		t.Errorf("chooser consulted %d times, want 2: a partial yes must not auto-allow the next call", calls)
	}
}

func TestWholeApprovalThroughChooserIsRemembered(t *testing.T) {
	calls := 0
	choose := func(context.Context, ToolCallInfo) (Choice, error) {
		calls++
		return Choice{Claimed: true, Approved: true}, nil
	}
	p := NewTieredApproval(WithChooser(choose), WithRememberApprovals(true))
	for range 2 {
		if allowed, _, _ := gate(p, editCall(`{}`)); !allowed {
			t.Fatal("not allowed")
		}
	}
	if calls != 1 {
		t.Errorf("chooser consulted %d times, want 1", calls)
	}
}

// The chooser sits inside the gate, so it sees arguments after every earlier
// middleware has rewritten them, and narrows those rather than the model's.
func TestChooserSeesRewrittenArgs(t *testing.T) {
	var saw string
	choose := func(_ context.Context, info ToolCallInfo) (Choice, error) {
		saw = string(info.Call.Args.Raw())
		return Choice{Claimed: true, Approved: true}, nil
	}
	rewrite := func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
		info.Call.Args = core.NewRawJSON(json.RawMessage(`{"rewritten":true}`))
		return next(ctx, info)
	}
	gateMW := NewTieredApproval(WithChooser(choose)).WrapToolCall
	final := func(context.Context, ToolCallInfo) (*core.ToolResult, error) { return &core.ToolResult{}, nil }
	if _, err := rewrite(context.Background(), editCall(`{}`), func(ctx context.Context, info ToolCallInfo) (*core.ToolResult, error) {
		return gateMW(ctx, info, final)
	}); err != nil {
		t.Fatal(err)
	}
	if saw != `{"rewritten":true}` {
		t.Errorf("chooser saw %s", saw)
	}
}
