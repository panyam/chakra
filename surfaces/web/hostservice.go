// Package web exposes host.App over a Connect RPC bridge with a live event
// stream, so a browser can drive the same surface-agnostic host as the
// terminal agentchat (issue 1196, epic 1193). The bridge is a thin projection.
// It holds only a SessionManager of *App and translates each RPC to an App
// method. No web or proto type leaks back into the host package
// (CONSTRAINTS.md A4). The host is still surface-agnostic, and this module
// owns all the web wiring.
//
// One server can host many concurrent conversations. Each RPC carries a
// session_id routing field, and the SessionManager uses it to select the
// RPC's App. An empty session_id resolves to a default session created at
// startup. So a single-surface client written before multi-session support
// still works unchanged.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/panyam/chakra/host"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
	"github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1/webv1connect"
	"github.com/panyam/mcpkit/core"
)

// HostService is the Connect handler that bridges HostService RPCs to the App
// selected by each request's session_id. Embedding UnimplementedHostServiceHandler
// keeps it forward-compatible when the proto gains methods.
type HostService struct {
	webv1connect.UnimplementedHostServiceHandler
	sessions *SessionManager
	// watchWindow, when set, ends each Watch after this long. See WithWatchWindow.
	watchWindow time.Duration
	// build, when set, makes each CreateSession's App from the request's
	// provider spec. See WithSessionBuilder.
	build SessionBuilder
	// private hides sessions from anyone without the id. See WithPrivateSessions.
	private bool
	// profiles are the other kinds of session this server builds. See
	// WithProfile.
	profiles []Profile
}

// Profile is a kind of session a server builds besides its default, such as
// agentweb's coding agent (#178). Sessions of every profile live in the same
// SessionManager, so the Sessions panel and ListWebSessions show them all.
type Profile struct {
	// Name is what CreateSessionRequest.profile asks for, such as "coding".
	// "" and "chat" mean the default and can't be used.
	Name string

	// Path is where the page for this profile is served, such as "/code/".
	// The page is the same shell, marked with the profile, and it creates a
	// session of this profile when the browser has none. Empty serves no page.
	Path string

	// Build makes one session's App. Its error goes back to the caller as
	// FailedPrecondition, so it should say what the server is missing.
	Build func(ctx context.Context) (*host.App, error)
}

// WithProfile adds a kind of session CreateSession can build by name.
func WithProfile(p Profile) Option {
	return func(s *HostService) { s.profiles = append(s.profiles, p) }
}

// SessionBuilder makes a session's App from the provider in a CreateSession
// request. spec may be nil when the request named none, and the builder
// decides whether that's allowed. The caller gets its error as
// InvalidArgument, so the error should say what to fix and never echo the key.
type SessionBuilder func(ctx context.Context, spec *webv1.ProviderSpec) (*host.App, error)

// WithSessionBuilder makes CreateSession build each App with b from the
// request's provider spec, instead of with the SessionManager's factory. With
// it a browser brings its own key, and the server needs none of its own.
func WithSessionBuilder(b SessionBuilder) Option {
	return func(s *HostService) { s.build = b }
}

// WithPrivateSessions makes a session reachable only by its id. An empty
// session_id no longer routes to a default session, and ListWebSessions is
// refused. Imagine a stranger reaches the server. They can't find another
// person's session, so they can't drive it or use its key. Session ids are 128
// random bits, so holding one is the credential.
func WithPrivateSessions() Option {
	return func(s *HostService) { s.private = true }
}

// Option configures a HostService.
type Option func(*HostService)

// WithWatchWindow ends each Watch stream after d, and the client reconnects
// with its resume_after count. Some hosts, such as App Engine standard, buffer
// whole responses. They never deliver a stream that is still open, so this
// turns Watch into a long poll there. Zero, the default, keeps the stream open.
func WithWatchWindow(d time.Duration) Option {
	return func(s *HostService) { s.watchWindow = d }
}

// NewHostService wraps a single App as the default session, with no factory.
// An empty session_id routes to app, so the single-surface flow works
// unchanged. CreateSession is unavailable without a factory. Use
// NewHostServiceWithSessions to host multiple concurrent conversations.
func NewHostService(app *host.App) *HostService {
	mgr := NewSessionManager(nil)
	mgr.SetDefault(app)
	return &HostService{sessions: mgr}
}

// NewHostServiceWithSessions bridges a SessionManager, so one server hosts many
// concurrent conversations. The manager's default session backs the empty
// session_id; CreateSession mints new ones from the manager's factory.
func NewHostServiceWithSessions(mgr *SessionManager, opts ...Option) *HostService {
	s := &HostService{sessions: mgr}
	for _, o := range opts {
		o(s)
	}
	return s
}

// appFor resolves a request's session_id to its App, or a Connect CodeNotFound
// error when no such session exists. An empty session_id resolves to the
// default session. A store-backed manager rehydrates a session that is not
// cached, for example after a restart. ctx covers the store round-trips it
// makes. An unknown run is still CodeNotFound.
func (s *HostService) appFor(ctx context.Context, sessionID string) (*host.App, error) {
	if s.private && sessionID == "" {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no session_id: create a session first"))
	}
	app, ok := s.sessions.Get(ctx, sessionID)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %q not found", sessionID))
	}
	return app, nil
}

// Watch streams the selected session's event log. It replays the retained
// backlog from offset 0 (App.Subscribe), then sends live events until the
// client disconnects. Watch cancels the derived ctx on return. So the drain
// goroutine and its Subscription exit even when Send fails before the request
// ctx is observed.
func (s *HostService) Watch(ctx context.Context, req *connect.Request[webv1.WatchRequest], stream *connect.ServerStream[webv1.Frame]) error {
	app, err := s.appFor(ctx, req.Msg.GetSessionId())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	if s.watchWindow > 0 {
		ctx, cancel = context.WithTimeout(ctx, s.watchWindow)
	}
	defer cancel()
	// Open the stream immediately with a ready sentinel (empty kind). The
	// Connect protocol flushes response headers on the first Send, so without
	// this a client attaching to an idle session would block on Watch until the
	// first real event. Clients skip a frame whose kind is empty.
	if err := stream.Send(&webv1.Frame{}); err != nil {
		return err
	}
	skip := req.Msg.GetResumeAfter()
	for ev := range app.Subscribe(ctx) {
		if skip > 0 {
			skip--
			continue
		}
		if err := stream.Send(eventToFrame(ev)); err != nil {
			return err
		}
	}
	return nil
}

// Submit runs one conversational turn on the selected session. The turn's events
// stream on that session's Watch, and Submit returns when the turn finishes.
// A failed turn maps to a Connect error, and Watch also announces the failure
// as a HostTurnFailed frame. While a turn is running, the input is queued as a
// steer for it (App.Submit) and Submit returns at once with Steered set.
func (s *HostService) Submit(ctx context.Context, req *connect.Request[webv1.SubmitRequest]) (*connect.Response[webv1.SubmitResponse], error) {
	app, err := s.appFor(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	var opts host.TurnOptions
	if raw := req.Msg.GetResponseSchema(); raw != "" {
		if !json.Valid([]byte(raw)) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("response_schema is not JSON"))
		}
		opts.ResponseSchema = core.NewRawJSON(json.RawMessage(raw))
	}
	steered, err := app.Submit(ctx, req.Msg.GetInput(), opts)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&webv1.SubmitResponse{Steered: steered}), nil
}

// Dispatch runs a slash command on the selected session and returns its
// CmdResult as {kind, json}. An unknown command is CodeInvalidArgument (a client
// can retry it as a turn); any other command error is CodeInternal.
func (s *HostService) Dispatch(ctx context.Context, req *connect.Request[webv1.DispatchRequest]) (*connect.Response[webv1.DispatchResponse], error) {
	app, err := s.appFor(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	res, err := app.Dispatch(ctx, req.Msg.GetLine())
	if errors.Is(err, host.ErrUnknownCommand) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&webv1.DispatchResponse{
		Kind:    string(res.Kind),
		Payload: marshalOrMinimal(res, host.HostEventKind(res.Kind)),
		Quit:    res.Quit,
	}), nil
}

// RespondToAsk answers a pending elicitation on the selected session. A client
// reads the AskID off a Frame and passes it here. First responder wins. An
// unknown or already-answered ask is CodeFailedPrecondition, because it is app
// state and not a transport failure. An empty By defaults to "web" so the
// resolved frame names the surface.
func (s *HostService) RespondToAsk(ctx context.Context, req *connect.Request[webv1.RespondToAskRequest]) (*connect.Response[webv1.RespondToAskResponse], error) {
	app, err := s.appFor(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	var result core.ElicitationResult
	if raw := req.Msg.GetResult(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	by := req.Msg.GetBy()
	if by == "" {
		by = "web"
	}
	// ask_id holds the event-log offset where the ask was announced (E2's
	// offset-based RespondToAsk). Cast it from int64 to int at the boundary.
	// Resolution goes through the log barrier. So a stale or already-answered
	// offset returns the log's ErrAlreadyResolved / ErrOffsetOutOfRange. That
	// error is app state, so it is reported as a failed precondition and not as
	// a transport failure.
	if err := app.RespondToAsk(int(req.Msg.GetAskId()), result, by); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&webv1.RespondToAskResponse{}), nil
}

// ListSessions returns one page of the selected session's persisted RunStore
// runs. Without a configured RunStore, persistence is off, and ListSessions
// returns CodeFailedPrecondition because that is app state. ListSessions pages
// the runs inside one conversation. ListWebSessions returns the roster of
// concurrent conversations.
func (s *HostService) ListSessions(ctx context.Context, req *connect.Request[webv1.ListSessionsRequest]) (*connect.Response[webv1.ListSessionsResponse], error) {
	app, err := s.appFor(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	page, err := app.SessionsPage(ctx, req.Msg.GetCursor())
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	runs, _ := json.Marshal(page.Runs)
	return connect.NewResponse(&webv1.ListSessionsResponse{
		Runs:        runs,
		HasMore:     page.HasMore,
		ActiveRunId: app.RunID(),
	}), nil
}

// GetStatus returns the selected session's active model label and run id for
// the status line. It also returns the connections for the header's model
// picker.
func (s *HostService) GetStatus(ctx context.Context, req *connect.Request[webv1.GetStatusRequest]) (*connect.Response[webv1.GetStatusResponse], error) {
	app, err := s.appFor(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	names, active := app.Providers()
	return connect.NewResponse(&webv1.GetStatusResponse{
		ModelLabel:     app.ModelLabel(),
		RunId:          app.RunID(),
		Providers:      names,
		ActiveProvider: active,
	}), nil
}

// CreateSession builds a new App from the server's config as a fresh
// conversation, and returns its session_id. In the single-App case the server
// has no factory and returns CodeFailedPrecondition. A factory error is
// CodeInternal.
func (s *HostService) CreateSession(ctx context.Context, req *connect.Request[webv1.CreateSessionRequest]) (*connect.Response[webv1.CreateSessionResponse], error) {
	if name := req.Msg.GetProfile(); name != "" && name != "chat" {
		return s.createProfileSession(ctx, name)
	}
	if s.build != nil {
		app, err := s.build(ctx, req.Msg.GetProvider())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		id, err := s.sessions.Adopt(ctx, app)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&webv1.CreateSessionResponse{SessionId: id}), nil
	}
	id, _, err := s.sessions.Create(ctx)
	if errors.Is(err, ErrNoSessionFactory) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&webv1.CreateSessionResponse{SessionId: id}), nil
}

// createProfileSession builds a session of the named profile and adds it to
// the roster. An unknown name is InvalidArgument, and it lists the names this
// server has.
func (s *HostService) createProfileSession(ctx context.Context, name string) (*connect.Response[webv1.CreateSessionResponse], error) {
	var have []string
	for _, p := range s.profiles {
		if p.Name != name {
			have = append(have, p.Name)
			continue
		}
		app, err := p.Build(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("profile %q: %w", name, err))
		}
		id, err := s.sessions.Adopt(ctx, app)
		if err != nil {
			app.Close()
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&webv1.CreateSessionResponse{SessionId: id}), nil
	}
	return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no session profile %q on this server (have chat%s)", name, joinNames(have)))
}

func joinNames(names []string) string {
	s := ""
	for _, n := range names {
		s += ", " + n
	}
	return s
}

// ListWebSessions returns the session_ids of every conversation the server
// hosts. With a store configured, it returns the durable roster merged with
// any live-only ids. The durable roster holds every persisted run id, so a
// restart still lists past sessions. Without a store, it returns the live
// in-memory roster. Both include the default. ListSessions is different,
// since it lists the persisted runs inside one App.
func (s *HostService) ListWebSessions(ctx context.Context, _ *connect.Request[webv1.ListWebSessionsRequest]) (*connect.Response[webv1.ListWebSessionsResponse], error) {
	if s.private {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("sessions are private on this server; the browser keeps its own list"))
	}
	return connect.NewResponse(&webv1.ListWebSessionsResponse{SessionIds: s.sessions.List(ctx)}), nil
}

// CloseSession closes the App for session_id and drops it from the roster. An
// unknown session_id is CodeNotFound.
func (s *HostService) CloseSession(_ context.Context, req *connect.Request[webv1.CloseSessionRequest]) (*connect.Response[webv1.CloseSessionResponse], error) {
	if !s.sessions.Close(req.Msg.GetSessionId()) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %q not found", req.Msg.GetSessionId()))
	}
	return connect.NewResponse(&webv1.CloseSessionResponse{}), nil
}
