package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// refuseUntil is a TurnEndHook that refuses with msg until it has seen n
// answers, recording each answer it judged.
func refuseUntil(n int, msg string, seen *[]TurnEndInfo) TurnEndHook {
	return func(_ context.Context, info TurnEndInfo) (TurnEndVerdict, error) {
		*seen = append(*seen, info)
		if len(*seen) <= n {
			return TurnEndVerdict{Continue: msg}, nil
		}
		return TurnEndVerdict{}, nil
	}
}

func runTurnEnd(t *testing.T, cfg RunnerConfig) (*TurnResult, []Event, error) {
	t.Helper()
	r, err := NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "fix the tests"}}, func(e Event) { events = append(events, e) })
	return res, events, err
}

func kindsOf(events []Event, k EventKind) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// A hook that accepts leaves the turn exactly as it was without one: one
// step, the answer as the result, no continuation.
func TestTurnEndHookThatAcceptsChangesNothing(t *testing.T) {
	var seen []TurnEndInfo
	stub := NewStubProvider(StubTurn{Text: "done"})
	res, events, err := runTurnEnd(t, RunnerConfig{Provider: stub, TurnEnd: []TurnEndHook{refuseUntil(0, "", &seen)}})
	if err != nil || res.Text != "done" || res.Steps != 1 || len(res.Messages) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(seen) != 1 || seen[0].Text != "done" || seen[0].Step != 1 || seen[0].MaxSteps != DefaultMaxSteps || len(seen[0].Messages) != 1 {
		t.Errorf("the hook saw %+v", seen)
	}
	if len(kindsOf(events, EventTurnContinued)) != 0 {
		t.Errorf("an accepted answer emitted a continuation")
	}
}

// A refusal puts its message in the record as a system note, emits
// turn-continued, and the model answers again with the note in view; the
// second answer, accepted, is the turn's.
func TestTurnEndHookRefusesThenAccepts(t *testing.T) {
	var seen []TurnEndInfo
	const keepGoing = "the tests are still red; keep going"
	stub := NewStubProvider(StubTurn{Text: "done"}, StubTurn{Text: "now really done"})
	res, events, err := runTurnEnd(t, RunnerConfig{Provider: stub, TurnEnd: []TurnEndHook{refuseUntil(1, keepGoing, &seen)}})
	if err != nil || res.Text != "now really done" || res.Steps != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	roles := []string{}
	for _, m := range res.Messages {
		roles = append(roles, string(m.Role))
	}
	if got := strings.Join(roles, ","); got != "assistant,system,assistant" || res.Messages[1].Text != keepGoing {
		t.Errorf("recorded %s, note %q", got, res.Messages[1].Text)
	}
	second := stub.Requests()[1].Messages
	if last := second[len(second)-1]; last.Role != RoleSystem || last.Text != keepGoing {
		t.Errorf("the second call ended with %+v, want the note", last)
	}
	cont := kindsOf(events, EventTurnContinued)
	if len(cont) != 1 || cont[0].Step != 1 || cont[0].Reason != keepGoing {
		t.Errorf("continued events %+v", cont)
	}
	if len(seen) != 2 || seen[1].Step != 2 || len(seen[1].Messages) != 3 {
		t.Errorf("the hook's second look %+v", seen)
	}
}

// A hook that never accepts can't loop: the refusals use up MaxSteps and the
// turn ends with ErrMaxSteps and the partial record, notes included.
func TestTurnEndHookThatNeverAcceptsHitsTheStepCap(t *testing.T) {
	var seen []TurnEndInfo
	stub := NewStubProvider(StubTurn{Text: "a"}, StubTurn{Text: "b"}, StubTurn{Text: "c"}, StubTurn{Text: "d"})
	res, events, err := runTurnEnd(t, RunnerConfig{Provider: stub, MaxSteps: 3, TurnEnd: []TurnEndHook{refuseUntil(99, "no", &seen)}})
	if !errors.Is(err, ErrMaxSteps) || res == nil || res.Steps != 3 || len(res.Messages) != 6 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(stub.Requests()) != 3 || len(seen) != 3 || len(kindsOf(events, EventTurnContinued)) != 3 {
		t.Errorf("%d calls, %d judged, %d continued", len(stub.Requests()), len(seen), len(kindsOf(events, EventTurnContinued)))
	}
}

// A refusal counts against the tree budget too: a budget of two steps stops
// a turn whose hook refuses forever after two model calls.
func TestTurnEndRefusalsCountAgainstTheTreeBudget(t *testing.T) {
	var seen []TurnEndInfo
	stub := NewStubProvider(StubTurn{Text: "a"}, StubTurn{Text: "b"}, StubTurn{Text: "c"})
	r, err := NewRunner(RunnerConfig{Provider: stub, TurnEnd: []TurnEndHook{refuseUntil(99, "no", &seen)}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithTreeBudget(context.Background(), TreeBudget{MaxSteps: 2})
	_, err = r.Run(ctx, []Message{{Role: RoleUser, Text: "q"}}, nil)
	if !errors.Is(err, ErrTreeBudget) || len(stub.Requests()) != 2 {
		t.Fatalf("%v after %d calls", err, len(stub.Requests()))
	}
}

// Turn-end hooks run before ResponseSchema's finalizing call, so a refused
// answer is never coerced, and the structured answer comes from the
// accepted one.
func TestTurnEndRunsBeforeStructuredFinalize(t *testing.T) {
	var seen []TurnEndInfo
	schema := core.NewRawJSON(json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`))
	stub := NewStubProvider(
		StubTurn{Text: "41"},
		StubTurn{Text: "42"},
		StubTurn{Text: `{"answer":42}`},
	)
	res, _, err := runTurnEnd(t, RunnerConfig{Provider: stub, ResponseSchema: schema, TurnEnd: []TurnEndHook{refuseUntil(1, "check again", &seen)}})
	if err != nil || string(res.Structured.Raw()) != `{"answer":42}` {
		t.Fatalf("%+v %v", res, err)
	}
	reqs := stub.Requests()
	if len(reqs) != 3 || reqs[0].ResponseSchema.Len() != 0 || reqs[1].ResponseSchema.Len() != 0 || reqs[2].ResponseSchema.Len() == 0 {
		t.Errorf("only the third call should finalize: %d calls", len(reqs))
	}
}

// Hooks run in order and the first refusal wins, so a later hook isn't asked
// about an answer already refused. An error fails the turn.
func TestTurnEndHooksRunInOrder(t *testing.T) {
	var first, second []TurnEndInfo
	stub := NewStubProvider(StubTurn{Text: "a"}, StubTurn{Text: "b"})
	res, events, err := runTurnEnd(t, RunnerConfig{Provider: stub, TurnEnd: []TurnEndHook{refuseUntil(1, "first", &first), refuseUntil(0, "second", &second)}})
	if err != nil || res.Text != "b" || len(second) != 1 || second[0].Text != "b" {
		t.Fatalf("%v, the second hook judged %+v", err, second)
	}
	if c := kindsOf(events, EventTurnContinued); len(c) != 1 || c[0].Reason != "first" {
		t.Errorf("continued %+v", c)
	}
	boom := func(context.Context, TurnEndInfo) (TurnEndVerdict, error) {
		return TurnEndVerdict{}, errors.New("checker down")
	}
	_, _, err = runTurnEnd(t, RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "a"}), TurnEnd: []TurnEndHook{boom}})
	if err == nil || !strings.Contains(err.Error(), "turn-end hook: checker down") {
		t.Errorf("a failing hook: %v", err)
	}
}
