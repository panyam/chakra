package coding

import (
	"regexp"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

// stuckExtension adds agent.NewStuckDetector to the coding agent (#285), with
// the default note as its policy. It keys steps with stuckKey.
type stuckExtension struct{ host.BaseExtension }

func (stuckExtension) Name() string { return "coding.stuck" }

func (stuckExtension) StepStages() []agent.StepStage {
	return []agent.StepStage{agent.NewStuckDetector(agent.StuckConfig{Key: stuckKey})}
}

// durations matches the timings go test and the exec tools print, such as
// "(0.00s)", "0.214s", "950ms" and "1m2.5s".
var durations = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:h|m(?:\d+(?:\.\d+)?s)?|s|ms|µs|us|ns)\b`)

// stuckKey is agent.DefaultStuckKey over tool results with their timings
// blanked. Two test runs that fail the same way then count as the same
// result, whatever they took.
func stuckKey(rec agent.StepRecord) (agent.StuckKey, bool) {
	results := make([]agent.Message, len(rec.Results))
	for i, m := range rec.Results {
		m.Text = durations.ReplaceAllString(m.Text, "<t>")
		results[i] = m
	}
	rec.Results = results
	return agent.DefaultStuckKey(rec)
}
