package main

// expectedFailures lists the checks known to fail today, by check id and wire,
// with the ticket that fixes each. A run that differs from this list in either
// direction fails, so a fix that makes a check pass has to remove its line
// here, and that diff is how the mission records progress.
var expectedFailures = map[outcomeKey]string{}

type outcomeKey struct{ check, wire string }
