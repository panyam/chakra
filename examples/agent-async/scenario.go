// Command agent-async demonstrates an agent managing its own asynchronous
// work through conversation. It subscribes to an event stream and installs a
// standing "when X happens, do Y" trigger. It runs a task tool, and later the
// event wakes it to act on the trigger. All of this goes through host
// meta-tools, with no config.
//
// It runs the whole flow deterministically against a scripted StubProvider
// (no LLM, no network), so it doubles as a golden-transcript test of chakra's
// scriptable loop. Point a real model at the same server with --model to
// watch it improvise. The host module's tests (host/tasks_test.go) exercise
// background-detach on long tasks. There the task runs past its grace window
// and then notifies via a task.completed event. Here the report finishes fast.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	tasksext "github.com/panyam/mcpkit/ext/tasks"
	"github.com/panyam/mcpkit/server"
)

type userData struct {
	Email string `json:"email"`
}

// buildServer builds the app-domain MCP server for the agent to drive. It has
// a user.created event source, a send_email tool, and a task-backed
// long_report tool.
func buildServer() (*server.Server, func(context.Context, userData) error, *[]string) {
	srv := server.NewServer(core.ServerInfo{Name: "agent-async-demo", Version: "0.1.0"})

	src, yield := events.NewYieldingSource[userData](events.EventDef{
		Name: "user.created", Description: "a new user signed up",
	})
	events.Register(events.Config{Sources: []events.EventSource{src}, Server: srv, UnsafeAnonymousPrincipal: "demo"})

	var sent []string
	srv.Register(core.TextTool[struct {
		To string `json:"to"`
	}]("send_email", "send an email to an address",
		func(ctx core.ToolContext, in struct {
			To string `json:"to"`
		}) (string, error) {
			sent = append(sent, in.To)
			return "email sent to " + in.To, nil
		}))

	srv.Register(core.TypedTool[struct{}, core.ToolResponse]("long_report",
		"build a long report (runs as a background task)",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResponse, error) {
			tc := tasksext.GetTaskContext(ctx)
			if tc == nil {
				return core.GoAsyncResult{}, nil
			}
			time.Sleep(50 * time.Millisecond) // stand in for real work
			return core.TextResult("report ready: 42 pages"), nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportRequired}),
	))
	tasksext.Register(tasksext.Config{Server: srv})

	return srv, yield, &sent
}

// serve runs the app-domain server as a standalone streamable-HTTP MCP server.
// config.json points the live chat/web surfaces at it, so they have real tools
// to drive. The scripted scenario uses the same buildServer over httptest
// instead.
func serve(addr string) error {
	srv, _, _ := buildServer()
	log.Printf("agent-async demo server on http://localhost%s/mcp", addr)
	return srv.Run(addr)
}

// stubScript is the model's scripted side of the conversation. It pins the
// moves a real model would make, so the demo is deterministic.
func stubScript() *agent.StubProvider {
	tc := func(id, name, args string) agent.ToolCall {
		return agent.ToolCall{ID: id, Name: name, Args: core.NewRawJSON(json.RawMessage(args))}
	}
	return agent.NewStubProvider(
		// User: "email me when users are created". Model subscribes + installs a trigger.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("s1", "subscribe_events", `{"server":"app","name":"user.created"}`)}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("t1", "create_trigger",
			`{"event":"user.created","instructions":"send a welcome email to the new user's address","label":"welcome"}`)}},
		agent.StubTurn{Text: "Set — I'll welcome-email every new user."},
		// User: "start the quarterly report". Model kicks off the task.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("r1", "long_report", `{}`)}},
		agent.StubTurn{Text: "The quarterly report is ready — 42 pages."},
		// Proactive turn: user.created fired → model sends the welcome email.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("e1", "send_email", `{"to":"ada@example.com"}`)}},
		agent.StubTurn{Text: "Welcomed ada@example.com."},
	)
}

// runScenario wires the host App to the demo server and plays the narrative.
// It yields the event between the setup turns and the report kickoff. out
// receives the full transcript. provider nil uses the deterministic script.
func runScenario(out *syncWriter, provider agent.Provider) error {
	srv, yield, _ := buildServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	// Load the SAME config.json as the live chat/web surfaces. It holds the
	// instructions, metaTools, and the app server. Then point its server at
	// the in-process httptest instance for the deterministic run. The scripted
	// StubProvider (below) is the only piece that cannot live in JSON.
	// config.json's model is for a live model, and WithProvider bypasses it
	// here.
	cfg, err := host.LoadConfig("config.json")
	if err != nil {
		return err
	}
	cfg.Servers[0].URL = ts.URL + "/mcp"
	opts := []host.AppOption{}
	if provider != nil {
		opts = append(opts, host.WithProvider(provider))
	} else {
		opts = append(opts, host.WithProvider(stubScript()))
	}

	app, err := host.NewApp(cfg, out, strings.NewReader(""), opts...)
	if err != nil {
		return err
	}
	defer app.Close()

	ctx := context.Background()
	fmt.Fprintln(out, "> email me whenever a user is created")
	if err := app.RunTurn(ctx, "email me whenever a user is created"); err != nil {
		return err
	}
	fmt.Fprintln(out, "> start the quarterly report")
	if err := app.RunTurn(ctx, "start the quarterly report"); err != nil {
		return err
	}

	// A user signs up. The standing trigger wakes the agent, and the agent emails them.
	fmt.Fprintln(out, "(a user signs up...)")
	if err := yield(ctx, userData{Email: "ada@example.com"}); err != nil {
		return err
	}
	waitForOutput(out, "Welcomed", 3*time.Second)
	return nil
}
