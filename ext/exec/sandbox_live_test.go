//go:build exec_live && darwin

package exec

import (
	"context"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These run the real sandbox-exec, and are behind a tag because CI runs on
// Linux where this backend does not exist. Nothing in CI exercises a real
// sandbox. So only this file checks that the profile does what the golden test
// says it does.
//
//	go test -tags exec_live ./...

func liveSource(t *testing.T, root string, spec CommandSpec) *Source {
	t.Helper()
	src, err := NewSource(Config{Roots: []string{root}, Commands: []CommandSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	if src.sandbox.Name() != "sandbox-exec" {
		t.Fatalf("expected the real backend, got %s", src.sandbox.Name())
	}
	return src
}

func TestLiveWriteInsideARootSucceeds(t *testing.T) {
	root := workspace(t)
	target := filepath.Join(root, "out.txt")
	src := liveSource(t, root, CommandSpec{
		Name: "touch", Argv: helperArgv(t, "touch", target), Description: "Write a file.",
	})
	if got := call(t, src, "run_touch", nil); !strings.Contains(got, "exited 0") {
		t.Fatalf("a write inside the root must be permitted: %q", got)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
}

// TestLiveWriteOutsideEveryRootIsDenied pins the confinement with the cache
// allowances turned off.
//
// WritePaths is explicitly empty here on purpose. A toolchain cannot build
// without a writable temp directory, so the default list includes it. Under
// the shipped defaults, "outside every root" therefore doesn't mean "denied".
// This test asserts the roots-only property. The one below asserts how far the
// defaults widen it.
func TestLiveWriteOutsideEveryRootIsDenied(t *testing.T) {
	root := workspace(t)
	outside := filepath.Join(workspace(t), "escaped.txt")
	src, err := NewSource(Config{
		Roots:      []string{root},
		WritePaths: []string{},
		Commands:   []CommandSpec{{Name: "escape", Argv: helperArgv(t, "touch", outside), Description: "Write outside."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, src, "run_escape", nil)
	if strings.Contains(got, "exited 0") {
		t.Fatalf("the sandbox let a command write outside every root: %q", got)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the file exists; the profile did not confine writes")
	}
}

// TestLiveDefaultWritePathsWidenConfinementToTheCaches records the cost of the
// defaults rather than leaving it to be discovered. A command can write
// anywhere in the temp directory and the build caches. A real toolchain needs
// that to work, and an operator who keeps the defaults accepts that exposure.
func TestLiveDefaultWritePathsWidenConfinementToTheCaches(t *testing.T) {
	root := workspace(t)
	inTemp := filepath.Join(os.TempDir(), "mcpkit-exec-live-probe.txt")
	t.Cleanup(func() { os.Remove(inTemp) })

	src := liveSource(t, root, CommandSpec{
		Name: "temp", Argv: helperArgv(t, "touch", inTemp), Description: "Write to temp.",
	})
	if got := call(t, src, "run_temp", nil); !strings.Contains(got, "exited 0") {
		t.Fatalf("the default write paths cover the temp directory; a build cannot run without it: %q", got)
	}
}

func TestLiveDeniedReadIsDenied(t *testing.T) {
	root := workspace(t)
	home := t.TempDir()
	secret := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	src, err := NewSource(Config{
		Roots:    []string{root},
		DenyRead: []string{filepath.Join(home, ".ssh")},
		Commands: []CommandSpec{{Name: "steal", Argv: helperArgv(t, "read", secret), Description: "Read a key."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, src, "run_steal", nil)
	if strings.Contains(got, "PRIVATE KEY") {
		t.Fatalf("the profile allowed a read it denies: %q", got)
	}
}

func TestLiveLoopbackStaysOpen(t *testing.T) {
	root := workspace(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	src := liveSource(t, root, CommandSpec{
		Name: "dial", Argv: helperArgv(t, "dial", ln.Addr().String()), Description: "Dial loopback.",
	})
	if got := call(t, src, "run_dial", nil); !strings.Contains(got, "connected") {
		t.Fatalf("a test suite that starts a server on 127.0.0.1 is the ordinary case and must work: %q", got)
	}
}

// TestLiveCommandCanServeOnLoopback covers the httptest case, where the server
// runs inside the command. Seatbelt checks listen() as network-inbound.
// Imagine a profile that allows bind and outbound but not inbound. It passes
// the test above and still fails every test suite that starts its own server
// (#74).
func TestLiveCommandCanServeOnLoopback(t *testing.T) {
	src := liveSource(t, workspace(t), CommandSpec{
		Name: "serve", Argv: helperArgv(t, "listen", "127.0.0.1:0"), Description: "Serve on loopback.",
	})
	if got := call(t, src, "run_serve", nil); !strings.Contains(got, "served") {
		t.Fatalf("a command must be able to serve on 127.0.0.1: %q", got)
	}
}

// TestLiveEgressIsDenied asserts a denial, and on an offline machine that
// looks the same as a pass. A denied dial and an unreachable network both
// fail. The AllowNetwork half separates them. It is skipped rather than failed
// when the machine has no route out.
func TestLiveEgressIsDenied(t *testing.T) {
	root := workspace(t)
	const target = "1.1.1.1:80"

	closed := liveSource(t, root, CommandSpec{
		Name: "egress", Argv: helperArgv(t, "dial", target), Description: "Dial out.",
	})
	if got := call(t, closed, "run_egress", nil); strings.Contains(got, "connected") {
		t.Fatalf("the profile allowed egress with AllowNetwork off: %q", got)
	}

	open := liveSource(t, root, CommandSpec{
		Name: "egress", Argv: helperArgv(t, "dial", target), Description: "Dial out.", AllowNetwork: true,
	})
	if got := call(t, open, "run_egress", nil); !strings.Contains(got, "connected") {
		t.Skipf("no route to %s, so the denial above proves nothing on this machine: %q", target, got)
	}
}

// TestLiveARealToolchainRunsUnderTheProfile catches a write path missing from
// DefaultWritePaths. A sandboxed `go build` fails on its build cache long
// before it compiles anything.
func TestLiveARealToolchainRunsUnderTheProfile(t *testing.T) {
	root := workspace(t)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module sandboxprobe\n\ngo 1.21\n")
	write("main.go", "package main\n\nfunc main() {}\n")

	src := liveSource(t, root, CommandSpec{
		Name:        "build",
		Argv:        []string{"go", "build", "./..."},
		Description: "Build the module.",
		Timeout:     2 * time.Minute,
	})
	got := call(t, src, "run_build", nil)
	if !strings.Contains(got, "exited 0") {
		t.Fatalf("a real toolchain could not run under the profile:\n%s", got)
	}
}

var _ = context.Background

// TestLiveShellDenialIsFlaggedAndEscalationEscapesIt runs #154 end to end. The
// real profile blocks a shell write outside the workspace. The result says so
// and points at escalation, and the escalated retry writes the file.
func TestLiveShellDenialIsFlaggedAndEscalationEscapesIt(t *testing.T) {
	root := workspace(t)
	outside := filepath.Join(workspace(t), "escaped.txt")
	src, err := NewSource(Config{Roots: []string{root}, WritePaths: []string{}, Shell: &ShellConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{"sh", "-c", "touch " + outside}
	res, err := src.Call(context.Background(), ShellTool, map[string]any{"command": argv})
	if err != nil {
		t.Fatal(err)
	}
	if sc, _ := res.StructuredContent.(map[string]any); sc["sandbox_denied"] != true {
		t.Fatalf("a blocked write was not flagged as a sandbox denial: %+v", res.Content)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the sandboxed shell wrote outside the workspace")
	}

	res, err = src.Call(context.Background(), ShellTool, map[string]any{"command": argv, "escalate": true, "justification": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the escalated run did not write the file: %+v", res.Content)
	}
}

// TestLiveProcessesRunUnderTheSandbox runs #155 under the real backend. A
// long-running command and a tty command both start under sandbox-exec. A
// running process still can't write outside the roots.
func TestLiveProcessesRunUnderTheSandbox(t *testing.T) {
	root := t.TempDir()
	// No default write paths, as in the test above. The outside file lives in
	// the temp dir, and the sandbox doesn't let a command write there.
	src, err := NewSource(Config{Roots: []string{root}, WritePaths: []string{}, Shell: &ShellConfig{Processes: &ProcessConfig{DefaultYield: 2 * time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if src.sandbox.Name() == "unconfined" {
		t.Fatal("expected the platform sandbox")
	}
	outside := filepath.Join(workspace(t), "escaped")
	out, _ := callProc(t, src, ExecCommandTool, map[string]any{"cmd": "touch inside && touch " + outside + "; echo done"})
	if !strings.Contains(out, "done") {
		t.Fatalf("exec_command: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "inside")); err != nil {
		t.Errorf("write inside the root failed: %v", err)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("a process wrote outside every root")
	}
	out, _ = callProc(t, src, ExecCommandTool, map[string]any{"cmd": "test -t 0 && echo is-a-tty", "tty": true})
	if !strings.Contains(out, "is-a-tty") {
		t.Errorf("tty under the sandbox: %q", out)
	}
}

// TestLiveSandboxedCurlGoesThroughTheAllowlist runs #158 under the real
// backend. A sandboxed curl can only reach loopback. Through the proxy it
// reaches an allowed host, and the proxy refuses another host.
func TestLiveSandboxedCurlGoesThroughTheAllowlist(t *testing.T) {
	curl, err := osexec.LookPath("curl")
	if err != nil {
		t.Skip("no curl")
	}
	ok := okServer(t, "reached the allowed host")
	fakeHosts(t, map[string]string{"allowed.test": ok.Listener.Addr().String(), "blocked.test": ok.Listener.Addr().String()})
	root := t.TempDir()
	src, err := NewSource(Config{
		Roots: []string{root},
		Commands: []CommandSpec{
			{Name: "get_allowed", Argv: []string{curl, "-s", "http://allowed.test/"}, Description: "Fetch.", ReadOnly: true},
			{Name: "get_blocked", Argv: []string{curl, "-s", "http://blocked.test/"}, Description: "Fetch.", ReadOnly: true},
		},
		Network: &NetworkConfig{Allow: []string{"allowed.test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if src.sandbox.Name() == "unconfined" {
		t.Fatal("expected the platform sandbox")
	}
	if got := call(t, src, "run_get_allowed", nil); !strings.Contains(got, "reached the allowed host") {
		t.Errorf("allowed host: %q", got)
	}
	if got := call(t, src, "run_get_blocked", nil); !strings.Contains(got, "allowlist") || strings.Contains(got, "reached the allowed host") {
		t.Errorf("blocked host: %q", got)
	}
}

// TestLiveDetachedProcessStaysConfined checks that a detached command (#228)
// still runs under the profile and outlives Close.
func TestLiveDetachedProcessStaysConfined(t *testing.T) {
	root := t.TempDir()
	src, err := NewSource(Config{Roots: []string{root}, WritePaths: []string{}, Shell: &ShellConfig{Processes: &ProcessConfig{DefaultYield: 2 * time.Second, AllowDetach: true}}})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(workspace(t), "escaped")
	_, sc := callProc(t, src, ExecCommandTool, map[string]any{"cmd": "touch inside; touch " + outside + "; sleep 30", "detach": true, "yield_ms": 1000})
	pid, _ := sc["pid"].(int)
	if pid == 0 {
		t.Fatalf("detached start: %v", sc)
	}
	t.Cleanup(func() { syscall.Kill(-pid, syscall.SIGKILL) })
	src.Close()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the detached process died with the session: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "inside")); err != nil {
		t.Errorf("write inside the root failed: %v", err)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("a detached process wrote outside every root")
	}
}
