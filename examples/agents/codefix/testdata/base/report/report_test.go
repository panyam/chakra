package report

import (
	"testing"

	"example.com/shopkit/cart"
)

func TestSummary(t *testing.T) {
	c := &cart.Cart{Prices: []int{705, 300}}
	want := "total $9.04, priciest $7.05"
	if got := Summary(c); got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
}
