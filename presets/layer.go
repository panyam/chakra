package presets

import (
	"slices"

	agent "github.com/panyam/chakra"
)

// Replace puts st in place of the first stage named name, and reports
// whether one was found. It returns a new slice and leaves stages alone. With
// no stage of that name, it returns stages unchanged and false, so a host
// can tell a typo from a swap.
func Replace(stages []agent.StepStage, name string, st agent.StepStage) ([]agent.StepStage, bool) {
	i := slices.IndexFunc(stages, func(s agent.StepStage) bool { return s.Name == name })
	if i < 0 {
		return stages, false
	}
	out := slices.Clone(stages)
	out[i] = st
	return out, true
}

// Remove drops every stage named name. It returns a new slice and leaves
// stages alone.
func Remove(stages []agent.StepStage, name string) []agent.StepStage {
	return slices.DeleteFunc(slices.Clone(stages), func(s agent.StepStage) bool { return s.Name == name })
}
