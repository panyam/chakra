// Package presets builds a RunnerConfig for a common kind of agent, with the
// defaults that kind needs already wired (#282).
//
// Imagine a host that answers questions over a graph with a 14B local model.
// It needs a grammar for the answer, a probe of the data before the first
// call, notes that survive compaction, a check on each draft, a stuck
// detector, a cache for repeated lookups and a cap on how long the model
// looks around. Each of those is a chakra hook, and before this package each
// host wired them by hand. GraphQuery wires them from the host's parts.
//
// A preset lives in code, because its hooks are functions. Its numbers live
// in a Tuning struct with JSON tags, so a host can load them from its own
// config. A zero field keeps the default.
//
// A preset returns a plain agent.RunnerConfig, and a host changes it like
// any struct. Each stage the preset adds has an exported name, so Replace
// and Remove swap or drop one stage without rebuilding the list, and a host
// appends its own stages to StepStages.
package presets
