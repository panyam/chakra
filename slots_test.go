package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// keepLast is a compactor that drops every message but the last, once there
// are more than two.
type keepLast struct{}

func (keepLast) Compact(_ context.Context, h []Message) ([]Message, error) {
	if len(h) <= 2 {
		return h, nil
	}
	return h[len(h)-1:], nil
}

func slotRunner(t *testing.T, slots *Slots, extra ToolSource, compactor Compactor, turns ...StubTurn) (*StubProvider, *TurnResult, []Event) {
	t.Helper()
	tools := NewMultiSource()
	if err := tools.Add("slots", slots.Tool("facts", "attempts")); err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		if err := tools.Add("extra", extra); err != nil {
			t.Fatal(err)
		}
	}
	stub := NewStubProvider(turns...)
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: tools, MaxSteps: 10, Compactor: compactor, StepStages: []StepStage{slots.StepStage()}})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "find the modules"}}, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	return stub, res, events
}

func newTestSlots(t *testing.T) *Slots {
	t.Helper()
	s, err := NewSlots(
		SlotSpec{Name: "plan"},
		SlotSpec{Name: "facts", Mode: SlotAppend, MaxBytes: 40},
		SlotSpec{Name: "attempts", Mode: SlotAppend},
	)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writeSlot(id, slot, text string) StubTurn {
	return StubTurn{ToolCalls: []ToolCall{call(id, SlotToolName, `{"slot":"`+slot+`","text":"`+text+`"}`)}}
}

func requestText(req ProviderRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Text + "\n")
	}
	return b.String()
}

// Imagine the model learns early that no node has kind module, then a
// compaction drops everything but the last message. The fact still reaches
// every later call, because it sits in a slot rather than in the history.
func TestSlotSurvivesCompaction(t *testing.T) {
	slots := newTestSlots(t)
	stub, _, _ := slotRunner(t, slots, nil, keepLast{},
		writeSlot("c1", "facts", "no node has kind module"),
		runCall(2, "a"), runCall(3, "b"), runCall(4, "c"),
		StubTurn{Text: "done"})
	reqs := stub.Requests()
	last := reqs[len(reqs)-1]
	if len(last.Messages) > 3 {
		t.Fatalf("the compactor left %d messages, want the history cut down", len(last.Messages))
	}
	if !strings.Contains(requestText(last), "no node has kind module") {
		t.Errorf("the last call lost the fact:\n%s", requestText(last))
	}
	if strings.Contains(requestText(reqs[0]), "Your notes") {
		t.Errorf("the first call shows notes before any were written")
	}
}

func TestSlotsStayOutOfTheRecord(t *testing.T) {
	slots := newTestSlots(t)
	_, res, events := slotRunner(t, slots, nil, nil,
		writeSlot("c1", "facts", "go test needs -race"), StubTurn{Text: "done"})
	for _, m := range res.Messages {
		if strings.Contains(m.Text, "Your notes") {
			t.Fatalf("the turn's record kept the rendered slots: %q", m.Text)
		}
	}
	var got []SlotUpdate
	for _, e := range events {
		if e.Kind == EventSlot {
			got = append(got, *e.Slot)
		}
	}
	if !slices.Equal(got, []SlotUpdate{{Name: "facts", Value: "go test needs -race"}}) {
		t.Errorf("slot events = %+v", got)
	}
}

func TestPlanToolWritesThePlanSlot(t *testing.T) {
	slots := newTestSlots(t)
	plan := NewPlanTool().WriteTo(slots, "plan")
	stub, _, _ := slotRunner(t, slots, plan, keepLast{},
		StubTurn{ToolCalls: []ToolCall{call("p1", PlanToolName, `{"plan":[{"step":"read the schema","status":"in_progress"},{"step":"write the query","status":"pending"}]}`)}},
		runCall(2, "a"), runCall(3, "b"), StubTurn{Text: "done"})
	if got := slots.Get("plan"); !strings.Contains(got, "read the schema") {
		t.Fatalf("plan slot = %q", got)
	}
	reqs := stub.Requests()
	last := requestText(reqs[len(reqs)-1])
	if strings.Count(last, "read the schema") != 1 {
		t.Errorf("the last call should show the plan once, from the slot:\n%s", last)
	}
}

func TestSlotWrites(t *testing.T) {
	ctx := context.Background()
	s := newTestSlots(t)
	for _, line := range []string{"one is first", "two is second", "three is third"} {
		if _, err := s.Write(ctx, "facts", line); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Get("facts"); got != "two is second\nthree is third" {
		t.Errorf("append slot past its cap = %q, want the oldest line dropped", got)
	}
	if _, err := s.Write(ctx, "facts", strings.Repeat("x", 41)); err == nil {
		t.Errorf("an append longer than the whole cap was accepted")
	}
	if _, err := s.Write(ctx, "plan", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(ctx, "plan", "second"); err != nil || s.Get("plan") != "second" {
		t.Errorf("replace slot = %q, %v", s.Get("plan"), err)
	}
	if _, err := s.Write(ctx, "plan", strings.Repeat("x", 2001)); err == nil || s.Get("plan") != "second" {
		t.Errorf("an oversize replace changed the slot to %q, err %v", s.Get("plan"), err)
	}
	if _, err := s.Write(ctx, "ledger", "x"); !errors.Is(err, ErrUnknownSlot) {
		t.Errorf("write to an undeclared slot: %v", err)
	}
}

func TestRestoreSlotsTakesTheLastWrite(t *testing.T) {
	s := newTestSlots(t)
	if _, err := s.Write(context.Background(), "attempts", "stale"); err != nil {
		t.Fatal(err)
	}
	s.RestoreSlots([]Event{
		{Kind: EventSlot, Slot: &SlotUpdate{Name: "facts", Value: "a"}},
		{Kind: EventStep},
		{Kind: EventSlot, Slot: &SlotUpdate{Name: "facts", Value: "a\nb"}},
		{Kind: EventSlot, Slot: &SlotUpdate{Name: "ledger", Value: "ignored"}},
	})
	if s.Get("facts") != "a\nb" || s.Get("attempts") != "" || s.Get("ledger") != "" {
		t.Errorf("restored facts=%q attempts=%q ledger=%q", s.Get("facts"), s.Get("attempts"), s.Get("ledger"))
	}
}

func TestSlotToolRefusesASlotItWasntGiven(t *testing.T) {
	slots := newTestSlots(t)
	_, res, _ := slotRunner(t, slots, nil, nil, writeSlot("c1", "plan", "mine now"), StubTurn{Text: "done"})
	if slots.Get("plan") != "" {
		t.Fatalf("write_slot wrote the plan slot, which it wasn't given")
	}
	var refused bool
	for _, m := range res.Messages {
		refused = refused || (m.Role == RoleTool && strings.Contains(m.Text, `no slot "plan"`))
	}
	if !refused {
		t.Errorf("the model wasn't told the slot is off limits")
	}
}

func TestNewSlotsRejectsBadSpecs(t *testing.T) {
	for name, specs := range map[string][]SlotSpec{
		"no name":  {{}},
		"twice":    {{Name: "a"}, {Name: "a"}},
		"bad mode": {{Name: "a", Mode: "merge"}},
	} {
		if _, err := NewSlots(specs...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
