package discount

import "testing"

func TestApplyAtThreshold(t *testing.T) {
	if got := Apply(1000, 10); got != 900 {
		t.Fatalf("Apply(1000, 10) = %d, want 900", got)
	}
}

func TestApplyBelowThreshold(t *testing.T) {
	if got := Apply(999, 10); got != 999 {
		t.Fatalf("Apply(999, 10) = %d, want 999", got)
	}
}

func TestApplyRounds(t *testing.T) {
	if got := Apply(1005, 10); got != 904 {
		t.Fatalf("Apply(1005, 10) = %d, want 904", got)
	}
}

func TestThresholdIsTenDollars(t *testing.T) {
	if Threshold != 1000 {
		t.Fatalf("Threshold = %d cents, want 1000 ($10.00)", Threshold)
	}
}
