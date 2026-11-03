package host

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// commandOptions are the client options for a server launched from
// ServerConfig.Command. The process gets an environment built by commandEnv
// rather than the agent's. After a crash the client reconnects without
// replaying. The call that hit the crash fails (availabilitySource reports it
// as not available now), and the next call starts a fresh process. Replaying
// would re-run a tool that may already have run before the crash.
func commandOptions(sc ServerConfig, logger *slog.Logger) []client.ClientOption {
	cmdOpts := []client.CommandOption{client.WithCleanEnv(), client.WithEnv(commandEnv(sc)...)}
	if sc.Dir != "" {
		cmdOpts = append(cmdOpts, client.WithDir(sc.Dir))
	}
	if logger != nil {
		cmdOpts = append(cmdOpts, client.WithStderr(&stderrLog{logger: logger, server: sc.ID}))
	}
	opts := []client.ClientOption{
		client.WithCommandTransport(sc.Command, sc.Args, cmdOpts...),
		client.WithMaxRetries(1),
		client.WithReconnectBackoff(50 * time.Millisecond),
		client.WithReconnectWithoutReplay(),
	}
	if sc.StartupTimeoutSec > 0 {
		opts = append(opts, client.WithConnectTimeout(time.Duration(sc.StartupTimeoutSec)*time.Second))
	}
	return opts
}

// commandEnv builds the whole environment a command server starts with. It
// takes PATH and HOME from the agent, then the EnvPass names the agent has,
// then Env, which wins. The result is sorted so the child's environment
// doesn't depend on map order.
func commandEnv(sc ServerConfig) []string {
	vars := map[string]string{}
	for _, name := range append([]string{"PATH", "HOME"}, sc.EnvPass...) {
		if v, ok := os.LookupEnv(name); ok {
			vars[name] = v
		}
	}
	for k, v := range sc.Env {
		vars[k] = v
	}
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// stderrLog writes a command server's stderr to the host logger a line at a
// time, tagged with the server. A server's own diagnostics aren't lost, and
// they don't end up on the terminal the surface draws.
type stderrLog struct {
	logger *slog.Logger
	server string
	mu     sync.Mutex
	buf    bytes.Buffer
}

func (w *stderrLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			w.buf.WriteString(line)
			return len(p), nil
		}
		w.logger.Info("mcp server stderr", "server", w.server, "line", strings.TrimRight(line, "\r\n"))
	}
}

// timeoutSource ends each tool call after d, for ServerConfig.CallTimeoutSec.
type timeoutSource struct {
	inner    agent.ToolSource
	d        time.Duration
	serverID string
}

func (s *timeoutSource) Tools(ctx context.Context) ([]core.ToolDef, error) { return s.inner.Tools(ctx) }

func (s *timeoutSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.d)
	defer cancel()
	res, err := s.inner.Call(ctx, name, args)
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("tool %q on server %q took longer than %s: %w", name, s.serverID, s.d, err)
	}
	return res, err
}

func callTimeout(sc ServerConfig) time.Duration {
	return time.Duration(sc.CallTimeoutSec) * time.Second
}
