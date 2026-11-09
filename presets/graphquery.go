package presets

import (
	"context"
	"errors"
	"slices"

	agent "github.com/panyam/chakra"
)

// The names of the stages GraphQuery adds, for Replace and Remove.
const (
	// StageRetrieval is the probe before the first call, from
	// GraphQuery.Retrievers.
	StageRetrieval = "presets.graph_query.retrieval"
	// StageStuck is the stuck detector.
	StageStuck = "presets.graph_query.stuck"
	// StageActions takes the tools away once the model has used
	// Tuning.ActionSteps steps calling them.
	StageActions = "presets.graph_query.actions"
	// StageLedger shows GraphQuery.Ledger on every call.
	StageLedger = "presets.graph_query.ledger"
)

// GraphQueryTuning holds GraphQuery's numbers. A zero field keeps the
// default, so a host can load the fields it changes from its own config and
// leave the rest out.
type GraphQueryTuning struct {
	// MaxTokens caps each reply. Zero means 512, which fits an answer in a
	// query language. A thinking model needs more.
	MaxTokens int `json:"maxTokens,omitempty"`
	// Temperature is sent when the provider takes one. Zero is the default,
	// so the same question gets the same query.
	Temperature float64 `json:"temperature,omitempty"`
	// NudgeTemperature is sent for the call after the stuck detector's first
	// finding, when the provider takes a temperature. At temperature 0 the
	// same messages draw the same reply, so the note alone rarely moves the
	// model. Zero means 0.7.
	NudgeTemperature float64 `json:"nudgeTemperature,omitempty"`
	// Penalties go out with each call, filtered by the provider's
	// Capabilities.Sendable. Nil means DRY at multiplier 0.8, base 1.75 and
	// allowed length 2, which llama.cpp's docs suggest. An empty struct sends
	// none.
	Penalties *agent.SamplingPenalties `json:"penalties,omitempty"`
	// MaxSteps caps the turn's steps. Zero means 12.
	MaxSteps int `json:"maxSteps,omitempty"`
	// ActionSteps is how many steps that call tools the model gets. After
	// that, StageActions offers no tools, so the model answers, under the
	// grammar when there is one. Zero means 8. A negative value never takes
	// the tools away.
	ActionSteps int `json:"actionSteps,omitempty"`
	// MaxFreeRefusals caps the free results and free refusals in a turn, as
	// RunnerConfig.MaxFreeRefusals does. Zero means 3, and a negative value
	// makes every one count.
	MaxFreeRefusals int `json:"maxFreeRefusals,omitempty"`
	// StuckRepeats and StuckAlternations are StuckConfig's Repeats and
	// Alternations. Zero means 2 for each, since a lookup that came back
	// once already won't change.
	StuckRepeats      int `json:"stuckRepeats,omitempty"`
	StuckAlternations int `json:"stuckAlternations,omitempty"`
	// StuckErrors is StuckConfig.Errors. Zero means 3, and a negative value
	// turns it off.
	StuckErrors int `json:"stuckErrors,omitempty"`
	// StuckWindow and StuckWindowRepeats are StuckConfig's Window and
	// WindowRepeats. Zero means a window of 6 steps and 3 repeats within it.
	// A negative StuckWindow turns the window off.
	StuckWindow        int `json:"stuckWindow,omitempty"`
	StuckWindowRepeats int `json:"stuckWindowRepeats,omitempty"`
	// RepeatMax is RepeatCacheConfig.Max. Zero means 3, and a negative value
	// leaves the repeat cache out.
	RepeatMax int `json:"repeatMax,omitempty"`
	// RetrievalBudget is RetrievalConfig.Budget. Zero means 2000 tokens.
	RetrievalBudget int `json:"retrievalBudget,omitempty"`
}

// GraphQuery is a preset for a model that answers a question by looking at
// some data and writing a query or an answer under a grammar (#300). The
// host brings what only it knows: the provider, the instructions, the
// grammar, the tools, the retrievers, the ledger and the check. Config wires
// them with these defaults.
//
//   - Temperature and MaxTokens from Tuning, the temperature only when the
//     provider takes one.
//   - Penalties filtered by Capabilities.Sendable, so the same preset runs on
//     llama.cpp and on Ollama.
//   - The grammar as RunnerConfig.ResponseGuided, only when the provider can
//     send a grammar. A provider that can't gets the turn without it rather
//     than a failed call.
//   - The repeat cache, so a repeated idempotent lookup is answered from its
//     first result and costs no step.
//   - The stuck detector over the last steps. Its first finding adds a note
//     and the nudge temperature for one call, and a finding after that runs
//     StillStuck.
//   - StageActions, which offers no tools after Tuning.ActionSteps steps
//     spent calling them.
//   - The retrieval block, the ledger and the stuck note go as RoleUser
//     messages, since chat templates such as Gemma's and Mistral's on
//     llama.cpp reject a system message that isn't the first.
type GraphQuery struct {
	// Provider answers each call. It is required.
	Provider agent.Provider
	// Instructions is the system prompt.
	Instructions string
	// Grammar is the answer's grammar, in the server's own grammar language.
	// Empty sends no grammar. Leave it empty for a thinking model, since a
	// grammar covers the whole reply.
	Grammar string
	// Tools are what the model looks at the data with. A tool marked
	// ReadOnly or Idempotent goes through the repeat cache.
	Tools agent.ToolSource
	// Retrievers probe the data for the question before the first call.
	// None leaves StageRetrieval out.
	Retrievers []agent.Retriever
	// Ledger is shown after the history on every call. Nil leaves
	// StageLedger out. Declare it with NewSlotsWith and AsUser, so its
	// message goes as RoleUser like the preset's others.
	Ledger *agent.Slots
	// LedgerSync, when set, runs before the ledger is shown on each call, so
	// a host that keeps the ledger itself writes it into the slots there. A
	// model that writes the slots needs Ledger.Tool in Tools instead.
	LedgerSync func(ctx context.Context, ledger *agent.Slots) error
	// Check judges each answer, as a TurnEndHook. Nil accepts every answer.
	Check agent.TurnEndHook
	// StuckKey says what counts as the same step. Nil means
	// agent.DefaultStuckKey, which reads tool calls. A model that acts
	// through text needs a key that reads StepRecord.Assistant.Text.
	StuckKey agent.StuckKeyFunc
	// StuckNote writes the note for the stuck detector's first finding. Nil
	// names what repeated and asks the model to try something else.
	StuckNote func(agent.StuckFinding) string
	// StillStuck runs on a finding after the first. Nil takes every tool away
	// for that call and asks for the answer, so the turn ends on the
	// model's best answer rather than on the step cap.
	StillStuck agent.StuckPolicy
	// Tuning holds the numbers.
	Tuning GraphQueryTuning
}

// Defaults for GraphQueryTuning's zero fields.
const (
	defaultMaxTokens        = 512
	defaultNudgeTemperature = 0.7
	defaultMaxSteps         = 12
	defaultActionSteps      = 8
	defaultStuckRepeats     = 2
	defaultStuckErrors      = 3
	defaultStuckWindow      = 6
	defaultStuckWindowHits  = 3
)

// defaultPenalties is GraphQueryTuning.Penalties when it is nil.
func defaultPenalties() *agent.SamplingPenalties {
	mult, base, allowed := 0.8, 1.75, 2
	return &agent.SamplingPenalties{DRYMultiplier: &mult, DRYBase: &base, DRYAllowedLength: &allowed}
}

// Config returns the RunnerConfig. It fails only without a Provider. The
// result is the host's to change before NewRunner.
func (g GraphQuery) Config() (agent.RunnerConfig, error) {
	if g.Provider == nil {
		return agent.RunnerConfig{}, errors.New("presets: GraphQuery requires a Provider")
	}
	t := g.Tuning.withDefaults()
	caps := agent.CapabilitiesOf(g.Provider)

	gen := agent.GenerationParams{MaxTokens: t.MaxTokens, Penalties: caps.Sendable(t.Penalties)}
	if caps.Temperature {
		temp := t.Temperature
		gen.Temperature = &temp
	}
	cfg := agent.RunnerConfig{
		Provider:        g.Provider,
		Instructions:    g.Instructions,
		Tools:           g.Tools,
		MaxSteps:        t.MaxSteps,
		MaxFreeRefusals: t.MaxFreeRefusals,
		Generation:      gen,
	}
	if guided := (&agent.GuidedDecoding{Grammar: g.Grammar}); g.Grammar != "" && caps.CanGuide(guided) {
		cfg.ResponseGuided = guided
	}
	if g.Check != nil {
		cfg.TurnEnd = []agent.TurnEndHook{g.Check}
	}
	if t.RepeatMax > 0 {
		cfg.ToolMiddleware = []agent.ToolMiddleware{agent.NewRepeatCache(agent.RepeatCacheConfig{Max: t.RepeatMax})}
	}

	if len(g.Retrievers) > 0 {
		st := agent.NewRetrievalStage(agent.RetrievalConfig{Retrievers: g.Retrievers, Budget: t.RetrievalBudget, AsUser: true})
		st.Name = StageRetrieval
		cfg.StepStages = append(cfg.StepStages, st)
	}
	cfg.StepStages = append(cfg.StepStages, g.stuckStage(t, caps), actionsStage(t.ActionSteps))
	if g.Ledger != nil {
		cfg.StepStages = append(cfg.StepStages, g.ledgerStage())
	}
	return cfg, nil
}

func (t GraphQueryTuning) withDefaults() GraphQueryTuning {
	or := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	or(&t.MaxTokens, defaultMaxTokens)
	or(&t.MaxSteps, defaultMaxSteps)
	or(&t.ActionSteps, defaultActionSteps)
	or(&t.StuckRepeats, defaultStuckRepeats)
	or(&t.StuckAlternations, defaultStuckRepeats)
	or(&t.StuckErrors, defaultStuckErrors)
	or(&t.StuckWindow, defaultStuckWindow)
	or(&t.StuckWindowRepeats, defaultStuckWindowHits)
	or(&t.RepeatMax, 3)
	if t.NudgeTemperature == 0 {
		t.NudgeTemperature = defaultNudgeTemperature
	}
	if t.Penalties == nil {
		t.Penalties = defaultPenalties()
	}
	return t
}

// stuckStage runs the detector. A finding at its pattern's threshold is the
// first one, since the detector fires again with a higher Count while the
// pattern holds. So the policy needs no state of its own.
func (g GraphQuery) stuckStage(t GraphQueryTuning, caps agent.Capabilities) agent.StepStage {
	first := map[string]int{
		agent.StuckRepeat:    t.StuckRepeats,
		agent.StuckAlternate: t.StuckAlternations,
		agent.StuckSameError: t.StuckErrors,
		agent.StuckWindow:    t.StuckWindowRepeats,
	}
	note := g.StuckNote
	if note == nil {
		note = func(f agent.StuckFinding) string {
			return "You have " + f.String() + ". Doing it again will give the same result. Try a different approach, or answer with what you have."
		}
	}
	still := g.StillStuck
	if still == nil {
		still = answerNow
	}
	nudge := t.NudgeTemperature
	st := agent.NewStuckDetector(agent.StuckConfig{
		Key:           g.StuckKey,
		Repeats:       t.StuckRepeats,
		Alternations:  t.StuckAlternations,
		Errors:        t.StuckErrors,
		Window:        max(t.StuckWindow, 0),
		WindowRepeats: t.StuckWindowRepeats,
		Policy: func(ctx context.Context, info agent.StepInfo, v agent.StepView, f agent.StuckFinding) (agent.StepView, error) {
			if f.Count > first[f.Pattern] {
				return still(ctx, info, v, f)
			}
			v.Messages = append(slices.Clip(v.Messages), agent.Message{Role: agent.RoleUser, Text: note(f)})
			if caps.Temperature {
				v.Generation.Temperature = &nudge
			}
			return v, nil
		},
	})
	st.Name = StageStuck
	return st
}

// answerNow is StillStuck's default.
func answerNow(_ context.Context, _ agent.StepInfo, v agent.StepView, f agent.StuckFinding) (agent.StepView, error) {
	v.Tools = nil
	v.Messages = append(slices.Clip(v.Messages), agent.Message{Role: agent.RoleUser,
		Text: "You have " + f.String() + ". Looking again won't help. Answer now with what you have."})
	return v, nil
}

// actionsStage offers no tools once the turn has n steps that called one.
func actionsStage(n int) agent.StepStage {
	return agent.StepStage{Name: StageActions, Scope: agent.ScopeRequest,
		Run: func(_ context.Context, info agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			if n < 0 {
				return v, nil
			}
			spent := 0
			for _, m := range info.Turn {
				if m.Role == agent.RoleAssistant && len(m.ToolCalls) > 0 {
					spent++
				}
			}
			if spent >= n {
				v.Tools = nil
			}
			return v, nil
		}}
}

func (g GraphQuery) ledgerStage() agent.StepStage {
	show := g.Ledger.StepStage()
	return agent.StepStage{Name: StageLedger, Scope: agent.ScopeRequest,
		Run: func(ctx context.Context, info agent.StepInfo, v agent.StepView) (agent.StepView, error) {
			if g.LedgerSync != nil {
				if err := g.LedgerSync(ctx, g.Ledger); err != nil {
					return v, err
				}
			}
			return show.Run(ctx, info, v)
		}}
}
