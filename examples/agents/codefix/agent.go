package codefix

import "github.com/panyam/chakra/examples/agents/common"

// Agent is codefix as the exercise and the walk run it.
func Agent() common.Agent {
	return common.Agent{
		Name:         "codefix",
		Ladder:       Ladder,
		Instructions: Instructions,
		Scripted:     Scripted,
		MaxSteps:     10,
		About: []string{
			"codefix fixes bugs in a small Go module, example.com/shopkit: money, cart, discount and report packages, each with tests.",
			"Each case copies the module, brings in one bug and hands the agent a bug report. It passes when go test ./... passes and no _test.go file changed.",
			"The bugs: an off-by-one, a comparison that leaves out its threshold, a missing nil check that panics, a bug one package away from where it shows, and a report with two bugs in two files.",
		},
		StepNotes: map[string]string{
			"read + edit": "ext/files: read_file, edit_file and friends, rooted at the case's copy. The model can't run anything.",
			"+ run tests": "ext/exec offers run_tests, which runs go test ./... and shows the output: which test failed, and where a panic happened.",
			"+ repo map":  "A retrieval stage puts a map of the module before the model's first call: each file, what it declares, and what it calls in other packages.",
			"+ plan slot": "A plan slot the model writes with write_slot. Its text is shown on every call, so a list of changes outlives the steps that made them.",
			"+ test gate": "A turn-end hook runs the tests when the model says it's done. While they fail, it sends the model back with the output.",
		},
	}
}
