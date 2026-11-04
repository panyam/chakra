//go:build eval_llm

package longmemeval

import (
	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// appendRubric adds the LLM-as-judge scorer for a case that has a rubric, for
// the fuzzy categories where a substring check cannot express the grade.
func appendRubric(scorers []eval.Scorer, p agent.Provider, rubric string) []eval.Scorer {
	if rubric == "" || p == nil {
		return scorers
	}
	return append(scorers, eval.Judge(p, rubric))
}

// appendOfficial adds the judge that uses LongMemEval's own prompt for the
// instance's question type. With no provider it adds nothing, as
// appendRubric does, and the case fails for having no scorers.
func appendOfficial(scorers []eval.Scorer, p agent.Provider, inst Instance) []eval.Scorer {
	if p == nil {
		return scorers
	}
	return append(scorers, officialScorer(p, inst))
}
