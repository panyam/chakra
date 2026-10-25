package main

// expectedFailures lists the checks known to fail today, by check id and wire,
// with the ticket that fixes each. A run that differs from this list in either
// direction fails, so a fix that makes a check pass has to remove its line
// here, and that diff is how the mission records progress.
var expectedFailures = map[outcomeKey]string{
	{"no-prefetch", "legacy"}:       "#50",
	{"no-prefetch", "stateless"}:    "#50",
	{"skill-rotation", "legacy"}:    "#50",
	{"skill-rotation", "stateless"}: "#50",
	{"event-hint", "legacy"}:        "#81",
	{"event-hint", "stateless"}:     "#81",
}

type outcomeKey struct{ check, wire string }
