package eval

import (
	"testing"

	"github.com/panyam/chakra"
)

func TestRowsWantsEveryRowAndNoOtherKnownRow(t *testing.T) {
	among := []string{"example.com/shop/orders", "example.com/shop/orders.Place", "example.com/shop/payments", "example.com/ledger/audit"}
	s := Rows([]string{"example.com/shop/orders", "example.com/shop/payments"}, among)
	for _, c := range []struct {
		text string
		pass bool
	}{
		{"example.com/shop/orders\nexample.com/shop/payments", true},
		{"- `example.com/shop/payments`\n- `example.com/shop/orders`", true},
		{"Both example.com/shop/orders and example.com/shop/payments import it.", true},
		{"example.com/shop/orders", false},
		{"example.com/shop/orders\nexample.com/shop/payments\nexample.com/ledger/audit", false},
		{"example.com/shop/orders.Place\nexample.com/shop/payments", false},
		{"orders and payments", false},
		{"Two packages import it:\n- example.com/shop/orders\n- `example.com/shop/payments`\n\nexample.com/ledger/audit imports it too, but it's in another module.", true},
		{"- example.com/shop/orders\n- example.com/ledger/audit\n\nexample.com/shop/payments imports it too.", false},
	} {
		got := s.Score(Result{Turn: &agent.TurnResult{Text: c.text}})
		if got.Pass != c.pass {
			t.Errorf("%q: pass = %v, want %v (%s)", c.text, got.Pass, c.pass, got.Detail)
		}
	}
	if got := s.Score(Result{}); got.Pass {
		t.Errorf("a failed run passed: %+v", got)
	}
}
