//go:build unix

package exec

import (
	"io"

	"github.com/creack/pty"
)

// startTTY starts p's command on a pseudo-terminal, under the sandbox. The
// terminal makes the process a session leader, so killProcessGroup still
// reaches everything it starts.
func startTTY(p *proc, sb Sandbox, policy Policy) error {
	if err := sb.Confine(p.cmd, policy); err != nil {
		return err
	}
	f, err := pty.StartWithSize(p.cmd, &pty.Winsize{Cols: TTYCols, Rows: TTYRows})
	if err != nil {
		return err
	}
	p.stdin, p.closeIn = f, func() { f.Close() }
	p.drained = make(chan struct{})
	go func() {
		// Ends at the terminal's EOF once the process and anything it left
		// holding the terminal are gone, or when wait closes it.
		io.Copy(p.out, f)
		close(p.drained)
	}()
	return nil
}
