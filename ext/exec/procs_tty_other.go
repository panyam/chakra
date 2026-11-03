//go:build !unix

package exec

// startTTY is unavailable where there are no pseudo-terminals.
func startTTY(*proc, Sandbox, Policy) error { return errNoTTY }
