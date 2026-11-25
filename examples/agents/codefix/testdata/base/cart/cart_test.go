package cart

import "testing"

func TestTotal(t *testing.T) {
	c := &Cart{Prices: []int{100, 250}}
	if got := c.Total(); got != 350 {
		t.Fatalf("Total = %d, want 350", got)
	}
}

func TestNilCartTotal(t *testing.T) {
	var c *Cart
	if got := c.Total(); got != 0 {
		t.Fatalf("nil Total = %d, want 0", got)
	}
}
