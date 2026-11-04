package exec

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// procStart returns pid's start time as an opaque token, and false when there
// is no such process or it has exited and not been reaped.
func procStart(pid int) (string, bool) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(k.Proc.P_pid) != pid || k.Proc.P_stat == sZomb {
		return "", false
	}
	t := k.Proc.P_starttime
	return strconv.FormatInt(int64(t.Sec), 10) + "." + strconv.FormatInt(int64(t.Usec), 10), true
}

// sZomb is SZOMB from <sys/proc.h>, a process that exited and wasn't reaped.
const sZomb = 5
