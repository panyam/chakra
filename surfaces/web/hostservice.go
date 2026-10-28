// Package web exposes agent/host.App over a Connect RPC bridge with a live
// event stream, so a browser can drive the same surface-agnostic host the
// terminal agentchat drives (issue 1196, epic 1193). The bridge is a thin
// projection: it holds only a SessionManager of *App and translates each RPC to
// an App method. No web or proto type leaks back into agent/host
// (agent/CONSTRAINTS A4/A6); the host stays surface-agnostic and this module
// owns all the web wiring.
//
// One server can host many concurrent conversations. Each RPC carries a
// session_id routing field that selects its App via the SessionManager; an
// empty session_id resolves to a default session created at startup, so a
// single-surface client that predates multi-session keeps working unchanged.
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
}

// SessionBuilder makes a session's App from the provider a CreateSession named.
// spec may be nil when the request named none; the builder decides whether
// that's allowed. Its error is returned to the caller as InvalidArgument, so it
// should say what to fix and never echo the key.
type SessionBuilder func(ctx context.Context, spec *webv1.ProviderSpec) (*host.App, error)

// WithSessionBuilder makes CreateSession build each App with b from the
// request's provider spec, instead of with the SessionManager's factory. This is
// how a browser brings its own key: the server needs none of its own.
func WithSessionBuilder(b SessionBuilder) Option {
	return func(s *HostService) { s.build = b }
}

// WithPrivateSessions makes a session reachable only by its id. An empty
// session_id no longer routes to a default session, and ListWebSessions is
// refused, so a stranger who reaches the server can't find or drive a session
// someone else created (and with it, their key). Session ids are 128 random
// bits, so holding one is the credential.
func WithPrivateSessions() Option {
	return func(s *HostService) { s.private = true }
}

// Option configures a HostService.
type Option func(*HostService)

// WithWatchWindow ends each Watch stream after d, and the client reconnects
// with its resume_after count. A host that buffers whole responses (App Engine
// standard) never delivers a stream that stays open, so this turns Watch into
// a long poll there. Zero, the default, keeps the stream open.
func WithWatchWindow(d time.Duration) Option {
	return func(s *HostService) { s.watchWindow = d }
}

// NewHostService wraps a single App as the default session with no factory, so
// the existing single-surface flow works unchanged (an empty session_id routes
// to app; CreateSession is unavailable without a factory). Use
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
// default session. ctx threads the store round-trips a store-backed manager
// performs when it rehydrates a session that is not cached (e.g. after a
// restart) — an unknown run is still CodeNotFound.
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

// Watch streams the selected session's event log: the retained backlog replayed
// from offset 0 (App.Subscribe), then live events, until the client disconnects.
// The derived ctx is cancelled on return so the drain goroutine and its
// Subscription exit even when Send fails before the request ctx is observed.
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
// stream on that session's Watch; this returns when the turn finishes. A failed
// turn maps to a Connect error (the same failure is also announced as a
// HostTurnFailed frame on Watch).
func (s *HostService) Submit(ctx context.Context, req *connect.Request[webv1.SubmitRequest]) (*connect.Response[webv1.SubmitResponse], error) {
	app, err := s.appFor(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := app.RunTurn(ctx, req.Msg.GetInput()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&webv1.SubmitResponse{}), nil
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

// RespondToAsk answers a pending elicitation on the selected session by the
// AskID a client read off a Frame. First responder wins; an unknown or
// already-answered ask is CodeFailedPrecondition (app state, not a transport
// failure). An empty By defaults to "web" so the resolved frame names the surface.
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
	// ask_id carries the event-log offset the ask was announced at (E2's
	// offset-based RespondToAsk); cast int64→int at the boundary. Resolution
	// rides the log barrier, so a stale or already-answered offset returns the
	// log's ErrAlreadyResolved / ErrOffsetOutOfRange (app state, reported as a
	// failed precondition, not a transport failure).
	if err := app.RespondToAsk(int(req.Msg.GetAskId()), result, by); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&webv1.RespondToAskResponse{}), nil
}

// ListSessions returns one page of the selected session's persisted RunStore
// runs. No RunStore configured is CodeFailedPrecondition (persistence is off, an
// app-state fact). This pages the runs inside one conversation; the roster of
// concurrent conversations is ListWebSessions.
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

// GetStatus returns the selected session's active model label and run id — the
// status-line read.
func (s *HostService) GetStatus(ctx context.Context, req *connect.Request[webv1.GetStatusRequest]) (*connect.Response[webv1.GetStatusResponse], error) {
	app, err := s.appFor(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&webv1.GetStatusResponse{
		ModelLabel: app.ModelLabel(),
		RunId:      app.RunID(),
	}), nil
}

// CreateSession mints a fresh conversation (a new App built from the server's
// config) and returns its session_id. A server built without a factory (the
// single-App case) returns CodeFailedPrecondition; a factory error is
// CodeInternal.
func (s *HostService) CreateSession(ctx context.Context, req *connect.Request[webv1.CreateSessionRequest]) (*connect.Response[webv1.CreateSessionResponse], error) {
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

// ListWebSessions returns the session_ids of every conversation the server
// hosts. With a store configured this is the durable roster (every persisted
// run id, so a restart still lists past sessions), merged with any live-only
// ids; otherwise it is the live in-memory roster. The default is included. This
// is the roster of conversations, distinct from ListSessions (persisted runs
// inside one App).
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
