package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

// DefaultSessionID is the id of the default session. An empty session_id
// routes to it. One agentweb server always has a default session. A client
// written before multi-session support sends no session_id, and it keeps
// working unchanged.
const DefaultSessionID = "default"

// ErrNoSessionFactory is returned by SessionManager.Create when the manager was
// built without a factory. Such a manager can only serve its pre-built default
// session, which is the single-App back-compat path.
var ErrNoSessionFactory = errors.New("web: session manager has no factory; cannot create sessions")

// SessionManager owns the live host.Apps of a single agentweb server. It holds
// one App per concurrent conversation, keyed by session id. It is safe for
// concurrent use. Get selects an App by a request's session_id, and an empty
// session_id resolves to the default session. New sessions are built on
// demand from a factory, and the factory produces a fresh App from the
// server's config.
//
// When a store is configured (NewSessionManagerWithStore), the manager is a
// live-App cache over a durable RunStore roster. Each session_id is a run id,
// and Create attaches a fresh run. Imagine a process restart drops a live App.
// The next Get for that session finds nothing cached, so it resumes the
// session's run from the store and rehydrates the App. The factory must build
// Apps with the same store attached (host.WithRunStore), so a rehydrated App
// keeps persisting. With no store the manager is a plain in-memory cache, and
// nothing survives a restart, as in the original behavior.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*host.App
	factory  func(context.Context) (*host.App, error)
	// store, when non-nil, holds the durable roster, and the manager caches
	// live Apps over it. The factory attaches the SAME store to each App. So
	// Create attaches a run under the session id, and Get rehydrates a dropped
	// session by resuming that run. Nil keeps the pure in-memory behavior.
	store agent.RunStore
	// profiles builds and cleans up the sessions of each kind other than
	// chat, by profile name. A profile session's id starts with its name and
	// a dash, so the kind survives a restart without the store holding it.
	profiles map[string]Profile
}

// NewSessionManager returns a manager whose Create builds fresh Apps from
// factory. factory may be nil, in which case the manager can only serve a
// default session registered via SetDefault and Create returns
// ErrNoSessionFactory. The manager has no durable store. Sessions exist only in
// memory and do not survive a process restart. Use NewSessionManagerWithStore
// for persistence.
func NewSessionManager(factory func(context.Context) (*host.App, error)) *SessionManager {
	return &SessionManager{sessions: map[string]*host.App{}, factory: factory}
}

// NewSessionManagerWithStore returns a manager that caches live Apps over the
// durable RunStore store. Each session_id is a run id, and Create attaches a
// run under a freshly minted id. After a restart a session is not cached, so
// Get resumes its run and rehydrates it, and the session survives the
// restart. The factory must attach the SAME RunStore to every App it builds
// (via host.WithRunStore), or a rehydrated App will not persist. A nil store
// is equivalent to NewSessionManager.
func NewSessionManagerWithStore(factory func(context.Context) (*host.App, error), store agent.RunStore) *SessionManager {
	return &SessionManager{sessions: map[string]*host.App{}, factory: factory, store: store}
}

// resolve maps an empty id to the default session id, so an empty session_id on
// the wire always addresses the default session.
func resolve(id string) string {
	if id == "" {
		return DefaultSessionID
	}
	return id
}

// SetDefault registers app as the default session, and an empty session_id
// routes to it. It is called once at startup, and it does not close a prior
// default.
func (m *SessionManager) SetDefault(app *host.App) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[DefaultSessionID] = app
}

// Create builds a fresh App via the factory, stores it under a newly minted id,
// and returns the id with the App. It returns ErrNoSessionFactory when the
// manager has no factory. A factory error is propagated and nothing is stored.
//
// With a store configured, the minted id is also the run id. Create attaches a
// fresh run to the App (host.App.AttachRun), so the session is on the durable
// roster and survives a restart. An AttachRun error is propagated and nothing is
// stored. Without a store the id is just an in-memory key and no run is created.
func (m *SessionManager) Create(ctx context.Context) (string, *host.App, error) {
	if m.factory == nil {
		return "", nil, ErrNoSessionFactory
	}
	app, err := m.factory(ctx)
	if err != nil {
		return "", nil, err
	}
	id, err := m.Adopt(ctx, app)
	if err != nil {
		return "", nil, err
	}
	return id, app, nil
}

// Adopt adds an App built elsewhere under a newly minted id. A session builder
// builds that App from a request's provider spec. When the manager has a
// store, Adopt attaches a run, exactly as Create does for a factory-built App.
// On an AttachRun error the App is closed and nothing is stored.
func (m *SessionManager) Adopt(ctx context.Context, app *host.App) (string, error) {
	id := m.mintID()
	if err := m.adoptAs(ctx, id, app); err != nil {
		return "", err
	}
	return id, nil
}

// adoptAs stores app under id, attaching a run first when there is a store.
func (m *SessionManager) adoptAs(ctx context.Context, id string, app *host.App) error {
	if m.store != nil {
		// session_id == run id. Attach (create) the run so it goes on the
		// durable roster and a later Get can rehydrate it. AttachRun is
		// create-or-resume, but the minted id is unique so this always creates.
		if err := app.AttachRun(ctx, id); err != nil {
			app.Close()
			return err
		}
	}
	m.mu.Lock()
	m.sessions[id] = app
	m.mu.Unlock()
	return nil
}

// Get returns the App for id and whether it was found. An empty id resolves to
// the default session. A missing session is app state (found=false), not an
// error, so the caller maps it to a Connect CodeNotFound.
//
// With a store configured, Get rehydrates a session that is not cached, for
// example one a process restart dropped. Get builds a fresh App via the
// factory and resumes the run with this session id. If the store does not know
// the run, Get returns found=false, which is still app state and not an
// error. Rehydration makes store round-trips (LoadRun), and ctx covers them.
// The App build and resume run outside the manager lock, so a slow factory
// does not serialize concurrent session access. A lost race closes the
// redundant App.
func (m *SessionManager) Get(ctx context.Context, id string) (*host.App, bool) {
	rid := resolve(id)
	m.mu.Lock()
	app, ok := m.sessions[rid]
	m.mu.Unlock()
	if ok {
		return app, true
	}
	if m.store == nil {
		return nil, false
	}
	// The id says which kind of session to rebuild. A profile builds on the
	// folders it used before the restart, which it finds by the id.
	build := m.factory
	if name := ProfileOf(rid); name != "" {
		m.mu.Lock()
		p, ok := m.profiles[name]
		m.mu.Unlock()
		if !ok {
			return nil, false
		}
		build = func(ctx context.Context) (*host.App, error) { return p.Build(ctx, rid) }
	}
	if build == nil {
		return nil, false
	}
	app, err := build(ctx)
	if err != nil {
		return nil, false
	}
	if err := app.Resume(ctx, rid); err != nil {
		// An unknown run or a load failure counts as not found. The caller maps
		// it to a Connect CodeNotFound, exactly as for a never-created session.
		app.Close()
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// A concurrent Get may have rehydrated the same session; keep the first
	// winner and drop ours so only one live App backs a session.
	if existing, ok := m.sessions[rid]; ok {
		app.Close()
		return existing, true
	}
	m.sessions[rid] = app
	return app, true
}

// List returns the ids of every session, unordered. With a store configured it
// returns the durable roster from RunStore.ListRuns. That roster holds every
// persisted run id, so a restart still shows past sessions. List merges in any
// live-only ids that the store does not know yet. Without a store it returns
// the in-memory keys. The default session's id is included. ctx covers the
// store round-trips.
func (m *SessionManager) List(ctx context.Context) []string {
	if m.store == nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		ids := make([]string, 0, len(m.sessions))
		for id := range m.sessions {
			ids = append(ids, id)
		}
		return ids
	}
	// The durable roster is the source of truth for persisted sessions. Page
	// through every run, so List still returns them after a restart empties the
	// live map.
	seen := map[string]bool{}
	var ids []string
	cursor := ""
	for {
		resp, err := m.store.ListRuns(ctx, agent.ListRunsRequest{Cursor: cursor})
		if err != nil {
			break
		}
		for _, r := range resp.Runs {
			if !seen[r.ID] {
				seen[r.ID] = true
				ids = append(ids, r.ID)
			}
		}
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	// Merge any live-only ids that the store does not know. Such a session is
	// cached, but its run is not persisted yet. The factory attaches the store
	// today, so this set is normally empty. The merge keeps List correct if it
	// isn't.
	m.mu.Lock()
	for id := range m.sessions {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	return ids
}

// Close closes the App for id and drops it from the live cache, returning
// whether a live session was found. An empty id resolves to the default session.
// Closing is idempotent, and a second Close of the same id returns false. With
// a store configured, Close does NOT delete the run from the store. It evicts
// only the live App, so a later Get rehydrates the session from its persisted
// run.
func (m *SessionManager) Close(id string) bool {
	rid := resolve(id)
	m.mu.Lock()
	app, ok := m.sessions[rid]
	if ok {
		delete(m.sessions, rid)
	}
	m.mu.Unlock()
	if ok {
		app.Close()
		m.cleanup(rid)
	}
	return ok
}

// cleanup removes what a profile session left on disk once the session can't
// come back, which is when there is no store to resume it from.
func (m *SessionManager) cleanup(id string) {
	if m.store != nil {
		return
	}
	m.mu.Lock()
	p, ok := m.profiles[ProfileOf(id)]
	m.mu.Unlock()
	if ok && p.Close != nil {
		p.Close(id)
	}
}

// ProfileOf returns the profile a session id belongs to, or "" for chat.
func ProfileOf(id string) string {
	name, _, ok := strings.Cut(id, "-")
	if !ok {
		return ""
	}
	return name
}

// addProfile registers a kind of session the manager can create and
// rehydrate. A profile name can't hold a dash, since the name ends at the
// first dash of a session id.
func (m *SessionManager) addProfile(p Profile) error {
	if p.Name == "" || p.Name == "chat" || strings.Contains(p.Name, "-") {
		return fmt.Errorf("web: profile name %q must be non-empty, not chat, and hold no dash", p.Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.profiles == nil {
		m.profiles = map[string]Profile{}
	}
	m.profiles[p.Name] = p
	return nil
}

// CreateProfile builds a session of the named profile under a fresh id that
// starts with the name. With a store it attaches a run, as Create does, so the
// session survives a restart. On a build or AttachRun error nothing is stored
// and the profile's Close removes what the build left.
func (m *SessionManager) CreateProfile(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	p, ok := m.profiles[name]
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("web: no profile %q", name)
	}
	id := name + "-" + m.mintID()
	app, err := p.Build(ctx, id)
	if err == nil {
		err = m.adoptAs(ctx, id, app)
	}
	if err != nil {
		if p.Close != nil {
			p.Close(id)
		}
		return "", err
	}
	return id, nil
}

// CloseAll closes every live session and empties the roster. It is used to tear
// the whole server down.
func (m *SessionManager) CloseAll() {
	m.mu.Lock()
	apps := make(map[string]*host.App, len(m.sessions))
	for id, app := range m.sessions {
		apps[id] = app
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for id, app := range apps {
		app.Close()
		m.cleanup(id)
	}
}

// mintID returns a fresh, unpredictable session id (crypto/rand hex). It never
// collides with DefaultSessionID. The id has 128 bits, so in practice it never
// collides with a live session.
func (m *SessionManager) mintID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
