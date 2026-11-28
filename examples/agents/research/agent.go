package research

import (
	"github.com/panyam/chakra/examples/agents/common"
)

// Agent is research as the exercise and the walk run it.
func Agent() common.Agent {
	return common.Agent{
		Name:         "research",
		Ladder:       Ladder,
		Instructions: Instructions,
		Scripted:     Scripted,
		MaxSteps:     12,
		About: []string{
			"research answers questions about Shetland's islands and lighthouses, from snapshots of real pages.",
			"Three sources: Wikipedia pages, Wikivoyage pages, and older revisions of Wikipedia pages, which the archive holds. A search tool and a fetch tool read them, with no network.",
			"A case passes when the answer is right and it cites a page that supports it. Two things trip a model: a question that needs a fact from one page to find another, and an older revision that gives a figure a newer page has replaced.",
		},
		StepNotes: map[string]string{
			"search + fetch":   "The model gets the two tools and a prompt that asks for a short answer and its URLs.",
			"+ citation check": "A turn-end hook refuses an answer that cites nothing, or cites a page the turn never fetched.",
			"+ plan slot":      "The model keeps notes in a slot: the sub-questions, and each fact with its URL. The notes go before every model call.",
			"+ sub-agents":     "search gives way to research, which sends a task to three sub-agents at once, one for each source. Each searches and fetches in its own source and reports facts with URLs.",
			"+ critic":         "A second call to the model reads the cited pages and refuses an answer they don't support.",
			"+ cost cap":       "A token budget across the question and its sub-agents ends a question that runs long.",
		},
	}
}
