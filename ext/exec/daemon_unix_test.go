//go:build unix

package exec

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// daemonSleep is how long the helper's daemon lives. Each test must return
// well before it, or Wait waited on the daemon.
const daemonSleep = "30s"

// startDaemon starts a helper that sleeps in a new session and writes to the
// caller's stdout, then returns its pid.
func startDaemon(d string) int {
	child := osexec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", helperSentinel, "sleep", d)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		os.Exit(8)
	}
	return child.Process.Pid
}

// killDaemon ends the daemon a test's command left behind.
func killDaemon(t *testing.T, out string) {
	t.Helper()
	t.Cleanup(func() { _ = syscall.Kill(grandchildPID(t, out), syscall.SIGKILL) })
}

func TestACommandThatLeavesADaemonReturns(t *testing.T) {
	spec := CommandSpec{
		Name:        "daemonize",
		Argv:        helperArgv(t, "daemon", daemonSleep),
		Description: "Start a daemon and exit.",
		Timeout:     15 * time.Second,
	}
	src := mustSource(t, baseConfig(t, spec))

	start := time.Now()
	res, err := src.Call(context.Background(), "run_daemonize", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	out := res.Content[0].Text
	killDaemon(t, out)

	if elapsed > 10*time.Second {
		t.Fatalf("the call took %s; it waited on the daemon", elapsed)
	}
	sc := res.StructuredContent.(map[string]any)
	if res.IsError || sc["timed_out"] != false || sc["exit_code"] != 0 {
		t.Errorf("want a clean exit 0, got %v", sc)
	}
	if sc["background_held_output"] != true || !strings.Contains(out, "background process") {
		t.Errorf("the result must say a background process held the output:\n%s", out)
	}
}

// TestShellThatLeavesADaemonReturns is the Terminal-Bench shape: a shell
// script whose last steps start nginx and websockify.
func TestShellThatLeavesADaemonReturns(t *testing.T) {
	src := shellSource(t, nil)

	start := time.Now()
	out, res := callAll(t, src, map[string]any{"command": helperArgv(t, "daemon", daemonSleep), "timeout_ms": 15000})
	elapsed := time.Since(start)
	killDaemon(t, out)

	if elapsed > 10*time.Second {
		t.Fatalf("the call took %s; it waited on the daemon", elapsed)
	}
	if res.IsError || !strings.Contains(out, "background process") {
		t.Errorf("want a clean exit that names the background process:\n%s", out)
	}
}

func TestAProcessThatLeavesADaemonExits(t *testing.T) {
	src := procSource(t, ProcessConfig{})
	out, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": helperCmd(t, "daemon", daemonSleep), "yield_ms": 300})
	killDaemon(t, out)
	if sc["running"] == false {
		return
	}
	id := sc["session_id"].(string)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, sc = callProc(t, src, WriteStdinTool, map[string]any{"session_id": id, "yield_ms": 500})
		if sc["running"] == false {
			return
		}
	}
	t.Fatalf("the process still reads as running with only its daemon left: %v", sc)
}
