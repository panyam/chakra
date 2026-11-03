package eval

import (
	"fmt"
	"os"

	"github.com/panyam/chakra"
)

// Adapter turns an external benchmark into cases this harness can run.
//
// The suites vary and the harness doesn't. LongMemEval, LoCoMo, BFCL,
// tau-bench and our own fixtures differ in where the tasks come from and how
// they are graded. They don't differ in how a turn is executed or how a report
// is tallied. So an adapter is a data source, and adding one must not touch
// the Runner, the scorers, or Suite.
//
// # Conventions an adapter follows
//
//   - **Data lives outside the repo.** Benchmark corpora are large and
//     separately licensed, so an adapter reads a path from the environment and
//     skips cleanly when it is unset. mcpkit's conformance/ follows the same
//     convention with its MCPCONFORMANCE_*_PATH variables. Nothing is vendored.
//   - **Adapters live in sibling packages.** This package (eval/) must not grow
//     a dependency because one benchmark ships parquet or needs a tokenizer.
//   - **Per-case ground truth goes in the case.** A suite's expected answers
//     go in SuiteCase.Scorers, and the adapter maps its format onto them.
//
// Zero cases with a nil error means "this suite is not configured here". A
// caller reports that as a skip, not a pass.
type Adapter interface {
	// Name identifies the suite in reports and skip messages.
	Name() string

	// Load reads the suite and yields its cases. An error means the data was
	// found but could not be read; zero cases with a nil error means the
	// suite is not configured on this machine.
	Load() ([]SuiteCase, error)
}

// SuitePath resolves a benchmark's data location from an environment
// variable, and reports whether it is usable.
//
// Adapters call this instead of reading os.Getenv directly, so they can tell
// three outcomes apart: configured and present, not configured, and configured
// but wrong. Only the third returns an error. Imagine an operator sets the
// variable with a typo in the path. They meant to run the suite. If SuitePath
// reported "not configured", the suite would silently never run.
func SuitePath(envVar string) (path string, ok bool, err error) {
	p := os.Getenv(envVar)
	if p == "" {
		return "", false, nil
	}
	if _, statErr := os.Stat(p); statErr != nil {
		return "", false, fmt.Errorf("eval: %s=%q: %w", envVar, p, statErr)
	}
	return p, true, nil
}

// LoadSuite builds a Suite from an adapter over a base config.
//
// It returns ok=false when the adapter is not configured, so a caller can
// skip. Any other failure is an error.
func LoadSuite(cfg agent.RunnerConfig, a Adapter) (suite Suite, ok bool, err error) {
	cases, err := a.Load()
	if err != nil {
		return Suite{}, false, fmt.Errorf("eval: adapter %q: %w", a.Name(), err)
	}
	if len(cases) == 0 {
		return Suite{}, false, nil
	}
	return Suite{Config: cfg, Cases: cases}, true, nil
}
