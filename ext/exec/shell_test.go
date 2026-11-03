package exec

import (
	"context"
	"encoding/json"
	osexec "os/exec"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

var testRules = []PrefixRule{
	{Prefix: [][]string{{"git"}, {"status", "diff"}}, Decision: Allow},
	{Prefix: [][]string{{"go"}, {"test", "vet"}}, Decision: Allow},
	{Prefix: [][]string{{"rm"}, {"-rf"}}, Decision: Prompt, Justification: "recursive delete"},
	{Prefix: [][]string{{"git"}, {"push"}, {"--force"}}, Decision: Forbidden, Justification: "rewrites shared history"},
}

// recordingSandbox confines nothing and counts the commands it was asked to.
type recordingSandbox struct{ confined *int }

func (recordingSandbox) Name() string     { return "recording" }
func (recordingSandbox) Available() error { return nil }
func (r recordingSandbox) Confine(*osexec.Cmd, Policy) error {
	*r.confined++
	return nil
}

func shellSource(t *testing.T, sb Sandbox) *Source {
	t.Helper()
	if sb == nil {
		// A stand-in that counts as a sandbox: escalation only means
		// something when there is one to leave (#203).
		sb = recordingSandbox{new(int)}
	}
	return mustSource(t, Config{Roots: []string{workspace(t)}, Shell: &ShellConfig{Rules: testRules}, Sandbox: sb})
}

func shellCall(argv []string, extra map[string]any) agent.ToolCallInfo {
	args := map[string]any{"command": argv}
	for k, v := range extra {
		args[k] = v
	}
	raw, _ := json.Marshal(args)
	return agent.ToolCallInfo{Call: agent.ToolCall{Name: ShellTool, Args: core.NewRawJSON(raw)}}
}

func callAll(t *testing.T, src *Source, args map[string]any) (string, *core.ToolResult) {
	t.Helper()
	res, err := src.Call(context.Background(), ShellTool, args)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String(), res
}

func TestShellClassifierMapsRulesToDispositions(t *testing.T) {
	src := shellSource(t, nil)
	cases := []struct {
		argv   []string
		extra  map[string]any
		disp   agent.CallDisposition
		key    string
		reason string
	}{
		{words("git status -s"), nil, agent.DispositionReadOnly, "git status", ""},
		{words("rm -rf build"), nil, agent.DispositionDestructive, "rm -rf", "recursive delete"},
		{words("git push --force origin"), nil, agent.DispositionForbidden, "", "rewrites shared history"},
		{words("curl example.com"), nil, agent.DispositionUnknown, "curl example.com", ""},
		{[]string{"bash", "-lc", "go test ./... && go vet ./..."}, nil, agent.DispositionReadOnly, "", ""},
		{[]string{"bash", "-lc", "go test ./... && rm -rf build"}, nil, agent.DispositionDestructive, "", "recursive delete"},
		{[]string{"bash", "-lc", "git status; git push --force"}, nil, agent.DispositionForbidden, "", "rewrites shared history"},
		{[]string{"bash", "-lc", "git status; make"}, nil, agent.DispositionUnknown, "", ""},
		{[]string{"bash", "-lc", "git status > out"}, nil, agent.DispositionUnknown, "", "a script the rules can't read"},
		{words("go mod download"), map[string]any{"escalate": true, "justification": "needs the network"}, agent.DispositionAlwaysAsk, "", "outside the sandbox: needs the network"},
		// Escalation can't get past a forbidden rule.
		{words("git push --force"), map[string]any{"escalate": true, "justification": "please"}, agent.DispositionForbidden, "", "rewrites shared history"},
	}
	for _, c := range cases {
		got, ok := src.classifyShell(context.Background(), shellCall(c.argv, c.extra))
		if !ok {
			t.Errorf("%q: not claimed", c.argv)
			continue
		}
		if got.Disposition != c.disp || got.Key != c.key || got.Reason != c.reason {
			t.Errorf("%q: got %+v, want {%d %q %q}", c.argv, got, c.disp, c.key, c.reason)
		}
	}
	if _, ok := src.classifyShell(context.Background(), agent.ToolCallInfo{Call: agent.ToolCall{Name: "run_test"}}); ok {
		t.Error("the shell classifier claimed another tool's call")
	}
}

func TestShellRunsInsideTheSandboxUnlessEscalated(t *testing.T) {
	n := 0
	src := shellSource(t, recordingSandbox{&n})
	out, _ := callAll(t, src, map[string]any{"command": helperArgv(t, "echo", "hi")})
	if !strings.Contains(out, "exited 0") || !strings.Contains(out, "hi") || n != 1 {
		t.Fatalf("sandboxed run: confined=%d, output %q", n, out)
	}
	out, _ = callAll(t, src, map[string]any{"command": helperArgv(t, "echo", "hi"), "escalate": true, "justification": "test"})
	if !strings.Contains(out, "exited 0") || n != 1 {
		t.Errorf("escalated run was confined (confined=%d) or failed: %q", n, out)
	}
}

func TestShellRefusesBadArguments(t *testing.T) {
	src := shellSource(t, nil)
	for _, args := range []map[string]any{
		{},
		{"command": []string{}},
		{"command": helperArgv(t, "echo"), "escalate": true},
		{"command": helperArgv(t, "echo"), "workdir": "../.."},
	} {
		_, res := callAll(t, src, args)
		if !res.IsError {
			t.Errorf("%v: ran", args)
		}
	}
}

func TestShellFlagsALikelySandboxDenial(t *testing.T) {
	src := shellSource(t, nil)
	out, res := callAll(t, src, map[string]any{"command": helperArgv(t, "fail-with", "touch: /etc/x: Operation not permitted")})
	if !strings.Contains(out, "escalate: true") || res.StructuredContent.(map[string]any)["sandbox_denied"] != true {
		t.Errorf("no escalation hint on a denial: %q", out)
	}
	out, _ = callAll(t, src, map[string]any{"command": helperArgv(t, "fail")})
	if strings.Contains(out, "escalate: true") {
		t.Errorf("an ordinary failure was read as a sandbox denial: %q", out)
	}
}

func TestLooksLikeSandboxDenial(t *testing.T) {
	cases := []struct {
		out     string
		network bool
		want    bool
	}{
		{"mkdir: /Users/x/.config: Operation not permitted", false, true},
		{"open /usr/local/x: read-only file system", false, true},
		{"go: downloading x: dial tcp: lookup proxy.golang.org: no such host", false, true},
		{"go: downloading x: dial tcp: lookup proxy.golang.org: no such host", true, false},
		{"--- FAIL: TestSum (0.00s)", false, false},
		{"permission denied", false, false},
	}
	for _, c := range cases {
		if got := looksLikeSandboxDenial(c.out, c.network); got != c.want {
			t.Errorf("%q (network %v): got %v, want %v", c.out, c.network, got, c.want)
		}
	}
}

func TestShellApprovalWording(t *testing.T) {
	src := shellSource(t, nil)
	q, ok := src.renderShellApproval(shellCall(words("go mod download"), map[string]any{"escalate": true, "justification": "needs the network"}))
	if !ok || !strings.Contains(q, "OUTSIDE the sandbox") || !strings.Contains(q, "needs the network") {
		t.Errorf("escalation wording: %q", q)
	}
	info := shellCall(words("rm -rf build"), nil)
	info.Class = &agent.CallClass{Disposition: agent.DispositionDestructive, Reason: "recursive delete"}
	q, _ = src.renderShellApproval(info)
	if !strings.Contains(q, "Run in the recording sandbox") || !strings.Contains(q, "rm -rf build") || !strings.Contains(q, "(recursive delete)") {
		t.Errorf("sandboxed wording: %q", q)
	}
}

func TestShellNeedsNoAllowlistAndChecksItsRules(t *testing.T) {
	if _, err := NewSource(Config{Roots: []string{workspace(t)}, Shell: &ShellConfig{}, Sandbox: Unconfined()}); err != nil {
		t.Errorf("shell alone: %v", err)
	}
	if _, err := NewSource(Config{Roots: []string{workspace(t)}, Shell: &ShellConfig{Rules: []PrefixRule{{Decision: Allow}}}, Sandbox: Unconfined()}); err == nil {
		t.Error("a bad rule loaded")
	}
}

// TestShellWithoutASandboxIgnoresEscalation is #203: inside a container that is
// itself the sandbox, exec runs unconfined, and a model asking to escalate
// must not be sent to an ask nobody can answer.
func TestShellWithoutASandboxIgnoresEscalation(t *testing.T) {
	src := shellSource(t, Unconfined())
	call := shellCall(words("apt-get install -y nginx"), map[string]any{"escalate": true})
	if got, _ := src.classifyShell(context.Background(), call); got.Disposition == agent.DispositionAlwaysAsk {
		t.Errorf("escalate without a sandbox was classified %+v; it should be judged like any other call", got)
	}
	out, res := callAll(t, src, map[string]any{"command": helperArgv(t, "echo", "hi"), "escalate": true})
	if res.IsError || !strings.Contains(out, "hi") {
		t.Errorf("escalate without a justification was refused: %q", out)
	}
	out, _ = callAll(t, src, map[string]any{"command": helperArgv(t, "fail-with", "touch: /etc/x: Operation not permitted")})
	if strings.Contains(out, "escalate") {
		t.Errorf("suggested escalating with no sandbox to leave: %q", out)
	}
	def := src.shellDef()
	if strings.Contains(def.Description, "escalate") || def.InputSchema.(map[string]any)["properties"].(map[string]any)["escalate"] != nil {
		t.Errorf("the tool offers escalation without a sandbox: %q", def.Description)
	}
}
