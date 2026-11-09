package presets

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

const grammar = `root ::= "answer"`

// llama is a StubProvider that sends what llama-server sends.
type llama struct{ *agent.StubProvider }

func (llama) ProviderCapabilities() agent.Capabilities {
	return agent.Capabilities{Guided: []string{agent.GuidedGrammar}, Temperature: true,
		Penalties: []string{agent.PenaltyRepeat, agent.PenaltyFrequency, agent.PenaltyPresence, agent.PenaltyDRY}}
}

// ollama sends a temperature and a frequency penalty, and no grammar.
type ollama struct{ *agent.StubProvider }

func (ollama) ProviderCapabilities() agent.Capabilities {
	return agent.Capabilities{Temperature: true, Penalties: []string{agent.PenaltyFrequency, agent.PenaltyPresence}}
}

// rig is a GraphQuery over a stub with a read-only "look" tool that counts
// its runs. It sets no grammar, since an answer from a step that offered
// tools then takes a second call, the guided finalize.
type rig struct {
	stub  *agent.StubProvider
	looks atomic.Int32
	q     GraphQuery
}

func newRig(t *testing.T, turns ...agent.StubTurn) *rig {
	t.Helper()
	r := &rig{stub: agent.NewStubProvider(turns...)}
	src := agent.NewFuncSource()
	type in struct {
		X string `json:"x"`
	}
	if err := agent.AddFunc(src, "look", "looks", func(_ context.Context, i in) (string, error) {
		r.looks.Add(1)
		return "look " + i.X, nil
	}, agent.ReadOnly()); err != nil {
		t.Fatal(err)
	}
	r.q = GraphQuery{Provider: llama{r.stub}, Instructions: "Answer in the grammar.", Tools: src}
	return r
}

func (r *rig) run(t *testing.T) (*agent.TurnResult, error) {
	t.Helper()
	cfg, err := r.q.Config()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return runner.Run(context.Background(), []agent.Message{{Role: agent.RoleUser, Text: "which roads reach the port?"}}, nil)
}

func look(x string) agent.StubTurn {
	return agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c", Name: "look", Args: core.NewRawJSON(json.RawMessage(`{"x":"` + x + `"}`))}}}
}

func lastMessage(req agent.ProviderRequest) agent.Message { return req.Messages[len(req.Messages)-1] }

// Each request carries the preset's generation params, and the answer is held
// to the grammar.
func TestGraphQuerySendsTheDefaults(t *testing.T) {
	r := newRig(t, look("a"), agent.StubTurn{Text: "the answer is"}, agent.StubTurn{Text: "answer"})
	r.q.Grammar = grammar
	res, err := r.run(t)
	if err != nil || res.Text != "answer" {
		t.Fatalf("%+v %v", res, err)
	}
	reqs := r.stub.Requests()
	first := reqs[0]
	if first.Temperature == nil || *first.Temperature != 0 || first.MaxTokens != 512 {
		t.Errorf("temperature %v, max tokens %d; want 0, 512", first.Temperature, first.MaxTokens)
	}
	if p := first.Penalties; p == nil || p.DRYMultiplier == nil || *p.DRYMultiplier != 0.8 || *p.DRYBase != 1.75 || *p.DRYAllowedLength != 2 {
		t.Errorf("penalties %+v", p)
	}
	if first.Guided != nil || len(first.Tools) != 1 {
		t.Errorf("step 1 guided %v with %d tools; a step with tools sends no grammar", first.Guided, len(first.Tools))
	}
	if last := reqs[len(reqs)-1]; last.Guided == nil || last.Guided.Grammar != grammar {
		t.Errorf("the answer went out under %+v", last.Guided)
	}
}

// The config a host would build by hand, for a llama.cpp and an Ollama
// provider.
func TestGraphQueryConfigFitsTheProvider(t *testing.T) {
	q := GraphQuery{Provider: llama{agent.NewStubProvider()}, Grammar: grammar}
	cfg, err := q.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxSteps != 12 || cfg.MaxFreeRefusals != 0 || cfg.ResponseGuided == nil || len(cfg.ToolMiddleware) != 1 {
		t.Errorf("llama: steps %d, free %d, guided %v, middleware %d", cfg.MaxSteps, cfg.MaxFreeRefusals, cfg.ResponseGuided, len(cfg.ToolMiddleware))
	}
	if got := stageNames(cfg.StepStages); got != StageStuck+" "+StageActions {
		t.Errorf("stages %q", got)
	}

	q.Provider = ollama{agent.NewStubProvider()}
	q.Tuning.Penalties = &agent.SamplingPenalties{Frequency: ptr(0.3), DRYMultiplier: ptr(0.8)}
	cfg, err = q.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResponseGuided != nil {
		t.Errorf("ollama got a grammar it can't send")
	}
	if p := cfg.Generation.Penalties; p == nil || p.Frequency == nil || p.DRYMultiplier != nil {
		t.Errorf("ollama penalties %+v; want frequency only", p)
	}
}

// A provider that takes no temperature is sent none, even on a nudge.
func TestGraphQuerySendsNoTemperatureToAProviderThatTakesNone(t *testing.T) {
	cfg, err := GraphQuery{Provider: agent.NewStubProvider()}.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Generation.Temperature != nil || cfg.Generation.Penalties != nil {
		t.Errorf("generation %+v", cfg.Generation)
	}
}

// The probe goes before the question and the ledger after the history, both
// as the user's.
func TestGraphQueryRetrievalAndLedgerGoAsTheUsers(t *testing.T) {
	r := newRig(t, agent.StubTurn{Text: "answer"})
	r.q.Retrievers = []agent.Retriever{agent.NewRetriever("sample", func(context.Context, agent.RetrievalQuery) ([]agent.Snippet, error) {
		return []agent.Snippet{{Source: "roads", Text: "road(a, port)", Score: 1}}, nil
	})}
	ledger, err := agent.NewSlotsWith(agent.SlotsConfig{Specs: []agent.SlotSpec{{Name: "tried"}}, Header: "Tried so far:", AsUser: true})
	if err != nil {
		t.Fatal(err)
	}
	r.q.Ledger = ledger
	syncs := 0
	r.q.LedgerSync = func(ctx context.Context, l *agent.Slots) error {
		syncs++
		_, err := l.Write(ctx, "tried", "look a")
		return err
	}
	if _, err := r.run(t); err != nil {
		t.Fatal(err)
	}
	msgs := r.stub.Requests()[0].Messages
	if len(msgs) != 3 {
		t.Fatalf("messages %+v", msgs)
	}
	if probe := msgs[0]; probe.Role != agent.RoleUser || !strings.Contains(probe.Text, "road(a, port)") {
		t.Errorf("probe %+v", probe)
	}
	if notes := msgs[2]; notes.Role != agent.RoleUser || !strings.HasPrefix(notes.Text, "Tried so far:") || !strings.Contains(notes.Text, "look a") {
		t.Errorf("ledger %+v", notes)
	}
	if syncs != 1 {
		t.Errorf("ledger synced %d times", syncs)
	}
}

// A repeated lookup is answered from the first result and costs no step.
func TestGraphQueryAnswersARepeatedLookFromTheCache(t *testing.T) {
	r := newRig(t, look("a"), look("a"), agent.StubTurn{Text: "answer"})
	res, err := r.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if r.looks.Load() != 1 || res.FreeRefusals != 1 {
		t.Errorf("look ran %d times, %d free", r.looks.Load(), res.FreeRefusals)
	}
}

// The first finding sends a note and the nudge temperature for one call. The
// next takes the tools away and asks for the answer.
func TestGraphQueryNudgesThenAsksForTheAnswer(t *testing.T) {
	r := newRig(t, look("a"), look("a"), look("a"), agent.StubTurn{Text: "answer"})
	r.q.Tuning.RepeatMax = -1
	if _, err := r.run(t); err != nil {
		t.Fatal(err)
	}
	reqs := r.stub.Requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests", len(reqs))
	}
	nudged := reqs[2]
	if m := lastMessage(nudged); m.Role != agent.RoleUser || !strings.Contains(m.Text, "repeated look 2 times") {
		t.Errorf("note %+v", m)
	}
	if nudged.Temperature == nil || *nudged.Temperature != 0.7 || len(nudged.Tools) != 1 {
		t.Errorf("nudged call: temperature %v, %d tools", nudged.Temperature, len(nudged.Tools))
	}
	still := reqs[3]
	if len(still.Tools) != 0 || !strings.Contains(lastMessage(still).Text, "Answer now") {
		t.Errorf("still stuck: %d tools, last %+v", len(still.Tools), lastMessage(still))
	}
	if still.Temperature == nil || *still.Temperature != 0 {
		t.Errorf("the nudge outlived its call: %v", still.Temperature)
	}
}

func countLooks(msgs []agent.Message) int {
	n := 0
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.Name == "look" {
				n++
			}
		}
	}
	return n
}

// FirstStuck runs before the note, so the nudged call goes out with the
// repeat removed and the note still last.
func TestGraphQueryFirstStuckRunsBeforeTheNote(t *testing.T) {
	r := newRig(t, look("a"), look("a"), agent.StubTurn{Text: "answer"})
	r.q.Tuning.RepeatMax = -1
	r.q.FirstStuck = agent.CollapseStuckSteps(nil)
	if _, err := r.run(t); err != nil {
		t.Fatal(err)
	}
	nudged := r.stub.Requests()[2]
	if n := countLooks(nudged.Messages); n != 1 {
		t.Errorf("the nudged call sees %d looks, want 1", n)
	}
	if m := lastMessage(nudged); m.Role != agent.RoleUser || !strings.Contains(m.Text, "repeated look 2 times") {
		t.Errorf("last message %+v, want the note", m)
	}
}

func TestGraphQueryEmptyStuckNoteAddsNoMessage(t *testing.T) {
	r := newRig(t, look("a"), look("a"), agent.StubTurn{Text: "answer"})
	r.q.Tuning.RepeatMax = -1
	r.q.StuckNote = func(agent.StuckFinding) string { return "" }
	if _, err := r.run(t); err != nil {
		t.Fatal(err)
	}
	if m := lastMessage(r.stub.Requests()[2]); m.Role == agent.RoleUser {
		t.Errorf("an empty note still sent %+v", m)
	}
}

// A host's StillStuck replaces the default.
func TestGraphQueryStillStuckIsTheHosts(t *testing.T) {
	r := newRig(t, look("a"), look("a"), look("a"), agent.StubTurn{Text: "answer"})
	r.q.Tuning.RepeatMax = -1
	r.q.StillStuck = agent.StuckStop()
	if _, err := r.run(t); !errors.Is(err, agent.ErrTurnStopped) {
		t.Errorf("err %v, want ErrTurnStopped", err)
	}
}

// After ActionSteps steps that called tools, the model is offered none and
// answers under the grammar.
func TestGraphQueryTakesTheToolsAwayAfterActionSteps(t *testing.T) {
	r := newRig(t, look("a"), look("b"), agent.StubTurn{Text: "answer"})
	r.q.Grammar = grammar
	r.q.Tuning.ActionSteps = 2
	if _, err := r.run(t); err != nil {
		t.Fatal(err)
	}
	reqs := r.stub.Requests()
	if len(reqs[1].Tools) != 1 {
		t.Errorf("step 2 had %d tools", len(reqs[1].Tools))
	}
	if last := reqs[2]; len(last.Tools) != 0 || last.Guided == nil || last.Guided.Grammar != grammar {
		t.Errorf("step 3: %d tools, guided %+v", len(last.Tools), last.Guided)
	}
}

// Check judges each answer, and a refused one goes back to the model.
func TestGraphQueryChecksTheAnswer(t *testing.T) {
	r := newRig(t, agent.StubTurn{Text: "answer"}, agent.StubTurn{Text: "answer"})
	checks := 0
	r.q.Check = func(context.Context, agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
		if checks++; checks == 1 {
			return agent.TurnEndVerdict{Continue: "no rows; try another road", AsUser: true}, nil
		}
		return agent.TurnEndVerdict{}, nil
	}
	res, err := r.run(t)
	if err != nil || checks != 2 || res.Steps != 2 {
		t.Fatalf("checks %d, %+v, %v", checks, res, err)
	}
	if m := lastMessage(r.stub.Requests()[1]); m.Role != agent.RoleUser || m.Text != "no rows; try another road" {
		t.Errorf("refusal %+v", m)
	}
}

// A host's config sets the numbers it names, and the rest keep the defaults.
func TestGraphQueryTuningLoadsFromJSON(t *testing.T) {
	var tune GraphQueryTuning
	if err := json.Unmarshal([]byte(`{"maxSteps": 5, "temperature": 0.2, "penalties": {}}`), &tune); err != nil {
		t.Fatal(err)
	}
	cfg, err := GraphQuery{Provider: llama{agent.NewStubProvider()}, Tuning: tune}.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxSteps != 5 || *cfg.Generation.Temperature != 0.2 || cfg.Generation.MaxTokens != 512 || cfg.Generation.Penalties != nil {
		t.Errorf("steps %d, generation %+v", cfg.MaxSteps, cfg.Generation)
	}
}

func TestGraphQueryRequiresAProvider(t *testing.T) {
	if _, err := (GraphQuery{}).Config(); err == nil {
		t.Error("no error without a Provider")
	}
}

// A host swaps one stage and drops another by name, and appends its own.
func TestReplaceAndRemoveWorkByName(t *testing.T) {
	cfg, err := GraphQuery{Provider: llama{agent.NewStubProvider()}, Retrievers: []agent.Retriever{
		agent.NewRetriever("none", func(context.Context, agent.RetrievalQuery) ([]agent.Snippet, error) { return nil, nil }),
	}}.Config()
	if err != nil {
		t.Fatal(err)
	}
	before := stageNames(cfg.StepStages)
	mine := agent.StepStage{Name: "host.actions", Scope: agent.ScopeRequest}
	swapped, ok := Replace(cfg.StepStages, StageActions, mine)
	if !ok {
		t.Fatal("no stage named", StageActions)
	}
	swapped = Remove(swapped, StageRetrieval)
	if got := stageNames(swapped); got != StageStuck+" host.actions" {
		t.Errorf("stages %q", got)
	}
	if stageNames(cfg.StepStages) != before {
		t.Errorf("Replace or Remove changed the preset's slice")
	}
	if _, ok := Replace(cfg.StepStages, "presets.graph_query.typo", mine); ok {
		t.Error("Replace found a stage that isn't there")
	}
}

func stageNames(stages []agent.StepStage) string {
	var names []string
	for _, s := range stages {
		names = append(names, s.Name)
	}
	return strings.Join(names, " ")
}

func ptr[T any](v T) *T { return &v }
