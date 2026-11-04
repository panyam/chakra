//go:build unix

package exec

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// detachSource opens a session on root whose registry lives under state, so
// two calls with the same arguments are two sessions on one workspace.
func detachSource(t *testing.T, root, state string) *Source {
	t.Helper()
	src := mustSource(t, Config{
		Roots: []string{root},
		Shell: &ShellConfig{Rules: testRules, Processes: &ProcessConfig{
			AllowDetach: true, StateDir: state, DefaultYield: 300 * time.Millisecond, MaxYield: 3 * time.Second,
		}},
		Sandbox: recordingSandbox{new(int)},
	})
	t.Cleanup(func() { src.Close() })
	return src
}

func registryEntries(t *testing.T, src *Source) []string {
	t.Helper()
	names, _ := filepath.Glob(filepath.Join(src.procs.reg.dir, "*"))
	return names
}

func TestLaterSessionListsReadsAndKillsDetachedProcess(t *testing.T) {
	root, state := workspace(t), t.TempDir()
	first := detachSource(t, root, state)
	_, sc := callProc(t, first, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick"), "detach": true, "yield_ms": 300})
	pid, _ := sc["pid"].(int)
	if pid == 0 {
		t.Fatalf("detached start: %v", sc)
	}
	t.Cleanup(func() { syscall.Kill(-pid, syscall.SIGKILL) })
	first.Close()

	second := detachSource(t, root, state)
	out, sc := callProc(t, second, ListProcessesTool, map[string]any{})
	if !strings.Contains(out, "detached, from an earlier session") {
		t.Fatalf("list_processes doesn't show the earlier session's process: %q", out)
	}
	procs, _ := sc["processes"].([]map[string]any)
	if len(procs) != 1 || procs[0]["pid"] != pid {
		t.Fatalf("list_processes: %v, want pid %d", sc, pid)
	}
	id := procs[0]["session_id"]
	if out, _ := callProc(t, second, WriteStdinTool, map[string]any{"session_id": id}); !strings.Contains(out, "tick") {
		t.Errorf("write_stdin didn't read the log: %q", out)
	}
	if _, sc := callProc(t, second, WriteStdinTool, map[string]any{"session_id": id, "chars": "x\n"}); sc["error"] != true {
		t.Error("write_stdin typed into a process from an earlier session")
	}
	out, sc = callProc(t, second, KillProcessTool, map[string]any{"session_id": id})
	if sc["running"] != false {
		t.Fatalf("kill_process: %q %v", out, sc)
	}
	if _, ok := procStart(pid); ok {
		t.Error("the process is still running after kill_process")
	}
	if left := registryEntries(t, second); len(left) != 0 {
		t.Errorf("registry still holds %v after kill_process", left)
	}
}

func TestStartupDropsEntriesForGoneOrReusedPids(t *testing.T) {
	root, state := workspace(t), t.TempDir()
	reg, err := newRegistry(state, root)
	if err != nil {
		t.Fatal(err)
	}
	gone := osexec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	dead := gone.Process.Pid
	self, _ := procStart(os.Getpid())
	for _, e := range []regEntry{
		{ID: "proc-exited", PID: dead, StartToken: self, Cmd: "exited"},
		// The pid is running, but it started at another time than the entry
		// says, so the OS gave it to another process.
		{ID: "proc-reused", PID: os.Getpid(), StartToken: "0.0", Cmd: "reused"},
	} {
		e.OwnerPID, e.Started = dead, time.Now()
		if err := reg.write(e); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(reg.path(e.ID, ".log"), []byte("x"), 0o600)
	}
	src := detachSource(t, root, state)
	if out, _ := callProc(t, src, ListProcessesTool, map[string]any{}); out != "no processes" {
		t.Errorf("list_processes: %q, want no processes", out)
	}
	if left := registryEntries(t, src); len(left) != 0 {
		t.Errorf("registry still holds %v", left)
	}
}

func TestLaterSessionReportsExitCode(t *testing.T) {
	root, state := workspace(t), t.TempDir()
	first := detachSource(t, root, state)
	// One command exits between the sessions, and one while the second
	// session watches it.
	callProc(t, first, ExecCommandTool, map[string]any{"cmd": "echo early; exit 3", "detach": true, "yield_ms": 1})
	callProc(t, first, ExecCommandTool, map[string]any{"cmd": "sleep 1; echo late; exit 4", "detach": true, "yield_ms": 1})
	first.Close()
	time.Sleep(300 * time.Millisecond)

	second := detachSource(t, root, state)
	_, sc := callProc(t, second, ListProcessesTool, map[string]any{})
	procs, _ := sc["processes"].([]map[string]any)
	if len(procs) != 2 {
		t.Fatalf("list_processes: %v, want both processes", sc)
	}
	for _, want := range []struct{ out, exit string }{{"early", "[exited 3"}, {"late", "[exited 4"}} {
		var id any
		for _, p := range procs {
			if strings.Contains(p["cmd"].(string), want.out) {
				id = p["session_id"]
			}
		}
		out, _ := callProc(t, second, WriteStdinTool, map[string]any{"session_id": id, "yield_ms": 3000})
		if !strings.Contains(out, want.out) || !strings.Contains(out, want.exit) {
			t.Errorf("%s: %q, want its output and %q", want.out, out, want.exit)
		}
	}
	if left := registryEntries(t, second); len(left) != 0 {
		t.Errorf("registry still holds %v after both exits were read", left)
	}
}

func TestOpenSessionKeepsItsDetachedProcess(t *testing.T) {
	root, state := workspace(t), t.TempDir()
	first := detachSource(t, root, state)
	_, sc := callProc(t, first, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "tick"), "detach": true, "yield_ms": 1})
	pid, _ := sc["pid"].(int)
	t.Cleanup(func() { syscall.Kill(-pid, syscall.SIGKILL) })

	second := detachSource(t, root, state)
	if out, _ := callProc(t, second, ListProcessesTool, map[string]any{}); out != "no processes" {
		t.Errorf("a second session took a process from a session still open: %q", out)
	}
}

func TestDetachedExitCodeIsTheCommands(t *testing.T) {
	src := procSource(t, ProcessConfig{AllowDetach: true})
	if out, _ := callProc(t, src, ExecCommandTool, map[string]any{"cmd": "exit 5", "detach": true, "yield_ms": 2000}); !strings.Contains(out, "[exited 5") {
		t.Errorf("detached exit 5: %q", out)
	}
}
