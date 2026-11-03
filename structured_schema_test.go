package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

var answerSchema = core.NewRawJSON(json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`))

func runSchemaTurn(t *testing.T, cfgSchema, turnSchema core.RawJSON, turns ...StubTurn) (*TurnResult, *StubProvider, error) {
	t.Helper()
	stub := NewStubProvider(turns...)
	r, err := NewRunner(RunnerConfig{Provider: stub, ResponseSchema: cfgSchema})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.RunTurn(context.Background(), TurnRequest{
		History:        []Message{{Role: RoleUser, Text: "go"}},
		ResponseSchema: turnSchema,
	})
	return res, stub, err
}

func TestStructuredRetriesWithTheValidationErrors(t *testing.T) {
	res, stub, err := runSchemaTurn(t, answerSchema, core.RawJSON{},
		StubTurn{Text: "done"},
		StubTurn{Text: `{"answer":"forty-two"}`},
		StubTurn{Text: `{"answer":42}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Structured.Raw()) != `{"answer":42}` || res.StructuredError != "" {
		t.Fatalf("structured = %s, error %q", res.Structured.Raw(), res.StructuredError)
	}
	retry := stub.Requests()[2].Messages
	feedback := retry[len(retry)-1]
	if feedback.Role != RoleUser || !strings.Contains(feedback.Text, "/answer") {
		t.Errorf("the retry didn't tell the model what was wrong: %+v", feedback)
	}
}

func TestStructuredErrorWhenNoAttemptValidates(t *testing.T) {
	res, _, err := runSchemaTurn(t, answerSchema, core.RawJSON{},
		StubTurn{Text: "done"},
		StubTurn{Text: `{"answer":"x"}`},
		StubTurn{Text: `{"nope":1}`},
	)
	if err != nil {
		t.Fatalf("the turn itself should succeed: %v", err)
	}
	if !strings.Contains(res.StructuredError, "answer") {
		t.Errorf("StructuredError = %q, want the last attempt's validation errors", res.StructuredError)
	}
}

func TestTurnSchemaOverridesTheConfig(t *testing.T) {
	res, stub, err := runSchemaTurn(t, core.RawJSON{}, answerSchema,
		StubTurn{Text: "done"},
		StubTurn{Text: "```json\n{\"answer\":7}\n```"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Structured.Raw()) != `{"answer":7}` || res.StructuredError != "" {
		t.Fatalf("structured = %s, error %q", res.Structured.Raw(), res.StructuredError)
	}
	if stub.Requests()[1].ResponseSchema.Len() == 0 {
		t.Error("the finalizing request didn't carry the turn's schema")
	}
}

func TestBadSchemaFailsBeforeAnyModelCall(t *testing.T) {
	_, stub, err := runSchemaTurn(t, core.RawJSON{}, core.NewRawJSON(json.RawMessage(`{"type":"nonsense"}`)), StubTurn{Text: "done"})
	if err == nil || !strings.Contains(err.Error(), "response schema") {
		t.Fatalf("err = %v, want a response schema error", err)
	}
	if len(stub.Requests()) != 0 {
		t.Errorf("%d model calls before the schema was checked", len(stub.Requests()))
	}
}
