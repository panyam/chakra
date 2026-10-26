package host

import (
	"os"
	"testing"
)

// skipOnStateless skips a test of behaviour only the session wire has, when
// the suite runs with MCPKIT_CLIENT_MODE=stateless. why names the behaviour, so
// a skip reads as a statement about the wire rather than a test that broke.
func skipOnStateless(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("MCPKIT_CLIENT_MODE") == "stateless" {
		t.Skip("session wire only: " + why)
	}
}
