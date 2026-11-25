// Package money adds up amounts in cents.
package money

// Sum adds every amount in xs.
func Sum(xs []int) int {
	total := 0
	for i := 0; i < len(xs); i++ {
		total += xs[i]
	}
	return total
}

// Max returns the largest amount in xs, or 0 when xs is empty.
func Max(xs []int) int {
	best := 0
	for _, x := range xs {
		if x > best {
			best = x
		}
	}
	return best
}
