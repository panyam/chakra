package checkpoint

import (
	"context"
	"testing"

	"github.com/panyam/chakra"
)

// TestCompensateAloneIsNotReversible pins the distinction that the rest of
// the design depends on. A tool that can only offer a compensating action does
// NOT qualify for automatic approval. Deleting an issue is a new call with its
// own consequences, and it is not an inverse of creating one. Flipping this
// would let a tool auto-approve because of an offset that nobody verified.
func TestCompensateAloneIsNotReversible(t *testing.T) {
	r := Reversal{Compensate: &agent.ToolCall{Name: "delete_issue"}}
	if r.Reversible() {
		t.Fatal("a compensation-only reversal must not count as reversible")
	}
	if r.IsZero() {
		t.Fatal("a compensation is still something to offer, so not zero")
	}
}

func TestReversalStates(t *testing.T) {
	restore := func(context.Context) error { return nil }
	cases := map[string]struct {
		rev              Reversal
		zero, reversible bool
	}{
		"nothing offered": {Reversal{}, true, false},
		"restore only":    {Reversal{Restore: restore}, false, true},
		"compensate only": {Reversal{Compensate: &agent.ToolCall{Name: "x"}}, false, false},
		"both":            {Reversal{Restore: restore, Compensate: &agent.ToolCall{Name: "x"}}, false, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.rev.IsZero(); got != tc.zero {
				t.Errorf("IsZero() = %v, want %v", got, tc.zero)
			}
			if got := tc.rev.Reversible(); got != tc.reversible {
				t.Errorf("Reversible() = %v, want %v", got, tc.reversible)
			}
		})
	}
}
