// Package money holds amounts in cents.
package money

import "fmt"

// Amount is a sum of money in cents.
type Amount int64

// Add returns a plus b.
func Add(a, b Amount) Amount { return a + b }

// Format prints a as dollars and cents.
func Format(a Amount) string { return fmt.Sprintf("$%d.%02d", a/100, a%100) }
