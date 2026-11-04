//go:build !eval_llm

package longmemeval

import (
	"testing"

	"github.com/panyam/chakra/eval"
)

func TestOfficialJudgeReplacesTheRubric(t *testing.T) {
	for _, official := range []bool{false, true} {
		cases, err := DatasetAdapter{Path: samplePath, OfficialJudge: official}.Load()
		if err != nil {
			t.Fatal(err)
		}
		want := "Rubric"
		if official {
			want = "Official"
		}
		for _, c := range cases {
			scorers := c.Scorers(nil)
			if len(scorers) != 1 {
				t.Fatalf("%s: %d scorers, want 1", c.Scenario.Name, len(scorers))
			}
			score := scorers[0].Score(eval.Result{})
			if score.Name != want || score.Pass {
				t.Errorf("official=%v %s: score = %+v, want a failing %s in the default build", official, c.Scenario.Name, score, want)
			}
		}
	}
}
