//go:build unix

package exec

import (
	"os"
	osexec "os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group.
//
// Without it a timeout kills only the process we started. A command is
// normally a launcher such as make, go or npm, and its children do the work.
// Those children survive the parent's death. They keep the workspace busy and
// keep writing to unread pipes. Killing the group reaches them too.
func setProcessGroup(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the whole group, falling back to the single process
// when the group is not there to kill (the command exited between the timeout
// firing and this call).
func killProcessGroup(cmd *osexec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}

// setDetached starts the command in a new session, so it has no controlling
// terminal and is not in agentchat's process group. A detached process keeps
// running after agentchat exits. killProcessGroup still reaches it while the
// session lasts, since a session leader also leads its own group.
func setDetached(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// killGroup kills the group pid leads, or pid alone when there is no group.
// It is for a process this session didn't start, so it has no *exec.Cmd.
func killGroup(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// lockDir takes an exclusive lock on dir, so two sessions starting together
// don't both adopt the same entry.
func lockDir(dir string) (func(), error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
