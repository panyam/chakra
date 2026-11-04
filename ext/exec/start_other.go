//go:build !darwin && !linux

package exec

// procStart reports no process, so a later session drops every entry from
// the registry here rather than adopt a pid it can't check.
func procStart(int) (string, bool) { return "", false }
