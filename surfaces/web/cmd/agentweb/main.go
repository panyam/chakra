// Command agentweb serves the agent host over the Connect web bridge — the
// browser analogue of the terminal agentchat, and a thin surface over
// agent/host.App exactly as agentchat is (issue 1196). It loads a host config,
// builds one App, and serves the HostService plus the DockView + Solid frontend
// (issue 1197) on one servicekit listener.
//
// `--demo` skips the config and builds an App over an offline, inexhaustible
// demo provider, so `go run ./cmd/agentweb --demo` serves a working, streaming
// surface with no model or MCP server to configure — the run/screenshot target.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	agentsurfaces "github.com/panyam/chakra/surfaces"
	web "github.com/panyam/chakra/surfaces/web"
)

func main() {
	addr := flag.String("addr", ":8090", "address to listen on")
	configPath := flag.String("config", "", "path to a host config JSON (servers, model, policies)")
	demo := flag.Bool("demo", false, "run over an offline streaming demo provider (no config needed)")
	sessionStore := flag.String("session-store", "", "session persistence backend: memory | sqlite://path.db | redis://host:port | postgres://user:pass@host:port/db (empty = off, sessions live only in memory)")
	var workspace repeatedPath
	flag.Var(&workspace, "workspace", "expose file tools (read/edit/write/list/search) confined to these directories, plus checkpoint /undo. Repeatable: a session that spans repositories passes each one. The first is primary (empty = off; there is no default directory on purpose)")
	workspaceConfig := flag.String("workspace-config", "", "with --workspace, a JSON file adding the exec allowlist (commands the agent may run, sandboxed), language servers, and the repo map. See examples/dogfood/workspace.json")
	noCheckpoint := flag.Bool("no-checkpoint", false, "with --workspace, skip the file snapshot taken before each write, disabling /undo and /checkpoints")
	checkpointStore := flag.String("checkpoint-store", "", "with --workspace, where snapshots live (empty = under the first workspace directory). Worth setting so the store sits outside what the agent can read and edit")
	allowOrigin := flag.String("allow-origin", "", "comma-separated origins, besides localhost, allowed to call the API (e.g. 192.168.1.5 to use agentweb from a phone on the LAN, or *.example.com)")
	active := flag.String("active", "", "override the active chat connection at startup (a name in the config's connections; default = the config's active)")
	flag.Parse()

	// A configured --session-store makes web sessions durable: session_id is a
	// run id, and a session dropped by a restart rehydrates from the store on
	// the next request. Empty leaves the manager as a pure in-memory cache.
	store, err := agentsurfaces.RunStoreFromSpec(*sessionStore)
	if err != nil {
		log.Fatalf("agentweb: %v", err)
	}

	// One factory builds a fresh App per web session from the same config, so a
	// CreateSession over the wire mints an independent conversation. Every App
	// attaches the same store (buildApp), so a session it hosts persists. The
	// SessionManager holds them all; its default session (created at startup)
	// backs an empty session_id, keeping the single-surface flow unchanged.
	opts := appOptions{
		configPath:      *configPath,
		demo:            *demo,
		active:          *active,
		store:           store,
		workspace:       workspace,
		workspaceConfig: *workspaceConfig,
		checkpointStore: *checkpointStore,
		noCheckpoint:    *noCheckpoint,
	}
	factory := func(ctx context.Context) (*host.App, error) {
		return buildApp(opts)
	}
	var mgr *web.SessionManager
	if store != nil {
		mgr = web.NewSessionManagerWithStore(factory, store)
	} else {
		mgr = web.NewSessionManager(factory)
	}
	defaultApp, err := factory(context.Background())
	if err != nil {
		log.Fatalf("agentweb: %v", err)
	}
	if store != nil {
		// Give the default session a run too, so single-surface use (an empty
		// session_id) also survives a restart. AttachRun is create-or-resume, so
		// a restart with the same store resumes the prior default conversation.
		if err := defaultApp.AttachRun(context.Background(), web.DefaultSessionID); err != nil {
			log.Fatalf("agentweb: attaching default session run: %v", err)
		}
	}
	mgr.SetDefault(defaultApp)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("agentweb: %v", err)
	}
	handler := originGuard(newOriginChecker(*allowOrigin), web.HandlerWithSessions(mgr))
	fmt.Fprintf(os.Stderr, "agentweb serving at http://%s/ (Connect: /%s, Ctrl-C to stop)\n", *addr, "chakra.web.v1.HostService")
	if *allowOrigin == "" && !isLoopback(*addr) {
		fmt.Fprintf(os.Stderr, "note: only localhost pages may call the API; pass --allow-origin <this machine's address> to use it from another device\n")
	}
	err = serve(context.Background(), ln, handler)
	mgr.CloseAll()
	if err != nil {
		log.Fatalf("agentweb: serve: %v", err)
	}
}

// isLoopback reports whether addr only listens on the loopback interface.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// appOptions carries the flags buildApp needs, so every session the factory
// mints is built from the same settings.
type appOptions struct {
	configPath      string
	demo            bool
	active          string
	store           agent.RunStore
	workspace       []string
	workspaceConfig string
	checkpointStore string
	noCheckpoint    bool
}

// buildApp builds one App: the factory the SessionManager calls for the default
// session and every CreateSession. In demo mode it wires the offline demo
// provider; otherwise it loads the host config. The web surface renders in the
// browser off the event stream, so the App's own terminal renderer is discarded
// (output to io.Discard) — events reach clients through Subscribe / Watch. A
// non-nil store is attached to every App (host.WithRunStore) so each session,
// including the default, persists its turns — the invariant the store-backed
// SessionManager relies on when it rehydrates a dropped session.
func buildApp(o appOptions) (*host.App, error) {
	var storeOpt []host.AppOption
	if o.store != nil {
		storeOpt = append(storeOpt, host.WithRunStore(o.store))
	}
	// Appended to whichever App this call constructs, so the demo and config
	// paths cannot drift into offering different tools. Nil when --workspace
	// is unset, leaving both paths as they were. It is built after the host
	// config because exec's output view follows whether that config offloads.
	withWorkspace := func(cfg *host.Config) ([]host.AppOption, error) {
		wsCfg := agentsurfaces.WorkspaceConfig{Roots: o.workspace, NoCheckpoint: o.noCheckpoint, CheckpointStore: o.checkpointStore}
		if err := wsCfg.LoadFile(o.workspaceConfig); err != nil {
			return nil, err
		}
		wsCfg.Offloading = cfg.Offload != nil
		wsExts, err := agentsurfaces.WorkspaceExtensions(wsCfg)
		if err != nil {
			return nil, err
		}
		if len(wsExts) > 0 {
			return append(storeOpt, host.WithExtension(wsExts...)), nil
		}
		return storeOpt, nil
	}
	if o.demo {
		// Wire two delegate personas + a low offload threshold so a demo turn
		// exercises the whole observability surface: the scripted provider fans
		// out to researcher/analyst (sub-agent tree + tool calls), the
		// researcher's long reply comes back offloaded (tool inspector stub),
		// and reported usage moves the budget gauges.
		cfg := &host.Config{
			Model: host.ModelConfig{BaseURL: "http://demo", Model: "agentweb-demo"},
			SubAgents: []host.SubAgentConfig{
				{Name: "researcher", Description: "gathers background on a topic", Instructions: "You are a research sub-agent."},
				{Name: "analyst", Description: "assesses risks and trade-offs", Instructions: "You are a risk-analysis sub-agent."},
			},
			Offload: &host.OffloadConfig{ThresholdBytes: 300, PreviewLen: 120},
			// Gate the researcher delegate behind an approval "ask" (everything
			// else auto-runs) so a demo turn raises exactly one elicitation. The
			// prompt broadcasts to every surface; answering it on any surface
			// retracts it on the others, which is what this build (issue 1199)
			// exists to demonstrate.
			Approval: &host.ApprovalConfig{Mode: "allow", Rules: map[string]string{"researcher": "ask"}},
		}
		opts, err := withWorkspace(cfg)
		if err != nil {
			return nil, err
		}
		return host.NewApp(cfg, io.Discard, strings.NewReader(""),
			append(opts,
				host.WithProvider(demoProvider{}),
				host.WithElicitationUI(demoElicitUI),
			)...,
		)
	}
	if o.configPath == "" {
		return nil, fmt.Errorf("--config <path> is required (or pass --demo for the offline streaming demo)")
	}
	cfg, err := host.LoadConfig(o.configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if o.active != "" {
		if cfg.Connections == nil {
			return nil, fmt.Errorf("--active %q: the config has no connections", o.active)
		}
		if _, ok := cfg.Connections.Connections[o.active]; !ok {
			return nil, fmt.Errorf("--active %q: no such connection in the config", o.active)
		}
		cfg.Connections.Active = o.active
	}
	opts, err := withWorkspace(cfg)
	if err != nil {
		return nil, err
	}
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), opts...)
}

// repeatedPath collects a flag given more than once, so --workspace can name
// several directories. The stdlib flag package has no slice type and the
// alternative, splitting one value on commas, breaks on a path containing one.
type repeatedPath []string

func (r *repeatedPath) String() string { return strings.Join(*r, ",") }

func (r *repeatedPath) Set(v string) error {
	if v == "" {
		return fmt.Errorf("empty workspace path")
	}
	*r = append(*r, v)
	return nil
}
