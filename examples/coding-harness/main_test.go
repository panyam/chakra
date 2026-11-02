package main

import (
	"context"
	"testing"
)

func TestExerciseMatchesExpected(t *testing.T) {
	run, err := exercise(context.Background(), options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range run.unexpected() {
		_, pinned := expectedFailures[id]
		t.Errorf("%s: pinned=%v but pass=%v (%s)", id, pinned, run.outcomes[id].pass, run.outcomes[id].detail)
	}
	if !run.result.testsPassed {
		t.Errorf("fixture tests still fail after the scripted run:\n%s", run.result.testOutput)
	}
}
