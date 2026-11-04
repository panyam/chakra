package exec

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

func procSource(t *testing.T, pc ProcessConfig) *Source {
	t.Helper()
	if pc.DefaultYield == 0 {
		pc.DefaultYield = 300 * time.Millisecond
	}
	if pc.MaxYield == 0 {
		pc.MaxYield = 3 * time.Second
	}
	if pc.AllowDetach && pc.StateDir == "" {
		pc.StateDir = t.TempDir()
	}
	src := mustSource(t, Config{
		Roots:   []string{workspace(t)},
		Shell:   &ShellConfig{Rules: testRules, Processes: &pc},
		Sandbox: recordingSandbox{new(int)},
	})
	t.Cleanup(func() { src.Close() })
	return src
}

// helperCmd is a helper mode as one shell command line, for exec_command.
func helperCmd(t *testing.T, mode ...string) string {
	t.Helper()
	argv := helperArgv(t, mode...)
	for i, a := range argv {
		argv[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(argv, " ")
}

func callProc(t *testing.T, src *Source, tool string, args map[string]any) (string, map[string]any) {
	t.Helper()
	res, err := src.Call(context.Background(), tool, args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		text.WriteString(c.Text)
	}
	sc, _ := res.StructuredContent.(map[string]any)
	if res.IsError {
		sc = map[string]any{"error": true}
	}
	return text.String(), sc
}

func TestExecCommandReturnsWhenTheCommandFinishes(t *testing.T) {
	src := procSource(t, ProcessConfig{})
	out, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "echo", "hi"), "yield_ms": 3000})
	if sc["running"] != false || !strings.Contains(out, "hi") || !strings.Contains(out, "[exited 0") {
		t.Fatalf("result %q %v", out, sc)
	}
	if out, _ := callProc(t, src, ListProcessesTool, nil); out != "no processes" {
		t.Errorf("a finished process is still listed: %q", out)
	}
}

func TestInteractiveProcessTakesInput(t *testing.T) {
	src := procSource(t, ProcessConfig{})
	out, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "prompt")})
	if sc["running"] != true || !strings.Contains(out, "name?") {
		t.Fatalf("start: %q %v", out, sc)
	}
	id := sc["session_id"].(string)
	out, sc = callProc(t, src, WriteStdinTool, map[string]any{"session_id": id, "chars": "Ada\n", "yield_ms": 3000})
	if !strings.Contains(out, "hello Ada") || sc["running"] != false || strings.Contains(out, "name?") {
		t.Errorf("after input: %q %v", out, sc)
	}
}

func TestPollReturnsOnlyNewOutputAndKillStops(t *testing.T) {
	src := procSource(t, ProcessConfig{})
	out, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick")})
	id, _ := sc["session_id"].(string)
	if sc["running"] != true || !strings.Contains(out, "tick 0") {
		t.Fatalf("start: %q %v", out, sc)
	}
	out, _ = callProc(t, src, WriteStdinTool, map[string]any{"session_id": id})
	if strings.Contains(out, "tick 0\n") || !strings.Contains(out, "tick") {
		t.Errorf("poll repeated old output or showed none: %q", out)
	}
	if out, _ := callProc(t, src, ListProcessesTool, nil); !strings.Contains(out, id) || !strings.Contains(out, "running") {
		t.Errorf("list: %q", out)
	}
	out, sc = callProc(t, src, KillProcessTool, map[string]any{"session_id": id})
	if sc["running"] != false {
		t.Errorf("kill: %q %v", out, sc)
	}
	if out, _ := callProc(t, src, ListProcessesTool, nil); out != "no processes" {
		t.Errorf("killed process still listed: %q", out)
	}
}

func TestCloseKillsEveryProcess(t *testing.T) {
	src := procSource(t, ProcessConfig{})
	_, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick")})
	p, err := src.procs.get(sc["session_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	pid := p.cmd.Process.Pid
	src.Close()
	if err := syscall.Kill(pid, 0); err == nil {
		t.Error("the process outlived the session")
	}
}

func TestProcessCapAndOutputCap(t *testing.T) {
	src := procSource(t, ProcessConfig{Max: 1, MaxOutput: 64})
	_, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick")})
	id := sc["session_id"].(string)
	if _, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick")}); sc["error"] != true {
		t.Error("a second process started past Max 1")
	}
	time.Sleep(500 * time.Millisecond)
	out, sc := callProc(t, src, WriteStdinTool, map[string]any{"session_id": id, "yield_ms": 1})
	if d, _ := sc["dropped"].(int64); d == 0 && !strings.Contains(out, "dropped") {
		t.Errorf("output past MaxOutput wasn't reported as dropped: %q %v", out, sc)
	}
}

func TestTTYGivesTheProcessATerminal(t *testing.T) {
	src := procSource(t, ProcessConfig{})
	out, _ := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "isatty"), "tty": true, "yield_ms": 3000})
	if !strings.Contains(out, "tty=true") {
		t.Errorf("with tty: %q", out)
	}
	out, _ = callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "isatty"), "yield_ms": 3000})
	if !strings.Contains(out, "tty=false") {
		t.Errorf("without tty: %q", out)
	}
}

func procCall(name string, args map[string]any) agent.ToolCallInfo {
	raw, _ := json.Marshal(args)
	return agent.ToolCallInfo{Call: agent.ToolCall{Name: name, Args: core.NewRawJSON(raw)}}
}

func TestProcessToolsAreJudgedLikeShell(t *testing.T) {
	src := procSource(t, ProcessConfig{})
	cases := []struct {
		info agent.ToolCallInfo
		disp agent.CallDisposition
	}{
		{procCall(ExecCommandTool, map[string]any{"cmd": "go test ./..."}), agent.DispositionReadOnly},
		{procCall(ExecCommandTool, map[string]any{"cmd": "rm -rf build"}), agent.DispositionDestructive},
		{procCall(ExecCommandTool, map[string]any{"cmd": "git push --force"}), agent.DispositionForbidden},
		{procCall(WriteStdinTool, map[string]any{"session_id": "p1"}), agent.DispositionReadOnly},
		{procCall(WriteStdinTool, map[string]any{"session_id": "p1", "chars": "rm -rf /\n"}), agent.DispositionDestructive},
		{procCall(KillProcessTool, map[string]any{"session_id": "p1"}), agent.DispositionReversible},
	}
	for _, c := range cases {
		got, ok := src.classifyProcs(context.Background(), c.info)
		if !ok || got.Disposition != c.disp {
			t.Errorf("%s %s: got %+v (claimed %v), want disposition %d", c.info.Call.Name, c.info.Call.Args.Raw(), got, ok, c.disp)
		}
	}
	if _, ok := src.classifyProcs(context.Background(), procCall(ShellTool, map[string]any{})); ok {
		t.Error("claimed a shell call")
	}
}

func TestProcessToolsNeedTheirConfig(t *testing.T) {
	src := mustSource(t, Config{Roots: []string{workspace(t)}, Shell: &ShellConfig{Rules: testRules}, Sandbox: Unconfined()})
	defs, _ := src.Tools(context.Background())
	for _, d := range defs {
		if d.Name == ExecCommandTool || d.Name == WriteStdinTool {
			t.Errorf("%s offered without Shell.Processes", d.Name)
		}
	}
}

func TestParallelPollsShareOutputWithoutRepeating(t *testing.T) {
	src := procSource(t, ProcessConfig{})
	_, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick"), "yield_ms": 100})
	id := sc["session_id"].(string)
	outs := make(chan string, 4)
	for i := 0; i < 4; i++ {
		go func() {
			out, _ := callProc(t, src, WriteStdinTool, map[string]any{"session_id": id, "yield_ms": 200})
			outs <- out
		}()
	}
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		for _, line := range strings.Split(<-outs, "\n") {
			if strings.HasPrefix(line, "tick ") {
				seen[line]++
			}
		}
	}
	for line, n := range seen {
		if n > 1 {
			t.Errorf("%q returned %d times", line, n)
		}
	}
}

func TestExecCommandDetachSurvivesClose(t *testing.T) {
	src := procSource(t, ProcessConfig{AllowDetach: true})
	root := src.roots[0]
	out, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick"), "detach": true, "yield_ms": 300})
	pid, _ := sc["pid"].(int)
	if pid == 0 || sc["detached"] != true {
		t.Fatalf("detached start: %q %v", out, sc)
	}
	t.Cleanup(func() { syscall.Kill(-pid, syscall.SIGKILL) })
	logPath, _ := sc["log"].(string)
	if logPath == "" || strings.HasPrefix(logPath, root) {
		t.Errorf("log %q should be outside the workspace %q, so it doesn't show up in the task's files", logPath, root)
	}
	if _, sc := callProc(t, src, WriteStdinTool, map[string]any{"session_id": sc["session_id"], "chars": "x\n"}); sc["error"] != true {
		t.Error("write_stdin typed into a detached process")
	}
	src.Close()
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the detached process died with the session: %v", err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(b), "tick") {
		t.Errorf("log %s: %q %v", logPath, b, err)
	}
}

func TestDetachNeedsAllowDetach(t *testing.T) {
	src := procSource(t, ProcessConfig{})
	defs, _ := src.Tools(context.Background())
	for _, d := range defs {
		if d.Name != ExecCommandTool {
			continue
		}
		if _, ok := d.InputSchema.(map[string]any)["properties"].(map[string]any)["detach"]; ok {
			t.Error("exec_command offered detach without AllowDetach")
		}
	}
	if _, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick"), "detach": true}); sc["error"] != true {
		t.Error("a detached start went through without AllowDetach")
	}
}

func TestDetachedCallAlwaysAsks(t *testing.T) {
	src := procSource(t, ProcessConfig{AllowDetach: true})
	got, ok := src.classifyProcs(context.Background(), procCall(ExecCommandTool, map[string]any{"cmd": "go test ./...", "detach": true}))
	if !ok || got.Disposition != agent.DispositionDestructive {
		t.Errorf("detached read-only command: got %+v, want destructive", got)
	}
	got, _ = src.classifyProcs(context.Background(), procCall(ExecCommandTool, map[string]any{"cmd": "git push --force", "detach": true}))
	if got.Disposition != agent.DispositionForbidden {
		t.Errorf("detach turned a forbidden command into an ask: %+v", got)
	}
	q, _ := src.renderProcApproval(procCall(ExecCommandTool, map[string]any{"cmd": "python3 server.py", "detach": true}))
	if !strings.Contains(q, "after this session ends") {
		t.Errorf("ask doesn't say the process outlives the session: %q", q)
	}
}

func TestDetachedProcessPollsMoreThanOnce(t *testing.T) {
	src := procSource(t, ProcessConfig{AllowDetach: true})
	_, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick"), "detach": true, "yield_ms": 300})
	pid, _ := sc["pid"].(int)
	t.Cleanup(func() { syscall.Kill(-pid, syscall.SIGKILL) })
	for range 2 {
		if out, _ := callProc(t, src, WriteStdinTool, map[string]any{"session_id": sc["session_id"], "yield_ms": 300}); !strings.Contains(out, "tick") {
			t.Errorf("poll: %q", out)
		}
	}
}
