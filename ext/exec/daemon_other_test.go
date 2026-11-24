//go:build !unix

package exec

// startDaemon has no portable new-session call off unix, and the tests that
// use it are unix-only.
func startDaemon(string) int { return -1 }
