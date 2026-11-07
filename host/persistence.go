package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/chakra"
)

// WithRunStore enables session persistence. Each completed turn appends its
// messages and its event stream to a run in store, so a surface can resume
// or fork the session later. With a durable store, that works across process
// restarts. Nil, the default, keeps the pre-persistence behavior, and history
// lives only in memory.
func WithRunStore(store agent.RunStore) AppOption {
	return func(o *appOptions) { o.store = store }
}

// WithToolResultStore supplies the backing store for tool-result
// offloading (Config.Offload). Without it, offloading uses an in-memory
// store. Pass a durable one (store/redis, store/gorm) so offloaded blobs
// survive restarts. Ignored when Config.Offload is nil.
func WithToolResultStore(store agent.ToolResultStore) AppOption {
	return func(o *appOptions) { o.toolResultStore = store }
}

// WithConnectionTypes builds the connections of the Types in t with t's
// builders, and every other connection as the host would. Unlike
// WithProviderBuilder, it keeps the host's own handling for the rest, such
// as the Responses provider's full-history warning. Ignored when Connections
// is nil, WithProvider is set, or WithProviderBuilder is set.
func WithConnectionTypes(t ConnectionTypes) AppOption {
	return func(o *appOptions) { o.connectionTypes = t }
}

// WithProviderBuilder overrides how Config.Connections entries are turned
// into providers. Tests inject a builder returning StubProviders;
// production uses DefaultProviderBuilder (OpenAI-compatible). Ignored when
// Connections is nil or WithProvider is set.
func WithProviderBuilder(b ProviderBuilder) AppOption {
	return func(o *appOptions) { o.providerBuilder = b }
}

// RunID returns the active persisted run, or "" when persistence is off
// or no run has been attached or created yet (the first turn creates
// one lazily).
func (a *App) RunID() string {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	return a.session.runID
}

// AttachRun binds the session to runID, resuming the run or creating it. An
// existing run's history is loaded and threaded (resume), and an absent one
// is created empty. Surfaces that name sessions call this at startup
// ("continue yesterday's run-x or start it fresh"). For a must-exist switch
// mid-session, use Resume.
func (a *App) AttachRun(ctx context.Context, runID string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.session.store == nil {
		return fmt.Errorf("host: no RunStore configured")
	}
	resp, err := a.session.store.CreateRun(ctx, agent.CreateRunRequest{RunID: runID})
	if err != nil {
		return fmt.Errorf("host: attaching run %q: %w", runID, err)
	}
	if resp.Created {
		a.session.setRunID(resp.RunID)
		a.history = nil
		return nil
	}
	return a.resumeLocked(ctx, runID)
}

// Sessions lists the first page of persisted runs newest-first (the
// /sessions picker), or an error when no RunStore is configured. It is the
// first-page shim over SessionsPage; a surface that pages calls SessionsPage.
func (a *App) Sessions(ctx context.Context) ([]agent.RunInfo, error) {
	page, err := a.SessionsPage(ctx, "")
	if err != nil {
		return nil, err
	}
	return page.Runs, nil
}

// SessionPage is one page of the session picker. Runs holds the runs on this
// page, and HasMore reports whether another page exists. The App remembers
// the underlying cursor. A caller asks for the first page with
// SessionsPage(""), and then calls PageMore for each later page. PageMore
// uses the remembered cursor, the /sessions "more" flow.
type SessionPage struct {
	Runs    []agent.RunInfo
	HasMore bool
}

// SessionsPage returns one page of runs newest-first. The gorm and in-memory
// stores order by recency for free, and redis SCAN is unordered (see the
// runstore docs). An empty cursor starts from the most recent; any other value
// continues a prior page. The response's next cursor is remembered on the App
// so PageMore advances without the surface handling the opaque cursor. Errors
// when no RunStore is configured.
func (a *App) SessionsPage(ctx context.Context, cursor string) (SessionPage, error) {
	if a.session.store == nil {
		return SessionPage{}, fmt.Errorf("host: no RunStore configured")
	}
	resp, err := a.session.store.ListRuns(ctx, agent.ListRunsRequest{Cursor: cursor})
	if err != nil {
		return SessionPage{}, fmt.Errorf("host: listing sessions: %w", err)
	}
	a.session.rememberCursor(resp.NextCursor)
	return SessionPage{Runs: resp.Runs, HasMore: resp.NextCursor != ""}, nil
}

// PageMore returns the next page after the last SessionsPage call (the
// /sessions "more" flow), or an empty page when there is nothing more.
func (a *App) PageMore(ctx context.Context) (SessionPage, error) {
	cursor := a.session.nextCursor()
	if cursor == "" {
		return SessionPage{}, nil
	}
	return a.SessionsPage(ctx, cursor)
}

// maxSessionSearchScan bounds SearchSessions. Id-substring search has no
// store-side filter, so the host walks pages. The cap keeps a huge run set
// from turning a search into an unbounded scan. Content search is a future
// index.
const maxSessionSearchScan = 1000

// SearchSessions returns runs whose ID contains query (case-insensitive),
// newest-first, walking pages up to maxSessionSearchScan runs. The scan is
// bounded, so a match past the cap is not found. The limit is deliberate
// until a real search index exists.
func (a *App) SearchSessions(ctx context.Context, query string) ([]agent.RunInfo, error) {
	if a.session.store == nil {
		return nil, fmt.Errorf("host: no RunStore configured")
	}
	q := strings.ToLower(query)
	var out []agent.RunInfo
	cursor := ""
	scanned := 0
	for scanned < maxSessionSearchScan {
		resp, err := a.session.store.ListRuns(ctx, agent.ListRunsRequest{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("host: searching sessions: %w", err)
		}
		for _, r := range resp.Runs {
			scanned++
			if strings.Contains(strings.ToLower(r.ID), q) {
				out = append(out, r)
			}
		}
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	return out, nil
}

// Resume switches the session to an existing run. Its persisted messages
// replace the in-memory history, and subsequent turns append to it. An
// unknown runID is an error and changes nothing, so a typo cannot silently
// start an empty session. Use AttachRun for create-or-resume.
func (a *App) Resume(ctx context.Context, runID string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.session.store == nil {
		return fmt.Errorf("host: no RunStore configured")
	}
	return a.resumeLocked(ctx, runID)
}

func (a *App) resumeLocked(ctx context.Context, runID string) error {
	resp, err := a.session.store.LoadRun(ctx, agent.LoadRunRequest{RunID: runID})
	if err != nil {
		return fmt.Errorf("host: loading run %q: %w", runID, err)
	}
	if !resp.Found {
		return fmt.Errorf("host: run %q not found", runID)
	}
	a.history = resp.Run.Messages
	a.session.setRunID(runID)
	return nil
}

// Fork copies the current run's log into a new run and switches the session
// to the copy. An empty newRunID asks the store to generate the ID. The copy
// diverges from the next turn on, and the original is left untouched.
//
// A positive atMessage forks from an earlier point (checkpoint/rewind). Only
// the first atMessage messages carry over, and the in-memory history rewinds
// to match by reloading the fork. A zero or negative atMessage forks the
// whole log. History is already the shared prefix then, so no reload is
// needed.
func (a *App) Fork(ctx context.Context, newRunID string, atMessage int) (string, error) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.session.store == nil {
		return "", fmt.Errorf("host: no RunStore configured")
	}
	if a.session.runID == "" {
		return "", fmt.Errorf("host: no active run to fork (run at least one turn first)")
	}
	resp, err := a.session.store.ForkRun(ctx, agent.ForkRunRequest{RunID: a.session.runID, NewRunID: newRunID, AtMessage: atMessage})
	if err != nil {
		return "", fmt.Errorf("host: forking run %q: %w", a.session.runID, err)
	}
	if !resp.Found {
		return "", fmt.Errorf("host: run %q not found", a.session.runID)
	}
	if !resp.Created {
		return "", fmt.Errorf("host: run %q already exists", newRunID)
	}
	if atMessage > 0 {
		return resp.RunID, a.resumeLocked(ctx, resp.RunID)
	}
	a.session.setRunID(resp.RunID)
	return resp.RunID, nil
}

// ensureRunLocked lazily creates the session's run before the first
// persisted turn. Caller holds turnMu.
func (a *App) ensureRunLocked(ctx context.Context) error {
	if a.session.runID != "" {
		return nil
	}
	resp, err := a.session.store.CreateRun(ctx, agent.CreateRunRequest{})
	if err != nil {
		return fmt.Errorf("host: creating run: %w", err)
	}
	a.session.setRunID(resp.RunID)
	a.emit(HostEvent{Kind: HostSessionChanged, RunID: resp.RunID})
	return nil
}

// persistTurnLocked appends the completed turn (user message + appended
// messages) and flushes the teed event stream. A persistence failure renders
// as a warning, not a turn failure. The turn already succeeded and the
// in-memory session is still usable. Only durability degraded.
// Caller holds turnMu.
func (a *App) persistTurnLocked(ctx context.Context, msgs []agent.Message, pe *PersistingEmit) {
	resp, err := a.session.store.AppendMessages(ctx, agent.AppendMessagesRequest{RunID: a.session.runID, Messages: msgs})
	if err != nil {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: err.Error()})
	} else if !resp.Found {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("run %q disappeared from the store", a.session.runID)})
	}
	a.flushEventsLocked(ctx, pe)
}

// persistFailedTurnLocked keeps the record of a turn that failed: its tool
// calls, any writes they made, and the error, in the event log. It appends no
// messages, because RunTurn rolled the history back and the message log has to
// match it. Without this a failed turn left no trace in the store, so a restart
// lost the only record of edits the turn had made (issue 71). The flush runs
// detached from ctx. Imagine the user cancels a turn. It fails with ctx
// already done, and that turn is exactly the one worth keeping. A
// step-limited turn doesn't take this path. It keeps its messages and goes
// through persistTurnLocked instead (issue 109). Caller holds turnMu.
func (a *App) persistFailedTurnLocked(ctx context.Context, pe *PersistingEmit) {
	a.flushEventsLocked(context.WithoutCancel(ctx), pe)
}

// flushEventsLocked writes the turn's buffered events to the RunStore and
// advances the persisted offset. A failure renders a warning. Caller holds
// turnMu.
func (a *App) flushEventsLocked(ctx context.Context, pe *PersistingEmit) {
	if err := pe.Flush(ctx); err != nil {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: err.Error()})
		return
	}
	// The turn's events are now in the RunStore, so mark the event log as
	// persisted through its current length. The store now covers everything
	// appended up to here, including this turn's runner events. Subscribe
	// drains the Queue only from this offset forward. Advancing only on a
	// successful Flush keeps unpersisted events in the Queue-tail half of the
	// replay, so the replay has no gap. eventLog.Len() is in the absolute
	// offset space, so this is still correct under a bounded (evicting) log.
	a.events.persisted.Store(int64(a.events.log.Len()))
}

// PersistingEmit tees a Runner event stream into a RunStore event log. Use its
// Emit as the emit argument to Runner.Run (wrapping the surface's own handler),
// then Flush once the turn completes. Events buffer in memory and reach the
// store in one AppendEvents call per turn, rather than one store round-trip per
// text delta, so durability is per turn. Not safe for concurrent use, which
// matches the Runner's guarantee that emit is never called concurrently.
type PersistingEmit struct {
	store agent.RunStore
	runID string
	next  func(agent.Event)
	buf   []agent.Event
}

// NewPersistingEmit wraps next (nil is allowed) for the given run.
func NewPersistingEmit(store agent.RunStore, runID string, next func(agent.Event)) *PersistingEmit {
	if next == nil {
		next = func(agent.Event) {}
	}
	return &PersistingEmit{store: store, runID: runID, next: next}
}

// Emit forwards the event to the wrapped handler verbatim and buffers a
// persistence copy. The two differ only for turn-end: the buffered copy
// drops TurnResult.Messages (see redactForPersist). Live consumers such as
// renderers still see the whole event, and the event log doesn't duplicate
// what the message log already holds.
func (p *PersistingEmit) Emit(e agent.Event) {
	p.buf = append(p.buf, redactForPersist(e))
	p.next(e)
}

// redactForPersist strips TurnResult.Messages from a turn-end event
// before it goes into the persisted event log. The RunStore message log
// is the authoritative copy of those messages. Re-storing them inside
// the turn-end event doubles the payload (and, with large tool results,
// the offloaded stubs). The rest of the TurnResult (Text, Usage, Steps,
// FinishReason, Structured) is kept for replay and audit. Non turn-end
// events pass through unchanged. The original event and its TurnResult
// are never mutated. The copy is shallow with Messages nilled, so the
// live handler's view is untouched.
func redactForPersist(e agent.Event) agent.Event {
	if e.Kind != agent.EventTurnEnd || e.Result == nil {
		return e
	}
	trimmed := *e.Result
	trimmed.Messages = nil
	e.Result = &trimmed
	return e
}

// Flush appends the buffered events to the run and clears the buffer.
// If the store doesn't know the run, Flush returns an error, where the raw
// AppendEvents only reports Found=false. By construction, the caller created
// the run before the turn began, so a missing run is an error here.
func (p *PersistingEmit) Flush(ctx context.Context) error {
	if len(p.buf) == 0 {
		return nil
	}
	resp, err := p.store.AppendEvents(ctx, agent.AppendEventsRequest{RunID: p.runID, Events: p.buf})
	if err != nil {
		return err
	}
	if !resp.Found {
		return fmt.Errorf("host: run %q disappeared from the store", p.runID)
	}
	p.buf = nil
	return nil
}
