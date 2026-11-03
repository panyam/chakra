package stats

import "testing"

func TestSum(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{nil, 0},
		{[]int{5}, 5},
		{[]int{1, 2, 3}, 6},
	}
	for _, c := range cases {
		if got := Sum(c.in); got != c.want {
			t.Errorf("Sum(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMean(t *testing.T) {
	if got := Mean([]int{2, 4}); got != 3 {
		t.Errorf("Mean([2 4]) = %v, want 3", got)
	}
}
