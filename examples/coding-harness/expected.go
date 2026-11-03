package main

// expectedFailures lists the checks known to fail on the scripted run, each
// with the ticket that fixes it. A run that differs from this list in either
// direction fails, so a ticket that makes a check pass removes its line here,
// and that diff is how mission #162 records progress. The mission is done when
// this map is empty.
//
// Live runs are not held to this list; they print the same table to compare.
var expectedFailures = map[string]string{
	"plan_published":  "#148",
	"review_findings": "#152",
}
