// Package discount takes money off big orders.
package discount

// Threshold is the order total, in cents, from which the discount applies.
const Threshold = 1000

// Apply takes pct percent off total when total is at least Threshold,
// rounding the discount to the nearest cent.
func Apply(total, pct int) int {
	if total >= Threshold {
		return total - (total*pct+50)/100
	}
	return total
}
