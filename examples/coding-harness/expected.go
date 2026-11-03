package main

// expectedFailures lists the checks known to fail on the scripted run, each
// with the ticket that fixes it. If a run differs from this list in either
// direction, the exercise fails. So when a ticket makes a check pass, it also
// removes the check's line here. Mission #162 records progress through that
// diff, and the mission is done when this map is empty.
//
// Live runs are not held to this list; they print the same table to compare.
var expectedFailures = map[string]string{}
