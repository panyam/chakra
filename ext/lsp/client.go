package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// position is an LSP text position. Character is counted in the encoding the
// server agreed to at initialize, which is utf-16 unless it said otherwise.
type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type textRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type location struct {
	URI   string    `json:"uri"`
	Range textRange `json:"range"`
}

// diagnostic is one problem the server reports about a file.
//
// It is unexported because nothing outside this package uses it. The extension
// renders diagnostics to text for the model. A caller that wants structured
// problems should ask the language server, not this wrapper around it.
type diagnostic struct {
	Range    textRange `json:"range"`
	Severity int       `json:"severity"`
	Source   string    `json:"source"`
	Message  string    `json:"message"`
}

// busyGrace is how much longer refresh waits for a correction while the server
// reports itself busy.
//
// Waiting for a busy server to fall quiet does not work. rust-analyzer reports
// progress almost continuously, so "wait while busy" ran to the full timeout
// on every write, measured at eight seconds each. So busyGrace is added to the
// settle, and the deadline does not move. The server still gets extra patience
// while it is visibly working, and the extra wait is bounded.
const busyGrace = 1500 * time.Millisecond

// severity levels, as the protocol numbers them.
const (
	severityError   = 1
	severityWarning = 2
)

// documentSymbol is one entry of the file outline.
//
// Range covers the whole declaration and SelectionRange covers just the name.
// Name-addressed navigation needs that distinction. A request aimed anywhere
// in a function body is ambiguous, while one aimed at the identifier matches
// what an editor would send with the cursor on it.
type documentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          textRange        `json:"range"`
	SelectionRange textRange        `json:"selectionRange"`
	Children       []documentSymbol `json:"children"`
}

// client drives one language server subprocess.
type client struct {
	spec  ServerSpec
	roots []string

	cmd  *exec.Cmd
	conn *conn

	// encoding is what the server agreed to for Position.Character. Empty
	// means it declined to negotiate, which per spec means utf-16. gopls,
	// typescript-language-server and pyright all decline; rust-analyzer and
	// clangd answer "utf-8".
	encoding string

	// settle is how long to keep waiting for further diagnostics after one
	// arrives, for a server that does not report progress. See refresh.
	settle time.Duration

	mu    sync.Mutex
	diags map[string][]diagnostic
	open  map[string]int

	// sent is the content hash last given to the server. answered is the value
	// sent had when the server last published diagnostics for the file. When
	// the two match, the server already agrees with us. When they differ, a
	// change is in flight and the server has not replied yet.
	sent     map[string][32]byte
	answered map[string][32]byte

	// busyTokens are the work-done progress operations the server says are
	// running, and begun counts the ones that have started. Together they
	// replace guessing. A server that reports progress tells us when it starts
	// working and when it stops. That beats any quiet period we could pick
	// for it.
	busyTokens map[string]bool
	begun      int

	waiters map[string][]chan struct{}

	closeOnce sync.Once
}

// startClient spawns the server and completes the initialize handshake.
//
// The handshake is synchronous on purpose. A server that has not answered
// initialize cannot be sent anything else, and deferring the failure would
// turn "gopls is not installed" into a diagnostics block that is silently
// always empty.
func startClient(ctx context.Context, spec ServerSpec, roots []string) (*client, error) {
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("lsp: ServerSpec.Command is required")
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("lsp: at least one root is required")
	}
	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Dir = roots[0]
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: %s stdin: %w", spec.Command[0], err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: %s stdout: %w", spec.Command[0], err)
	}
	// The server's stderr is its own diagnostics channel, not ours. If we piped
	// it and nobody read the pipe, a chatty server would fill it and block
	// mid-response, and that would look like a hang. Discarding it avoids that.
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start %s: %w", spec.Command[0], err)
	}

	c := &client{
		spec:       spec,
		roots:      roots,
		cmd:        cmd,
		conn:       newConn(stdin, stdout),
		diags:      map[string][]diagnostic{},
		open:       map[string]int{},
		sent:       map[string][32]byte{},
		answered:   map[string][32]byte{},
		busyTokens: map[string]bool{},
		waiters:    map[string][]chan struct{}{},
	}
	c.conn.onNotify = c.onNotify
	go c.conn.run()

	if err := c.initialize(ctx); err != nil {
		_ = c.close()
		return nil, err
	}
	return c, nil
}

func (c *client) initialize(ctx context.Context) error {
	folders := make([]map[string]any, 0, len(c.roots))
	for _, r := range c.roots {
		folders = append(folders, map[string]any{"uri": pathToURI(r), "name": filepath.Base(r)})
	}
	params := map[string]any{
		"processId": os.Getpid(),
		// rootUri is the first root, and workspaceFolders lists them all.
		// rootUri is singular and deprecated, but servers that predate folders
		// still read it. Every server we measured answers correctly for a file
		// in a folder other than the first.
		"rootUri":          pathToURI(c.roots[0]),
		"workspaceFolders": folders,
		"capabilities": map[string]any{
			// utf-8 first, so a server that negotiates gives us byte offsets
			// and the conversion in offsetFor becomes a no-op. gopls declines
			// and we get utf-16, so the conversion still has to exist.
			"general": map[string]any{"positionEncodings": []string{"utf-8", "utf-16"}},
			// With progress reports, refresh can wait exactly as long as the
			// server is working instead of guessing a quiet period.
			"window": map[string]any{"workDoneProgress": true},
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": true},
				"publishDiagnostics": map[string]any{},
				"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"definition":         map[string]any{},
				"references":         map[string]any{},
			},
			"workspace": map[string]any{"configuration": true, "workspaceFolders": true},
		},
	}
	var result struct {
		Capabilities struct {
			PositionEncoding string `json:"positionEncoding"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := c.conn.call(ctx, "initialize", params, &result); err != nil {
		return fmt.Errorf("lsp: initialize %s: %w", c.spec.Command[0], err)
	}
	c.encoding = result.Capabilities.PositionEncoding
	c.settle = c.spec.SettleDelay
	if c.settle <= 0 {
		c.settle = defaultSettleDelay
	}
	return c.conn.notify("initialized", map[string]any{})
}

func (c *client) onNotify(method string, params json.RawMessage) {
	if method == "$/progress" {
		c.trackProgress(params)
		return
	}
	if method != "textDocument/publishDiagnostics" {
		return
	}
	var p struct {
		URI         string       `json:"uri"`
		Diagnostics []diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	abs, ok := c.pathFromURI(p.URI)
	if !ok {
		return
	}
	c.mu.Lock()
	c.diags[abs] = p.Diagnostics
	c.answered[abs] = c.sent[abs]
	waiters := c.waiters[abs]
	delete(c.waiters, abs)
	c.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// trackProgress follows the server's work-done progress notifications.
//
// Only begin and end matter. A server that reports progress tells us when a
// recompute starts and stops, and a timer can only guess at that.
// rust-analyzer brackets its cargo check this way, so waiting for the end
// takes exactly as long as the check and no longer.
func (c *client) trackProgress(params json.RawMessage) {
	var p struct {
		Token any `json:"token"`
		Value struct {
			Kind string `json:"kind"`
		} `json:"value"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	token := fmt.Sprint(p.Token)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch p.Value.Kind {
	case "begin":
		c.busyTokens[token] = true
		c.begun++
	case "end":
		delete(c.busyTokens, token)
	}
}

// busy reports whether the server says it is currently working.
func (c *client) busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.busyTokens) > 0
}

// awaitPublish registers interest in the next diagnostics for abs.
//
// Call it before sending the didChange that causes them. The server can
// publish before the notify call returns, and a waiter installed after that
// would miss the publication it is waiting for. A publication closes each
// channel it hands out, or dropWait removes it. A channel that gets neither
// remains in the map until the next publication for that file.
func (c *client) awaitPublish(abs string) chan struct{} {
	ch := make(chan struct{})
	c.mu.Lock()
	c.waiters[abs] = append(c.waiters[abs], ch)
	c.mu.Unlock()
	return ch
}

// dropWait removes a waiter that gave up, so a run of timeouts cannot grow the
// waiter list without bound.
func (c *client) dropWait(abs string, ch chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.waiters[abs]
	for i, w := range list {
		if w == ch {
			c.waiters[abs] = append(list[:i:i], list[i+1:]...)
			return
		}
	}
}

// sync tells the server about the current on-disk content of abs, opening the
// document the first time and revising it afterwards. It reports whether
// anything was actually sent.
//
// sync does not resend content the server already has. That saves a needless
// recompute on every navigation call, and refresh depends on it. clangd does
// not re-publish for a didChange with identical content, so a resend would
// leave a caller waiting for a publication that never comes.
func (c *client) sync(abs string) (bool, error) {
	body, err := os.ReadFile(abs)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(body)

	c.mu.Lock()
	version, isOpen := c.open[abs]
	if isOpen && c.sent[abs] == sum {
		c.mu.Unlock()
		return false, nil
	}
	version++
	c.open[abs] = version
	c.sent[abs] = sum
	c.mu.Unlock()

	uri := pathToURI(abs)
	if !isOpen {
		if err := c.conn.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": c.spec.LanguageID,
				"version":    version,
				"text":       string(body),
			},
		}); err != nil {
			return true, err
		}
		return true, c.save(uri, string(body))
	}
	// A single change with no range means "this is the whole document". Every
	// server accepts that form, whatever sync kind it declared. We drive
	// several servers and can't keep an incremental-diff implementation for
	// each one.
	if err := c.conn.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{"text": string(body)}},
	}); err != nil {
		return true, err
	}
	return true, c.save(uri, string(body))
}

// save tells the server the file is on disk. For some servers, only a save
// triggers a real re-check.
//
// We measured that rust-analyzer publishes NOTHING for a didChange. Its cargo
// check runs on save, and on save it answers in about 0.2s. Without this, the
// second and every later edit to a Rust file was never re-checked. The first
// edit only worked because opening the document happened to trigger a check.
//
// It is also honest rather than a trick. Everything here reads the file from
// disk, so by the time we describe it to the server it genuinely has been
// saved. Servers that do not care ignore the notification.
func (c *client) save(uri, text string) error {
	return c.conn.notify("textDocument/didSave", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"text":         text,
	})
}

// refresh syncs abs and waits for the diagnostics that follow, up to timeout.
//
// # Why it waits for quiet rather than for one publication
//
// The obvious implementation takes the first publication after the change as
// the answer. That works for gopls, typescript-language-server and pyright.
// Each of them publishes once with what it found. It fails for a server that
// computes in two phases.
//
// rust-analyzer publishes an EMPTY set as soon as the file changes and the
// real diagnostics about two seconds later, once cargo has run. Taking the
// first publication there reports a file that does not compile as "no problems
// reported". That is the worst way for this to be wrong. The post-write report
// exists to tell the model it broke something.
//
// So each publication restarts a settle timer and the last set wins. The
// timeout still bounds the whole wait, so a server that publishes
// continuously cannot hold a turn open.
//
// A timeout with nothing published returns false rather than the diagnostics
// we happen to be holding. Imagine we presented stale problems as the result
// of the edit that just ran. The model would go fix an error it had already
// fixed. Saying nothing is better.
func (c *client) refresh(ctx context.Context, abs string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	first := c.awaitPublish(abs)

	changed, err := c.sync(abs)
	if err != nil {
		c.dropWait(abs, first)
		return false
	}
	if !changed && c.hasAnswered(abs) {
		// The server has this exact content and has already reported on it,
		// so nothing further is coming and what we hold is current. Waiting
		// would report a timeout for a file we can already answer about.
		// That happens with clangd when it is handed content it already has.
		//
		// Both halves of the check are needed. Unchanged content alone is not
		// enough. Right after a didOpen the server has the content but has
		// not replied yet. Returning here would report the file as clean
		// before anything had looked at it.
		c.dropWait(abs, first)
		return true
	}

	if !c.waitPublish(ctx, abs, first, time.Until(deadline)) {
		return false
	}
	// quiet is the time we stop waiting for a correction. Each publication
	// moves it later. While the server reports itself busy it moves later
	// still, so a slow recheck is not cut off and reported as clean.
	quiet := time.Now().Add(c.settle)
	for {
		now := time.Now()
		switch {
		case ctx.Err() != nil, now.After(deadline):
			return true
		case len(c.diagnostics(abs)) > 0:
			// Something real to report beats waiting for more of it. A later
			// publication can only add problems, and the next turn's context
			// stage shows the full current set anyway. Holding the model up
			// for completeness would give it nothing new.
			return true
		}
		if c.busy() {
			quiet = now.Add(c.settle + busyGrace)
		}
		if now.After(quiet) {
			return true
		}
		// Each wait ends at the quiet deadline instead of sleeping in one go,
		// so busy() is re-read on every pass. A server publishes its
		// provisional empty set first and only then announces the work. A
		// single sample taken right after the first publication would miss it.
		wait := min(quiet.Sub(now), deadline.Sub(now))
		next := c.awaitPublish(abs)
		if c.waitPublish(ctx, abs, next, wait) {
			quiet = time.Now().Add(c.settle)
		}
	}
}

// hasAnswered reports whether the diagnostics we hold describe the content the
// server currently has.
func (c *client) hasAnswered(abs string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	sent, open := c.sent[abs]
	if !open {
		return false
	}
	answered, ok := c.answered[abs]
	return ok && answered == sent
}

// waitPublish blocks until ch fires, the wait elapses, or ctx ends, dropping
// the waiter in the two cases where nothing arrived.
func (c *client) waitPublish(ctx context.Context, abs string, ch chan struct{}, wait time.Duration) bool {
	if wait <= 0 {
		c.dropWait(abs, ch)
		return false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		c.dropWait(abs, ch)
		return false
	case <-ctx.Done():
		c.dropWait(abs, ch)
		return false
	}
}

func (c *client) diagnostics(abs string) []diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]diagnostic, len(c.diags[abs]))
	copy(out, c.diags[abs])
	return out
}

// tracked reports the files this client has been told about. Those are the
// files worth reporting diagnostics for.
func (c *client) tracked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.open))
	for abs := range c.open {
		out = append(out, abs)
	}
	return out
}

// closeDocument tells the server abs is no longer open and forgets what was
// sent for it, so a later sync opens it afresh.
func (c *client) closeDocument(abs string) {
	c.mu.Lock()
	delete(c.open, abs)
	delete(c.sent, abs)
	delete(c.answered, abs)
	c.mu.Unlock()
	_ = c.conn.notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(abs)},
	})
}

// close shuts the server down, politely first and then not.
//
// The graceful path matters more than it looks. gopls writes its module cache
// and telemetry on exit, and a killed server can leave a stale lock that makes
// the next session slower. The kill is the backstop for a server that ignores
// shutdown. A host that blocks on Close is worse than an orphan.
func (c *client) close() error {
	var err error
	c.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if callErr := c.conn.call(ctx, "shutdown", nil, nil); callErr == nil {
			_ = c.conn.notify("exit", nil)
		}
		_ = c.conn.w.Close()

		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
			<-done
			err = fmt.Errorf("lsp: %s did not exit, killed", c.spec.Command[0])
		}
	})
	return err
}

// pathToURI renders a filesystem path as a file:// URI.
//
// It uses url.URL, not string concatenation. A path can contain spaces and
// other characters that need escaping, and a server answers a malformed URI
// with an empty result, not an error.
func pathToURI(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String()
}

// pathFromURI converts a server-supplied URI to an absolute path, reporting
// false for anything outside every workspace root.
//
// Out-of-root results are normal. A definition in the standard library or a
// module cache is a real answer to a real question. It just isn't a file this
// workspace can talk about.
func (c *client) pathFromURI(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	abs := filepath.Clean(filepath.FromSlash(u.Path))
	for _, root := range c.roots {
		rel, relErr := filepath.Rel(root, abs)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return abs, true
	}
	return "", false
}
