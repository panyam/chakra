//go:build !eval_llm

package longmemeval

import (
	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// appendRubric marks a rubric case as ungraded in the default build.
//
// eval.Judge needs a live model and lives behind the eval_llm tag, so the
// rubric cannot be evaluated here. Returning the deterministic scorers alone
// would be worse than it looks. Imagine a case whose real grade IS the rubric.
// Any answer at all can satisfy its remaining scorers. The abstention case has
// exactly that shape. Its only deterministic check is that one unrelated token
// is absent, so a model replying with nonsense scores as having correctly
// abstained.
//
// Suite.Run already treats a case with no scorers as a failure, because
// nothing would ever fail it and it would pass forever. This applies the same
// reasoning one step further. Checks that cannot fail do not grade a case
// either. So the case fails, legibly, and names the tag that would grade it.
func appendRubric(scorers []eval.Scorer, _ agent.Provider, rubric string) []eval.Scorer {
	if rubric == "" {
		return scorers
	}
	return append(scorers, eval.Ungradeable("Rubric",
		"this case is graded by an LLM rubric; run with -tags eval_llm and a live provider"))
}
