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
	f, err := pty.Start(p.cmd)
	if err != nil {
		return err
	}
	p.stdin, p.closeIn = f, func() { f.Close() }
	go io.Copy(p.out, f) // ends when the terminal closes
	return nil
}
