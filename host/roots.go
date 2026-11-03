package host

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// WithRoots adds workspace directories to the ones in Config.Roots. Surfaces
// confine ext/files and ext/exec to their roots, and they pass the same roots
// here. A server asking roots/list then sees the project the agent is working
// on.
func WithRoots(dirs ...string) AppOption {
	return func(o *appOptions) { o.roots = append(o.roots, dirs...) }
}

// rootSet is the host's answer to roots/list. The handler reads it on every
// request, so SetRoots changes what the next roots/list returns without
// reconnecting.
type rootSet struct {
	mu    sync.RWMutex
	roots []core.Root
}

func (r *rootSet) list(context.Context) ([]core.Root, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]core.Root{}, r.roots...), nil
}

func (r *rootSet) set(roots []core.Root) {
	r.mu.Lock()
	r.roots = roots
	r.mu.Unlock()
}

// resolveRoots turns directories into file:// roots: absolute, cleaned and
// deduplicated, each named after its last path element.
func resolveRoots(dirs []string) ([]core.Root, error) {
	var out []core.Root
	seen := map[string]bool{}
	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, fmt.Errorf("host: root %q: %w", d, err)
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
		out = append(out, core.Root{URI: u.String(), Name: filepath.Base(abs)})
	}
	return out, nil
}

// Roots returns the roots the host currently declares to MCP servers, as
// roots/list answers them. Nil when the host declares none.
func (a *App) Roots() []core.Root {
	if a.roots == nil {
		return nil
	}
	r, _ := a.roots.list(context.Background())
	return r
}

// SetRoots replaces the declared roots and sends
// notifications/roots/list_changed to every connected server, which then asks
// roots/list again. It needs the host to have started with at least one root.
// The roots capability is fixed at initialize. A host that advertised none
// cannot start declaring them mid-session, so SetRoots returns an error.
//
// A server that is not connected misses the notification, and reads the new
// list when it next asks. Every server on the stateless wire behaves the same
// way. That wire has no list_changed notification (SEP-2575), so its servers
// ask roots/list inside each tool call that needs it. The returned error
// joins the notify failures from the connected servers. The new roots are in
// place either way.
func (a *App) SetRoots(ctx context.Context, dirs ...string) error {
	if a.roots == nil {
		return errors.New("host: SetRoots needs the host to start with roots (Config.Roots or WithRoots)")
	}
	roots, err := resolveRoots(dirs)
	if err != nil {
		return err
	}
	a.roots.set(roots)
	var errs []error
	for _, m := range a.servers.group.Status() {
		c := a.clientByID(m.ID)
		if m.State != client.StateReady || c == nil || c.UsingStatelessWire() {
			continue
		}
		if err := c.NotifyRootsChanged(ctx); err != nil {
			errs = append(errs, fmt.Errorf("server %s: %w", m.ID, err))
		}
	}
	return errors.Join(errs...)
}
