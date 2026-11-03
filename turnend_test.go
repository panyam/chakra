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

// Turn-end hooks judge ResponseSchema's finalized document, with the prose it
// came from as the draft, so a hook refuses what the turn would return. A
// refusal re-runs the finalizing call on the next answer.
func TestTurnEndJudgesTheStructuredAnswer(t *testing.T) {
	var seen []TurnEndInfo
	schema := core.NewRawJSON(json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`))
	stub := NewStubProvider(
		StubTurn{Text: "41"},
		StubTurn{Text: `{"answer":41}`},
		StubTurn{Text: "42"},
		StubTurn{Text: `{"answer":42}`},
	)
	res, _, err := runTurnEnd(t, RunnerConfig{Provider: stub, ResponseSchema: schema, TurnEnd: []TurnEndHook{refuseUntil(1, "check again", &seen)}})
	if err != nil || string(res.Structured.Raw()) != `{"answer":42}` || res.Text != "42" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(seen) != 2 || seen[0].Text != `{"answer":41}` || seen[0].Draft != "41" || seen[1].Text != `{"answer":42}` || seen[1].StructuredError != "" {
		t.Fatalf("the hook saw %+v", seen)
	}
	reqs := stub.Requests()
	if len(reqs) != 4 || reqs[0].ResponseSchema.Len() != 0 || reqs[1].ResponseSchema.Len() == 0 || reqs[2].ResponseSchema.Len() != 0 || reqs[3].ResponseSchema.Len() == 0 {
		t.Errorf("each answer should be finalized before it is judged: %d calls", len(reqs))
	}
}

// A document that never validates reaches the hook with StructuredError set,
// so the hook can send the model back rather than accept a broken result.
func TestTurnEndCanRefuseAnInvalidStructuredAnswer(t *testing.T) {
	schema := core.NewRawJSON(json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`))
	stub := NewStubProvider(
		StubTurn{Text: "dunno"}, StubTurn{Text: `{}`}, StubTurn{Text: `{}`},
		StubTurn{Text: "42"}, StubTurn{Text: `{"answer":42}`},
	)
	var seen []TurnEndInfo
	hook := func(_ context.Context, info TurnEndInfo) (TurnEndVerdict, error) {
		seen = append(seen, info)
		if info.StructuredError != "" {
			return TurnEndVerdict{Continue: "the result is missing answer"}, nil
		}
		return TurnEndVerdict{}, nil
	}
	res, _, err := runTurnEnd(t, RunnerConfig{Provider: stub, ResponseSchema: schema, TurnEnd: []TurnEndHook{hook}})
	if err != nil || string(res.Structured.Raw()) != `{"answer":42}` || res.StructuredError != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(seen) != 2 || seen[0].StructuredError == "" || seen[0].Text != `{}` {
		t.Fatalf("the hook saw %+v", seen)
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

// A Stop ends the turn on the refused answer: no further model call, an
// error wrapping ErrTurnStopped with the reason, and a partial result whose
// record ends with the answer the hook refused.
func TestTurnEndStopEndsTheTurnWithThePartialRecord(t *testing.T) {
	stop := func(_ context.Context, info TurnEndInfo) (TurnEndVerdict, error) {
		return TurnEndVerdict{Stop: "resent a draft already refused"}, nil
	}
	stub := NewStubProvider(StubTurn{Text: "same draft"}, StubTurn{Text: "never asked"})
	res, events, err := runTurnEnd(t, RunnerConfig{Provider: stub, TurnEnd: []TurnEndHook{stop}})
	if !errors.Is(err, ErrTurnStopped) || !strings.Contains(err.Error(), "resent a draft already refused") {
		t.Fatalf("err = %v", err)
	}
	if res == nil || res.Steps != 1 || res.Text != "" || len(res.Messages) != 1 {
		t.Fatalf("partial result %+v", res)
	}
	if last := res.Messages[0]; last.Role != RoleAssistant || last.Text != "same draft" {
		t.Errorf("the record ends with %+v, want the refused answer", last)
	}
	if n := len(stub.Requests()); n != 1 {
		t.Errorf("%d model calls, want 1", n)
	}
	if len(kindsOf(events, EventTurnContinued)) != 0 || len(kindsOf(events, EventTurnEnd)) != 0 || len(kindsOf(events, EventError)) != 1 {
		t.Errorf("events %+v", events)
	}
}

// A Stop from a later hook still wins over an earlier hook that accepted,
// and a Continue from an earlier hook wins over a later Stop.
func TestTurnEndFirstHookThatDoesNotAcceptWins(t *testing.T) {
	accept := func(context.Context, TurnEndInfo) (TurnEndVerdict, error) { return TurnEndVerdict{}, nil }
	stop := func(context.Context, TurnEndInfo) (TurnEndVerdict, error) { return TurnEndVerdict{Stop: "no"}, nil }
	_, _, err := runTurnEnd(t, RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "a"}), TurnEnd: []TurnEndHook{accept, stop}})
	if !errors.Is(err, ErrTurnStopped) {
		t.Fatalf("accept then stop: %v", err)
	}
	var seen []TurnEndInfo
	stub := NewStubProvider(StubTurn{Text: "a"}, StubTurn{Text: "b"})
	res, _, err := runTurnEnd(t, RunnerConfig{Provider: stub, TurnEnd: []TurnEndHook{refuseUntil(1, "again", &seen), stop}})
	if !errors.Is(err, ErrTurnStopped) || res.Steps != 2 {
		t.Fatalf("continue then stop: %+v %v", res, err)
	}
}

func TestTurnEndStopAndContinueTogetherIsAnError(t *testing.T) {
	both := func(context.Context, TurnEndInfo) (TurnEndVerdict, error) {
		return TurnEndVerdict{Stop: "no", Continue: "again"}, nil
	}
	stub := NewStubProvider(StubTurn{Text: "a"}, StubTurn{Text: "b"})
	res, _, err := runTurnEnd(t, RunnerConfig{Provider: stub, TurnEnd: []TurnEndHook{both}})
	if err == nil || errors.Is(err, ErrTurnStopped) || res != nil || len(stub.Requests()) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

// AsUser sends the Continue note as a user message, in the record and on the
// next request; without it the note stays a system note.
func TestTurnEndContinueAsUserReachesTheNextRequestAsAUserMessage(t *testing.T) {
	for _, asUser := range []bool{true, false} {
		want := RoleSystem
		if asUser {
			want = RoleUser
		}
		n := 0
		hook := func(context.Context, TurnEndInfo) (TurnEndVerdict, error) {
			n++
			if n == 1 {
				return TurnEndVerdict{Continue: "this draft failed; change it", AsUser: asUser}, nil
			}
			return TurnEndVerdict{}, nil
		}
		stub := NewStubProvider(StubTurn{Text: "draft"}, StubTurn{Text: "fixed"})
		res, _, err := runTurnEnd(t, RunnerConfig{Provider: stub, TurnEnd: []TurnEndHook{hook}})
		if err != nil || res.Text != "fixed" || len(res.Messages) != 3 {
			t.Fatalf("asUser=%v: %+v %v", asUser, res, err)
		}
		if note := res.Messages[1]; note.Role != want || note.Text != "this draft failed; change it" {
			t.Errorf("asUser=%v: recorded %+v", asUser, note)
		}
		second := stub.Requests()[1].Messages
		if last := second[len(second)-1]; last.Role != want || last.Text != "this draft failed; change it" {
			t.Errorf("asUser=%v: the next request ended with %+v", asUser, last)
		}
	}
}
