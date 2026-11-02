// Package stats has small helpers over integer slices.
package stats

// Sum returns the total of xs. An empty slice sums to 0.
func Sum(xs []int) int {
	total := 0
	for i := 1; i < len(xs); i++ {
		total += xs[i]
	}
	return total
}

// Mean returns the average of xs, or 0 for an empty slice.
func Mean(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	return float64(Sum(xs)) / float64(len(xs))
}
