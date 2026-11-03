//go:build !darwin

package exec

// defaultSandbox reports that this platform has no backend yet.
//
// Linux is the gap that matters, and the gap is deliberate. Delegating to
// bubblewrap is the obvious shape, but we could not verify it here. CI runs on
// ubuntu-latest. There the AppArmor restriction on unprivileged user
// namespaces can block bwrap outright, so the backend would have shipped less
// proven than the darwin one. A deployed agent on Linux is also the case most
// likely to be inside a container already. Unconfined exists for that case.
// The Linux backend is #32.
//
// Returning nil makes construction fail with an error naming the choices,
// rather than running a command with no confinement at all.
func defaultSandbox() Sandbox { return nil }
