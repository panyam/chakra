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
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	agentsurfaces "github.com/panyam/chakra/surfaces"
	web "github.com/panyam/chakra/surfaces/web"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
	"github.com/panyam/mcpkit/core"
)

func main() {
	// App Engine and similar hosts say which port to listen on through $PORT.
	defaultAddr := ":8090"
	if port := os.Getenv("PORT"); port != "" {
		defaultAddr = ":" + port
	}
	addr := flag.String("addr", defaultAddr, "address to listen on (default :$PORT when PORT is set)")
	configPath := flag.String("config", "", "path to a host config JSON (servers, model, policies)")
	// A host that can't pass flags (App Engine's app.yaml sets env_variables)
	// sets these two through AGENTWEB_DEMO and AGENTWEB_ALLOW_ORIGIN instead.
	demo := flag.Bool("demo", os.Getenv("AGENTWEB_DEMO") != "", "run over an offline streaming demo provider (no config needed; default on when AGENTWEB_DEMO is set)")
	sessionStore := flag.String("session-store", "", "session persistence backend: memory | sqlite://path.db | redis://host:port | postgres://user:pass@host:port/db (empty = off, sessions live only in memory)")
	var workspace repeatedPath
	flag.Var(&workspace, "workspace", "expose file tools (read/edit/write/list/search) confined to these directories, plus checkpoint /undo. Repeatable: a session that spans repositories passes each one. The first is primary (empty = off; there is no default directory on purpose)")
	workspaceConfig := flag.String("workspace-config", "", "with --workspace, a JSON file adding the exec allowlist (commands the agent may run, sandboxed), language servers, and the repo map. See examples/dogfood/workspace.json")
	noCheckpoint := flag.Bool("no-checkpoint", false, "with --workspace, skip the file snapshot taken before each write, disabling /undo and /checkpoints")
	checkpointStore := flag.String("checkpoint-store", "", "with --workspace, where snapshots live (empty = under the first workspace directory). Worth setting so the store sits outside what the agent can read and edit")
	allowOrigin := flag.String("allow-origin", os.Getenv("AGENTWEB_ALLOW_ORIGIN"), "comma-separated origins, besides localhost, allowed to call the API (e.g. 192.168.1.5 to use agentweb from a phone on the LAN, or *.example.com)")
	watchWindow := flag.Duration("watch-window", envDuration("AGENTWEB_WATCH_WINDOW"), "end each event stream after this long so the client reconnects (e.g. 3s). For hosts that buffer whole responses, such as App Engine standard; 0 keeps streams open")
	byok := flag.Bool("byok", os.Getenv("AGENTWEB_BYOK") != "", "bring your own key: each CreateSession names its provider and key, there is no default session, and sessions are reachable only by id. --config, when given, supplies everything but the model (default on when AGENTWEB_BYOK is set)")
	accessToken := flag.String("access-token", os.Getenv("AGENTWEB_ACCESS_TOKEN"), "require this token in the X-Agentweb-Token header on every API call (default $AGENTWEB_ACCESS_TOKEN). Set it whenever the server is reachable by anyone but you")
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
	svcOpts := []web.Option{web.WithWatchWindow(*watchWindow)}
	if *byok {
		// Every session is built from the provider its CreateSession names, and
		// none exists until one does: a default session would need a key of its
		// own, and would be reachable by anyone.
		svcOpts = append(svcOpts,
			web.WithSessionBuilder(func(ctx context.Context, spec *webv1.ProviderSpec) (*host.App, error) {
				return buildSessionApp(opts, spec)
			}),
			web.WithPrivateSessions())
		serveWith(*addr, *allowOrigin, *accessToken, mgr, svcOpts)
		return
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
	serveWith(*addr, *allowOrigin, *accessToken, mgr, svcOpts)
}

// serveWith listens on addr and serves mgr's sessions behind the origin guard
// and, when token is set, the token guard, until the process is stopped.
func serveWith(addr, allowOrigin, token string, mgr *web.SessionManager, svcOpts []web.Option) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("agentweb: %v", err)
	}
	handler := originGuard(newOriginChecker(allowOrigin), tokenGuard(token, web.HandlerWithSessions(mgr, svcOpts...)))
	fmt.Fprintf(os.Stderr, "agentweb serving at http://%s/ (Connect: /%s, Ctrl-C to stop)\n", addr, "chakra.web.v1.HostService")
	if allowOrigin == "" && !isLoopback(addr) {
		fmt.Fprintf(os.Stderr, "note: only localhost pages may call the API; pass --allow-origin <this machine's address> to use it from another device\n")
	}
	if token == "" && !isLoopback(addr) {
		fmt.Fprintf(os.Stderr, "note: no --access-token, so anyone who can reach this address can use the API\n")
	}
	err = serve(context.Background(), ln, handler)
	mgr.CloseAll()
	if err != nil {
		log.Fatalf("agentweb: serve: %v", err)
	}
}

// envDuration reads a duration from the environment, or 0 when unset. A
// malformed value is fatal, so a typo in app.yaml doesn't quietly turn into 0.
func envDuration(name string) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("agentweb: %s=%q: %v", name, v, err)
	}
	return d
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
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), append(opts, host.WithElicitationUI(awaitSurface))...)
}

// buildSessionApp builds a --byok session's App from the provider its
// CreateSession named. "demo" gets the offline demo App; anything else is a
// connection built with the browser's key, over --config's servers and
// policies when one is given and a bare config otherwise. Errors name the field
// to fix and never include the key.
func buildSessionApp(o appOptions, spec *webv1.ProviderSpec) (*host.App, error) {
	if spec == nil || spec.GetType() == "" {
		return nil, fmt.Errorf("this server needs a provider for each session: set provider.type (and model and api_key)")
	}
	if spec.GetType() == "demo" {
		o.demo = true
		return buildApp(o)
	}
	if spec.GetModel() == "" {
		return nil, fmt.Errorf("provider %q: model is required", spec.GetType())
	}
	conn := host.ConnectionConfig{Type: spec.GetType(), BaseURL: spec.GetBaseUrl(), Model: spec.GetModel(), APIKey: spec.GetApiKey()}
	p, err := host.DefaultProviderBuilder(conn)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", spec.GetType(), err)
	}
	cfg := &host.Config{}
	if o.configPath != "" {
		if cfg, err = host.LoadConfig(o.configPath); err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		cfg.Connections = nil
	}
	// The model label the surfaces show comes from here; the provider does
	// the talking.
	cfg.Model = host.ModelConfig{BaseURL: "byok://" + spec.GetType(), Model: spec.GetModel()}
	var opts []host.AppOption
	if o.store != nil {
		opts = append(opts, host.WithRunStore(o.store))
	}
	return host.NewApp(cfg, io.Discard, strings.NewReader(""), append(opts, host.WithProvider(p), host.WithElicitationUI(awaitSurface))...)
}

// awaitSurface is the local elicitation responder outside --demo. agentweb has
// no terminal, and the host's default responder reads the empty input above,
// hits EOF and declines every ask before a browser can answer it (issue 121).
// This one never answers, so the first surface to call RespondToAsk wins, and
// a cancelled turn still unblocks it through ctx.
func awaitSurface(ctx context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
	<-ctx.Done()
	return core.ElicitationResult{}, ctx.Err()
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
