// Command stdioserver is the MCP server the host's stdio tests launch. It
// speaks MCP on stdin/stdout and offers tools that report what the process
// sees (an environment variable, its pid, its working directory), sleep, and
// exit, so a test can check what reached the child and what happens when it
// dies. With HANG=1 it never answers, for the startup timeout.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

type nameArg struct {
	Name string `json:"name"`
}

type msArg struct {
	Ms int `json:"ms"`
}

func main() {
	if os.Getenv("HANG") == "1" {
		// Sleep rather than select{}. A Go program with no goroutine left to
		// wake would die of a deadlock at once, and that is not a hang.
		time.Sleep(time.Hour)
	}
	srv := server.NewServer(core.ServerInfo{Name: "stdio-demo", Version: "0.0.1"})
	srv.Register(core.TextTool[nameArg]("getenv", "returns an environment variable, or <unset>",
		func(_ core.ToolContext, in nameArg) (string, error) {
			if v, ok := os.LookupEnv(in.Name); ok {
				return v, nil
			}
			return "<unset>", nil
		}))
	srv.Register(core.TextTool[struct{}]("pid", "returns the server's process id",
		func(core.ToolContext, struct{}) (string, error) { return fmt.Sprint(os.Getpid()), nil }))
	srv.Register(core.TextTool[struct{}]("cwd", "returns the server's working directory",
		func(core.ToolContext, struct{}) (string, error) { return os.Getwd() }))
	srv.Register(core.TextTool[msArg]("sleep", "sleeps for ms milliseconds, or until the call is cancelled",
		func(ctx core.ToolContext, in msArg) (string, error) {
			select {
			case <-time.After(time.Duration(in.Ms) * time.Millisecond):
				return "slept", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}))
	srv.Register(core.TextTool[struct{}]("crash", "exits the server process at once, noting its pid in $CRASH_LOG",
		func(core.ToolContext, struct{}) (string, error) {
			if path := os.Getenv("CRASH_LOG"); path != "" {
				if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
					fmt.Fprintln(f, os.Getpid())
					f.Close()
				}
			}
			os.Exit(3)
			return "", nil
		}))
	if err := srv.RunStdio(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
