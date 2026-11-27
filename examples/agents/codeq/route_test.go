package codeq

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/chakra"
)

// The router names every question's tag from its wording alone.
func TestRouteNamesEachQuestionsTag(t *testing.T) {
	for _, q := range questions {
		if got := route(q.text); got != q.tag {
			t.Errorf("route(%q) = %q, want %q", q.text, got, q.tag)
		}
	}
}

// The routed note reaches the model call and stays out of the messages the
// stage was given, so it never joins the record.
func TestRouterAddsTheNoteForOneCall(t *testing.T) {
	q := questions[12]
	in := []agent.Message{{Role: agent.RoleUser, Text: q.text}}
	v, err := router().Run(context.Background(), agent.StepInfo{}, agent.StepView{Messages: in})
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 {
		t.Fatalf("the stage changed its input: %v", in)
	}
	last := v.Messages[len(v.Messages)-1]
	if last.Role != agent.RoleSystem || !strings.Contains(last.Text, "more than one lookup") {
		t.Errorf("last message = %+v, want the multi-hop note", last)
	}
	if router().Scope != agent.ScopeRequest {
		t.Errorf("scope = %v, want ScopeRequest", router().Scope)
	}
}
