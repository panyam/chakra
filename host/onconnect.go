package host

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/panyam/mcpkit/client"
)

// serverSetup records how a server's OnConnect calls last went. gen counts
// the runs, so streams that drop together rerun the calls once rather than
// once each. A stream remembers the gen it opened under, and a later gen
// with no error means another stream already redid the setup.
type serverSetup struct {
	mu  sync.Mutex
	gen int
	err error
}

// onConnectTimeout bounds each OnConnect call.
const onConnectTimeout = 30 * time.Second

// errSetupPending reports a subscription asked for before the server's
// OnConnect calls have run.
var errSetupPending = errors.New("onConnect has not run yet")

// rerunSetup runs the server's OnConnect calls unless a run newer than seen
// already succeeded. It returns the generation now in force. A server with
// no OnConnect calls always reports generation 0 and no error. A failed run
// emits a warning, and the error stays recorded until a later run succeeds.
func (a *App) rerunSetup(ctx context.Context, sc ServerConfig, c *client.Client, seen int) (int, error) {
	if len(sc.OnConnect) == 0 {
		return 0, nil
	}
	s := a.streams.setupFor(sc.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen > seen && s.err == nil {
		return s.gen, nil
	}
	s.err = callOnConnect(ctx, sc, c)
	s.gen++
	if s.err != nil {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("onConnect on %s: %v; its event streams stay closed", sc.ID, s.err)})
	}
	return s.gen, s.err
}

// setupDone reports the generation of the server's last OnConnect run, or
// why its streams may not open yet.
func (a *App) setupDone(sc ServerConfig) (int, error) {
	if len(sc.OnConnect) == 0 {
		return 0, nil
	}
	s := a.streams.setupFor(sc.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.gen == 0:
		return 0, errSetupPending
	case s.err != nil:
		return s.gen, fmt.Errorf("onConnect failed: %w", s.err)
	}
	return s.gen, nil
}

// callOnConnect makes the calls in order and stops at the first one that
// fails or returns a tool error.
func callOnConnect(ctx context.Context, sc ServerConfig, c *client.Client) error {
	for _, tc := range sc.OnConnect {
		args := tc.Arguments
		if args == nil {
			args = map[string]any{}
		}
		cctx, cancel := context.WithTimeout(ctx, onConnectTimeout)
		res, err := c.ToolCallFull(cctx, tc.Name, args)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", tc.Name, err)
		}
		if res != nil && res.IsError {
			return fmt.Errorf("%s: tool error: %s", tc.Name, resultText(res))
		}
	}
	return nil
}
