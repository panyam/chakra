package exec

import (
	"os"
	"strconv"
	"strings"
)

// procStart returns pid's start time as an opaque token, and false when there
// is no such process or it has exited and not been reaped. The token is field
// 22 of /proc/<pid>/stat, in clock ticks since boot.
func procStart(pid int) (string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// The command name, field 2, is in parentheses and may hold spaces.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", false
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3, the state, so field 22 is f[19].
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return "", false
	}
	return f[19], true
}
