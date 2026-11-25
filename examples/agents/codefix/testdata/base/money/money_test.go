package money

import "testing"

func TestSum(t *testing.T) {
	if got := Sum([]int{100, 250, 50}); got != 400 {
		t.Fatalf("Sum = %d, want 400", got)
	}
}

func TestMax(t *testing.T) {
	if got := Max([]int{100, 250, 50}); got != 250 {
		t.Fatalf("Max = %d, want 250", got)
	}
}
