package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// shellInfo is a call to one "shell" tool that annotates itself destructive,
// as a general command tool must, with the command as its argument.
func shellInfo(cmd string) ToolCallInfo {
	return ToolCallInfo{
		Call:        ToolCall{Name: "shell", Args: core.NewRawJSON([]byte(`{"cmd":"` + cmd + `"}`))},
		Destructive: true,
	}
}

func shellCmd(info ToolCallInfo) string {
	var a struct{ Cmd string }
	_ = info.Call.Args.Bind(&a)
	return a.Cmd
}

// classifyShell judges a command by its first two words, the way a prefix
// rule would.
func classifyShell(_ context.Context, info ToolCallInfo) (CallClass, bool) {
	if info.Call.Name != "shell" {
		return CallClass{}, false
	}
	cmd := shellCmd(info)
	f := strings.Fields(cmd)
	prefix := strings.Join(f[:min(2, len(f))], " ")
	switch {
	case cmd == "rm -rf /":
		return CallClass{Disposition: DispositionForbidden, Reason: "deletes the filesystem"}, true
	case prefix == "git status" || prefix == "ls":
		return CallClass{Disposition: DispositionReadOnly, Key: prefix}, true
	case f[0] == "touch":
		return CallClass{Disposition: DispositionReversible, Key: "touch"}, true
	case prefix == "rm -rf":
		return CallClass{Disposition: DispositionDestructive, Reason: "recursive delete"}, true
	case prefix == "git push":
		return CallClass{Disposition: DispositionDestructive, Key: prefix}, true
	case prefix == "make test":
		return CallClass{Key: prefix}, true
	}
	return CallClass{}, false
}

func TestClassifierDecidesPerCallUnderEachMode(t *testing.T) {
	const allow, ask, deny = "allow", "ask", "deny"
	cases := []struct {
		cmd  string
		want map[ApprovalMode]string
	}{
		{"git status", map[ApprovalMode]string{ModeAlwaysAsk: ask, ModeReadOnlyAuto: allow, ModeReversibleAuto: allow, ModeAlwaysAllow: allow}},
		{"touch new.txt", map[ApprovalMode]string{ModeAlwaysAsk: ask, ModeReadOnlyAuto: ask, ModeReversibleAuto: allow, ModeAlwaysAllow: allow}},
		{"rm -rf build", map[ApprovalMode]string{ModeAlwaysAsk: ask, ModeReadOnlyAuto: ask, ModeReversibleAuto: ask, ModeAlwaysAllow: allow}},
		{"rm -rf /", map[ApprovalMode]string{ModeAlwaysAsk: deny, ModeReadOnlyAuto: deny, ModeReversibleAuto: deny, ModeAlwaysAllow: deny}},
		// No opinion on the effect, so the tool's destructive annotation decides.
		{"make test", map[ApprovalMode]string{ModeAlwaysAsk: ask, ModeReadOnlyAuto: ask, ModeReversibleAuto: ask, ModeAlwaysAllow: allow}},
		// Unclaimed, so the gate acts as if there were no classifier.
		{"curl example.com", map[ApprovalMode]string{ModeAlwaysAsk: ask, ModeReadOnlyAuto: ask, ModeReversibleAuto: ask, ModeAlwaysAllow: allow}},
	}
	for _, c := range cases {
		for mode, want := range c.want {
			askFn, asked := countingAsk(false)
			p := NewTieredApproval(WithDefaultMode(mode), WithAsk(askFn), WithClassifier(classifyShell))
			allowed, reason, err := gate(p, shellInfo(c.cmd))
			if err != nil {
				t.Fatalf("%q mode %d: %v", c.cmd, mode, err)
			}
			got := deny
			switch {
			case allowed:
				got = allow
			case *asked > 0:
				got = ask
			}
			if got != want {
				t.Errorf("%q under mode %d: got %s, want %s (reason %q)", c.cmd, mode, got, want, reason)
			}
		}
	}
}

func TestForbiddenBeatsRuleAllowAndRememberedApproval(t *testing.T) {
	forbidAfterFirst := 0
	classify := func(ctx context.Context, info ToolCallInfo) (CallClass, bool) {
		forbidAfterFirst++
		if forbidAfterFirst > 1 {
			return CallClass{Disposition: DispositionForbidden, Key: "x", Reason: "nope"}, true
		}
		return CallClass{Disposition: DispositionDestructive, Key: "x"}, true
	}
	askFn, _ := countingAsk(true)
	p := NewTieredApproval(WithAsk(askFn), WithRememberApprovals(true), WithClassifier(classify))
	if allowed, _, _ := gate(p, shellInfo("x")); !allowed {
		t.Fatal("first call should be asked and approved")
	}
	allowed, reason, _ := gate(p, shellInfo("x"))
	if allowed || !strings.Contains(reason, "nope") {
		t.Errorf("a forbidden call with a remembered key ran (reason %q)", reason)
	}

	p = NewTieredApproval(WithDefaultMode(ModeAlwaysAllow), WithToolRule("shell", RuleAllow), WithClassifier(classifyShell))
	if allowed, _, _ := gate(p, shellInfo("rm -rf /")); allowed {
		t.Error("RuleAllow let a forbidden call run")
	}
}

func TestRememberKeysOnClassifierKey(t *testing.T) {
	askFn, asked := countingAsk(true)
	p := NewTieredApproval(WithAsk(askFn), WithRememberApprovals(true), WithClassifier(classifyShell))

	gate(p, shellInfo("git push origin main"))
	gate(p, shellInfo("git push origin other"))
	if *asked != 1 {
		t.Errorf("same prefix: asked %d times, want 1", *asked)
	}
	gate(p, shellInfo("touch new.txt"))
	if *asked != 2 {
		t.Errorf("approving git push covered touch: asked %d times, want 2", *asked)
	}
}

func TestClassifiedCallWithoutKeyIsNeverRemembered(t *testing.T) {
	askFn, asked := countingAsk(true)
	p := NewTieredApproval(WithAsk(askFn), WithRememberApprovals(true), WithClassifier(classifyShell),
		// A tool-name key would let one approval cover every call.
		WithRememberKey(func(info ToolCallInfo) (string, bool) { return info.Call.Name, true }))
	gate(p, shellInfo("rm -rf build"))
	gate(p, shellInfo("rm -rf build"))
	if *asked != 2 {
		t.Errorf("asked %d times, want 2: a keyless classified call was remembered", *asked)
	}
}

func TestClassifierSeesArgsRewrittenByEarlierMiddleware(t *testing.T) {
	rewrite := ToolMiddleware(func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
		info.Call.Args = core.NewRawJSON([]byte(`{"cmd":"rm -rf /"}`))
		return next(ctx, info)
	})
	p := NewTieredApproval(WithDefaultMode(ModeAlwaysAllow), WithClassifier(classifyShell))
	ran := false
	call := chainToolMiddleware([]ToolMiddleware{rewrite, p.WrapToolCall}, func(context.Context, ToolCallInfo) (*core.ToolResult, error) {
		ran = true
		return &core.ToolResult{}, nil
	})
	_, err := call(context.Background(), shellInfo("ls"))
	if ran {
		t.Fatal("the rewritten call ran; the classifier judged the arguments before the rewrite")
	}
	if _, ok := deniedReason(err); !ok {
		t.Fatalf("want a denial, got %v", err)
	}
}

func TestAskSeesClassification(t *testing.T) {
	var seen *CallClass
	p := NewTieredApproval(WithClassifier(classifyShell), WithAsk(func(ctx context.Context, info ToolCallInfo) (bool, error) {
		seen = info.Class
		return false, nil
	}))
	gate(p, shellInfo("rm -rf build"))
	if seen == nil || seen.Reason != "recursive delete" {
		t.Errorf("ask saw class %+v, want the classifier's reason", seen)
	}
}

func TestAlwaysAskAsksUnderEveryModeAndIsNeverRemembered(t *testing.T) {
	escalate := func(context.Context, ToolCallInfo) (CallClass, bool) {
		return CallClass{Disposition: DispositionAlwaysAsk, Key: "go mod", Reason: "outside the sandbox"}, true
	}
	for _, mode := range []ApprovalMode{ModeAlwaysAsk, ModeReadOnlyAuto, ModeReversibleAuto, ModeAlwaysAllow} {
		askFn, asked := countingAsk(true)
		p := NewTieredApproval(WithDefaultMode(mode), WithAsk(askFn), WithRememberApprovals(true),
			WithToolRule("shell", RuleAllow), WithClassifier(escalate))
		gate(p, shellInfo("go mod download"))
		gate(p, shellInfo("go mod download"))
		if *asked != 2 {
			t.Errorf("mode %d: asked %d times, want 2 (every call, none remembered, RuleAllow ignored)", mode, *asked)
		}
	}

	p := NewTieredApproval(WithDefaultMode(ModeAlwaysAllow), WithClassifier(escalate))
	if allowed, _, _ := gate(p, shellInfo("go mod download")); allowed {
		t.Error("ran with no AskFunc; an always-ask call must be refused when nobody can answer")
	}
	askFn, asked := countingAsk(true)
	p = NewTieredApproval(WithAsk(askFn), WithToolRule("shell", RuleDeny), WithClassifier(escalate))
	if allowed, _, _ := gate(p, shellInfo("go mod download")); allowed || *asked != 0 {
		t.Errorf("RuleDeny: allowed=%v asked=%d, want refused without asking", allowed, *asked)
	}
}
