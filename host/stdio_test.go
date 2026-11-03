package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
)

var (
	stdioServerOnce sync.Once
	stdioServerBin  string
	stdioServerErr  error
)

// stdioServer builds testdata/stdioserver once per test binary.
func stdioServer(t *testing.T) string {
	t.Helper()
	stdioServerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "chakra-stdioserver")
		if err != nil {
			stdioServerErr = err
			return
		}
		stdioServerBin = filepath.Join(dir, "stdioserver")
		cmd := exec.Command("go", "build", "-o", stdioServerBin, "./testdata/stdioserver")
		out, err := cmd.CombinedOutput()
		if err != nil {
			stdioServerErr = errors.New(string(out))
		}
	})
	if stdioServerErr != nil {
		t.Fatalf("building the stdio demo server: %v", stdioServerErr)
	}
	return stdioServerBin
}

func stdioConfig(t *testing.T, sc ServerConfig) *Config {
	sc.ID = "demo"
	if sc.Command == "" {
		sc.Command = stdioServer(t)
	}
	return &Config{Model: ModelConfig{BaseURL: "http://unused", Model: "stub"}, Servers: []ServerConfig{sc}}
}

func stdioApp(t *testing.T, sc ServerConfig) *App {
	t.Helper()
	app, err := NewApp(stdioConfig(t, sc), &strings.Builder{}, strings.NewReader(""), WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func callText(t *testing.T, app *App, tool string, args map[string]any) (string, error) {
	t.Helper()
	res, err := app.sources.Call(context.Background(), tool, args)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String(), nil
}

// A stdio server connects like an HTTP one: it reaches ready, its tools are
// offered, and a call runs in the child.
func TestStdioServerConnectsAndServesTools(t *testing.T) {
	app := stdioApp(t, ServerConfig{})
	defer app.Close()
	if st, _ := app.servers.group.State("demo"); st != client.StateReady {
		t.Fatalf("state %v, want ready", st)
	}
	tools, err := app.sources.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, d := range tools {
		names[d.Name] = true
	}
	if !names["getenv"] || !names["pid"] {
		t.Fatalf("tools %v", names)
	}
	pid, err := callText(t, app, "pid", nil)
	if err != nil || pid == strconv.Itoa(os.Getpid()) {
		t.Fatalf("pid %q err %v", pid, err)
	}
}

// The child sees PATH, HOME, each variable named in EnvPass, and each one set
// in Env. It sees none of the agent's other variables.
func TestStdioServerEnvironmentIsAllowlisted(t *testing.T) {
	t.Setenv("CHAKRA_TEST_SECRET", "s3cret")
	t.Setenv("CHAKRA_TEST_PASS", "passed")
	app := stdioApp(t, ServerConfig{EnvPass: []string{"CHAKRA_TEST_PASS", "CHAKRA_TEST_ABSENT"}, Env: map[string]string{"CHAKRA_TEST_SET": "set"}})
	defer app.Close()
	for name, want := range map[string]string{
		"CHAKRA_TEST_SECRET": "<unset>",
		"CHAKRA_TEST_PASS":   "passed",
		"CHAKRA_TEST_SET":    "set",
		"CHAKRA_TEST_ABSENT": "<unset>",
		"PATH":               os.Getenv("PATH"),
	} {
		got, err := callText(t, app, "getenv", map[string]any{"name": name})
		if err != nil || got != want {
			t.Errorf("%s = %q (err %v), want %q", name, got, err, want)
		}
	}
}

func TestStdioServerRunsInDir(t *testing.T) {
	dir := t.TempDir()
	app := stdioApp(t, ServerConfig{Dir: dir})
	defer app.Close()
	got, err := callText(t, app, "cwd", nil)
	real, _ := filepath.EvalSymlinks(dir)
	if gotReal, _ := filepath.EvalSymlinks(got); err != nil || gotReal != real {
		t.Fatalf("cwd %q (err %v), want %q", got, err, dir)
	}
}

// A server that never answers initialize fails at StartupTimeoutSec instead
// of holding up boot.
func TestStdioServerStartupTimeout(t *testing.T) {
	start := time.Now()
	app := stdioApp(t, ServerConfig{Env: map[string]string{"HANG": "1"}, StartupTimeoutSec: 1})
	defer app.Close()
	if took := time.Since(start); took < time.Second || took > 10*time.Second {
		t.Fatalf("boot took %v, want about the 1s startup timeout", took)
	}
	if st, _ := app.servers.group.State("demo"); st == client.StateReady {
		t.Fatalf("a server that never initialized is ready")
	}
}

// CallTimeoutSec ends a tool call that runs too long, and names the server and
// the limit. (The demo is an mcpkit stdio server, which handles one request
// at a time, so it can't hear the cancel until the sleep ends.)
func TestServerCallTimeout(t *testing.T) {
	app := stdioApp(t, ServerConfig{CallTimeoutSec: 1})
	defer app.Close()
	start := time.Now()
	_, err := callText(t, app, "sleep", map[string]any{"ms": 3000})
	if took := time.Since(start); err == nil || took > 2500*time.Millisecond {
		t.Fatalf("err %v after %v, want a timeout near 1s", err, took)
	}
	if !strings.Contains(err.Error(), `server "demo" took longer than 1s`) {
		t.Fatalf("error %q doesn't name the server and the limit", err)
	}
}

// A crash mid-call reports that call as unavailable, without running it
// again, and the next call starts a fresh process.
func TestStdioServerRestartsOnTheCallAfterACrash(t *testing.T) {
	crashLog := filepath.Join(t.TempDir(), "crashes")
	app := stdioApp(t, ServerConfig{Env: map[string]string{"CRASH_LOG": crashLog}})
	defer app.Close()
	before, err := callText(t, app, "pid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callText(t, app, "crash", nil); !errors.Is(err, agent.ErrNotAvailableNow) {
		t.Fatalf("the crashing call: %v, want ErrNotAvailableNow", err)
	}
	after, err := callText(t, app, "pid", nil)
	if err != nil {
		t.Fatalf("the call after the crash: %v", err)
	}
	if after == before {
		t.Fatalf("pid %s before and after, want a fresh process", after)
	}
	if alive(before) {
		t.Errorf("the crashed process %s is still running", before)
	}
	if log, _ := os.ReadFile(crashLog); strings.Count(string(log), "\n") != 1 {
		t.Errorf("crash ran %d times, want once (a replay would run it again on the fresh process):\n%s", strings.Count(string(log), "\n"), log)
	}
}

// Closing the App ends the child.
func TestStdioServerStopsWhenTheAppCloses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal 0 probe")
	}
	app := stdioApp(t, ServerConfig{})
	pid, err := callText(t, app, "pid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !alive(pid) {
		t.Fatalf("control: %s isn't running before Close", pid)
	}
	app.Close()
	deadline := time.Now().Add(10 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %s outlived App.Close", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func alive(pid string) bool {
	n, err := strconv.Atoi(pid)
	if err != nil {
		return false
	}
	return syscall.Kill(n, 0) == nil
}

func TestServerConfigNeedsURLOrCommand(t *testing.T) {
	base := func(sc ServerConfig) error {
		sc.ID = "x"
		return (&Config{Model: ModelConfig{BaseURL: "u", Model: "m"}, Servers: []ServerConfig{sc}}).Validate()
	}
	for name, c := range map[string]struct {
		sc   ServerConfig
		want string
	}{
		"neither":         {ServerConfig{}, "url or command"},
		"both":            {ServerConfig{URL: "http://x", Command: "srv"}, "not both"},
		"command auth":    {ServerConfig{Command: "srv", Auth: &AuthConfig{Type: "bearer", TokenEnv: "T"}}, "auth"},
		"url with args":   {ServerConfig{URL: "http://x", Args: []string{"a"}}, "command"},
		"url with env":    {ServerConfig{URL: "http://x", EnvPass: []string{"A"}}, "command"},
		"command is fine": {ServerConfig{Command: "srv", Args: []string{"--stdio"}}, ""},
	} {
		err := base(c.sc)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want an error naming %q", name, err, c.want)
		}
	}
}
