// Package report prints a cart's summary line.
package report

import (
	"fmt"

	"example.com/shopkit/cart"
	"example.com/shopkit/discount"
)

// Summary is one line: the total after a 10% discount, and the priciest
// item, both in dollars.
func Summary(c *cart.Cart) string {
	total := discount.Apply(c.Total(), 10)
	return fmt.Sprintf("total %s, priciest %s", dollars(total), dollars(c.Priciest()))
}

// dollars prints cents as dollars and cents.
func dollars(cents int) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}
