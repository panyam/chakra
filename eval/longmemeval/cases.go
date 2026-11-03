// Package longmemeval provides fast, self-contained SMOKE scenarios for the
// Phase 2 memory work. They follow the skill categories of the LongMemEval
// benchmark. These are NOT the LongMemEval benchmark. Each is a short,
// hand-authored conversation of 2–5 turns, while the dataset's histories run
// to ~100k tokens. They exercise the memory plumbing end-to-end with a real
// model and no download. The result is a regression signal, not a
// published-comparable score.
//
// Memory fails silently. A missing or stale memory yields a subtly worse
// answer, not an error. So even a coarse model-graded check is worth having,
// in a form that runs with zero external data.
//
// The rigorous bar is the actual LongMemEval dataset, adapted through this
// same eval harness. DatasetAdapter in dataset.go reads the downloaded dataset
// from an env path. The dataset is NOT vendored; see ATTRIBUTION.md. mcpkit's
// conformance suites use the same convention for external test data.
// Comparable numbers come from that loader and the general eval.Adapter seam,
// and the seam lets the harness point at many external suites.
//
// The scenario table and its deterministic assertions live in the default
// build, so unit tests can check that they are well-formed. live_test.go runs
// them against a live model behind the eval_llm build tag, with an LLM judge
// for the fuzzy categories.
package longmemeval

import (
	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// Category names the LongMemEval-style skill a case exercises.
type Category string

const (
	// CatExtraction — recall a fact stated earlier in one conversation.
	CatExtraction Category = "extraction"
	// CatMultiSession — synthesize facts stated across separate sessions.
	CatMultiSession Category = "multi-session"
	// CatKnowledgeUpdate — a fact was superseded; the latest value must win.
	CatKnowledgeUpdate Category = "knowledge-update"
	// CatTemporal — reason about the order or timing of remembered facts.
	CatTemporal Category = "temporal"
	// CatAbstention — a fact was never stated; the model must not invent one.
	CatAbstention Category = "abstention"
	// CatCompaction tests that an early fact survives history compaction. The
	// run uses a low compaction budget, so the head is summarized. The answer
	// still depends on a detail from the summarized region.
	CatCompaction Category = "compaction"
)

// memoryInstructions steer the model to actually use its scratchpad. The
// benchmark tests the memory tools, not the context window.
const memoryInstructions = "You have a working memory with remember, recall, and forget tools. " +
	"Save durable facts the user tells you, and recall them before answering questions about earlier turns. " +
	"If you were never told something, say you do not know rather than guessing."

// MemCase is one memory scenario plus how to grade its final answer.
// Deterministic substring checks (Must / MustNot) grade it where the expected
// token is unambiguous. A Rubric for the LLM judge grades it where it is not.
type MemCase struct {
	Category Category
	Scenario eval.Scenario
	// Rubric grades the final answer via the LLM judge (fuzzy categories).
	// Empty skips the judge.
	Rubric string
	// Must requires each substring in the final answer (deterministic).
	Must []string
	// MustNot forbids each substring in the final answer (deterministic). The
	// supersede and abstention checks use it.
	MustNot []string

	// NewCompactor builds a Compactor from the run's provider, and the
	// scenario runs under it. It takes the provider because a
	// SummarizingCompactor needs a model to summarize the head. Nil runs with
	// no compaction. A CatCompaction case sets it to prove a fact survives the
	// compaction boundary. It returns a Compactor rather than a token budget,
	// so a case can plug in ANY compaction strategy, SummarizingCompactor or
	// another. The Compactor itself decides per turn whether to compact
	// (Compact is a no-op under budget). So this field chooses the strategy,
	// not the trigger.
	NewCompactor func(provider agent.Provider) (agent.Compactor, error)
}

// SmokeScenarios returns the illustrative smoke set, one short scenario per
// category. Each is memory-enabled so the model manages the fact through its
// tools. These are a plumbing regression signal, not the LongMemEval
// benchmark; see the package doc.
func SmokeScenarios() []MemCase {
	scenario := func(name string, turns ...string) eval.Scenario {
		return eval.Scenario{Name: name, Turns: turns, Memory: true, Instructions: memoryInstructions}
	}
	return []MemCase{
		{
			Category: CatExtraction,
			Scenario: scenario("extraction-workplace",
				"My name is Ada and I work at Analytical Engines Inc.",
				"By the way, my primary language is Go.",
				"Where do I work?"),
			Must:   []string{"Analytical Engines"},
			Rubric: "The answer must state that the user works at Analytical Engines Inc.",
		},
		{
			Category: CatMultiSession,
			Scenario: scenario("multisession-dog-breed",
				"[Session 1] I adopted a dog last month and named her Pixel.",
				"[Session 2, weeks later] Pixel turned out to be a border collie.",
				"What breed is my dog?"),
			Must:   []string{"border collie"},
			Rubric: "The answer must identify the user's dog Pixel as a border collie, joining facts from two sessions.",
		},
		{
			Category: CatKnowledgeUpdate,
			Scenario: scenario("update-editor",
				"My favorite code editor is Sublime Text.",
				"Actually, I switched to VS Code last week and love it.",
				"Which editor do I use now?"),
			Must:    []string{"VS Code"},
			MustNot: []string{"Sublime"},
			Rubric:  "The answer must say VS Code (the updated value) and must not claim the user still uses Sublime Text.",
		},
		{
			Category: CatTemporal,
			Scenario: scenario("temporal-cities",
				"In 2019 I was living in Berlin.",
				"In 2022 I moved to Tokyo.",
				"Which of those two cities did I live in earliest?"),
			Must:   []string{"Berlin"},
			Rubric: "The answer must say Berlin, reasoning that 2019 is earlier than 2022.",
		},
		{
			Category: CatAbstention,
			Scenario: scenario("abstention-sister",
				"My name is Ada and I have a younger sister.",
				"What is my sister's name?"),
			MustNot: []string{"Analytical"}, // must not bleed an unrelated remembered token
			Rubric:  "The user never gave their sister's name. The answer must acknowledge it does not know and must NOT invent a name.",
		},
		{
			Category: CatCompaction,
			Scenario: eval.Scenario{
				Name:         "compaction-employee-id",
				Instructions: memoryInstructions,
				// The first turn states the ID. Filler chatter then buries it, so
				// a low compaction budget summarizes it out of the raw
				// transcript. The summary must keep the ID for the final answer.
				Turns: []string{
					"Please note my employee ID is 4471 for anything work-related.",
					"Also I like hiking on weekends and cold brew coffee.",
					"My commute is about 40 minutes each way on the train.",
					"I have a standup every morning at 9:30.",
					"What is my employee ID?",
				},
			},
			Must:   []string{"4471"},
			Rubric: "The answer must give the employee ID 4471, recalled from the earlier (compacted) part of the conversation.",
			NewCompactor: func(p agent.Provider) (agent.Compactor, error) {
				return agent.NewSummarizingCompactor(agent.SummarizingConfig{Provider: p, MaxTokens: 40})
			},
		},
	}
}

// Deterministic returns the substring scorers for a case (Must → Contains,
// MustNot → NotContains). These run without a model, so they are the CI-safe
// regression layer; the LLM judge (Rubric) is the periodic quality layer.
func (c MemCase) Deterministic() []eval.Scorer {
	var out []eval.Scorer
	for _, s := range c.Must {
		out = append(out, eval.Contains(s))
	}
	for _, s := range c.MustNot {
		out = append(out, eval.NotContains(s))
	}
	return out
}

// SuiteCase adapts a MemCase onto the harness's graded unit.
//
// An adapter does only this. It maps the suite's shape onto a Scenario plus
// the scorers that grade it. Here the shape is a category, a rubric,
// substring requirements, and an optional compaction strategy. The Runner,
// the scorers, and the report are untouched.
//
// The name includes the category. A report then groups readably, and the
// harness never needs to know what a category is.
func (c MemCase) SuiteCase() eval.SuiteCase {
	s := c.Scenario
	s.Name = string(c.Category) + "/" + s.Name

	sc := eval.SuiteCase{Scenario: s}

	sc.Scorers = func(p agent.Provider) []eval.Scorer {
		// The judge needs a model, so a rubric cannot be built until the
		// suite's provider is known. Scorers takes the provider for that reason
		// rather than being a plain slice. Without the eval_llm build tag,
		// eval.Judge does not exist, so appendRubric adds eval.Ungradeable for
		// a rubric instead. The case then fails and names the tag to use.
		return appendRubric(c.Deterministic(), p, c.Rubric)
	}

	if c.NewCompactor != nil {
		// A compaction case tests the harness, not the model. It proves a
		// fact survives the compaction boundary, so it has to run under a
		// Compactor. Building the Compactor needs the provider, because a
		// SummarizingCompactor summarizes with a model. Configure receives
		// the config, and the config holds the provider. So this has to be
		// a Configure func, not a static override.
		sc.Configure = func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
			compactor, err := c.NewCompactor(cfg.Provider)
			if err != nil {
				return cfg, err
			}
			cfg.Compactor = compactor
			return cfg, nil
		}
	}

	return sc
}

// SmokeAdapter yields the in-repo smoke set as an eval.Adapter.
//
// It needs no external data, so unlike a real corpus adapter it is always
// configured. It proves the seam with something that runs today. The
// LongMemEval corpus loader (issue 1014) implements the same interface over
// LONGMEMEVAL_DATA_PATH. It sits beside this one and needs no harness change.
type SmokeAdapter struct{}

// Name identifies the suite in reports.
func (SmokeAdapter) Name() string { return "longmemeval-smoke" }

// Load yields the smoke set. It never returns zero cases, because the data is
// compiled in.
func (SmokeAdapter) Load() ([]eval.SuiteCase, error) {
	cases := SmokeScenarios()
	out := make([]eval.SuiteCase, 0, len(cases))
	for _, c := range cases {
		out = append(out, c.SuiteCase())
	}
	return out, nil
}

var _ eval.Adapter = SmokeAdapter{}
