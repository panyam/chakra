package exec

import (
	"fmt"
	osexec "os/exec"
	"runtime"
)

// Sandbox confines a command before it runs.
//
// The seam is here rather than around a ToolSource because a ToolSource
// wrapper has nothing to confine. Most tools in this tree are in-process Go
// functions or MCP calls with no subprocess in them. Isolating those would
// mean isolating the agent process. That is a deployment decision rather than
// a library one. Only the code that spawns a process can confine it.
//
// A backend is not a guarantee. It makes it harder for a command to do
// something the operator did not intend. The approval gate asks whether the
// command should run at all.
type Sandbox interface {
	// Name identifies the backend in errors and in the approval prompt, so a
	// user approving a command can see what is confining it.
	Name() string

	// Available reports whether this backend can run here, and is checked at
	// construction so a misconfiguration fails at startup rather than on the
	// first command an hour into a session.
	Available() error

	// Confine rewrites cmd to run under the policy. It is called after cmd is
	// fully built and before it starts.
	Confine(cmd *osexec.Cmd, p Policy) error
}

// Policy is what a Sandbox has to enforce for one command.
type Policy struct {
	// Write lists absolute directories where the command may write. They are
	// the workspace roots plus the caches that toolchains need.
	Write []string

	// DenyRead lists absolute paths, and the command may not read them. Reads
	// are otherwise unrestricted. See Config.DenyRead for why reads are not
	// confined the way writes are.
	DenyRead []string

	// Dir is the working directory the command runs in.
	Dir string

	// AllowNetwork permits network access. False denies it.
	AllowNetwork bool
}

// Unconfined runs commands with no confinement at all.
//
// It has to be named in Config.Sandbox and is never selected by default.
// Imagine an operator running inside a container they built. They have
// already drawn the boundary. Naming Unconfined says so in the configuration,
// so nobody has to infer it from a missing backend.
func Unconfined() Sandbox { return unconfined{} }

type unconfined struct{}

func (unconfined) Name() string                      { return "unconfined" }
func (unconfined) Available() error                  { return nil }
func (unconfined) Confine(*osexec.Cmd, Policy) error { return nil }

// resolveSandbox picks the backend for this platform, or explains why there
// is not one.
//
// It refuses on purpose. Suppose a missing backend fell back to running
// unconfined. The allowlist would then seem to have an authority it does not
// have. The operator names `make test`, and the Makefile defines what
// `make test` does. That Makefile is workspace content, and an editing tool
// can rewrite it.
func resolveSandbox(s Sandbox, fallback func() Sandbox) (Sandbox, error) {
	if s == nil {
		s = fallback()
	}
	if s == nil {
		return nil, fmt.Errorf("exec: no sandbox backend on %s; set Config.Sandbox explicitly, "+
			"or exec.Unconfined() if this process is already isolated", runtime.GOOS)
	}
	if err := s.Available(); err != nil {
		return nil, fmt.Errorf("exec: sandbox %s unavailable: %w", s.Name(), err)
	}
	return s, nil
}
